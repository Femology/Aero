package aero

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// JSON-RPC 2.0 error taxonomy.
const (
	ErrParse      = -32700
	ErrInvalid    = -32600
	ErrNotFound   = -32601
	ErrInternal   = -32603
)

const (
	maxFrameSize  = 16 << 20
	headerSize    = 4
	hmacKeyBytes  = 32
	hmacAck       = "AERO-OK"
)

var (
	ErrFrameTooLarge   = errors.New("aero: frame exceeds maximum size")
	ErrHandshakeFailed = errors.New("aero: HMAC handshake failed")
	ErrInvalidHMACKey  = errors.New("aero: HMAC key must be 256 bits")
)

// HandlerFunc handles a JSON-RPC 2.0 request and returns a result.
type HandlerFunc func(params json.RawMessage) (any, error)

// NotifyFunc handles a JSON-RPC 2.0 notification (no response).
type NotifyFunc func(params json.RawMessage) error

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type pushMessage struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

// Conn wraps length-prefixed, HMAC-authenticated IPC streams.
type Conn struct {
	r *bufio.Reader
	w io.Writer
}

// WrapIO returns a Conn over established streams (pre-handshake).
func WrapIO(r io.Reader, w io.Writer) *Conn {
	return &Conn{r: bufio.NewReader(r), w: w}
}

// Multiplexer is the async JSON-RPC router: concurrent requests + push events.
type Multiplexer struct {
	mu        sync.RWMutex
	handlers  map[string]HandlerFunc
	notifies  map[string]NotifyFunc
	assets    *AssetStore
	writeMu   sync.Mutex
	conn      *Conn
}

// NewMultiplexer constructs an empty IPC router.
func NewMultiplexer() *Multiplexer {
	return &Multiplexer{
		handlers: make(map[string]HandlerFunc),
		notifies: make(map[string]NotifyFunc),
	}
}

// Register binds a JSON-RPC method that expects a correlated response (has id).
func (m *Multiplexer) Register(name string, fn HandlerFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers[name] = fn
}

// RegisterNotify binds a one-way notification handler (no id, no response).
func (m *Multiplexer) RegisterNotify(name string, fn NotifyFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.notifies[name] = fn
}

// OnEvent registers a push-channel producer (legacy alias for event emitters).
func (m *Multiplexer) OnEvent(name string, fn EventFunc) {
	m.Register(name, func(_ json.RawMessage) (any, error) {
		return fn()
	})
}

// AttachAssets registers the aero.asset handler backed by store.
func (m *Multiplexer) AttachAssets(store *AssetStore) {
	m.assets = store
	m.Register("aero.asset", func(params json.RawMessage) (any, error) {
		var req struct {
			Path   string `json:"path"`
			Range  string `json:"range,omitempty"`
			Method string `json:"method,omitempty"`
		}
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, fmt.Errorf("invalid params")
		}
		if m.assets == nil {
			return nil, fmt.Errorf("asset store unavailable")
		}
		return m.assets.Handle(AssetRequest{
			Path:   req.Path,
			Range:  req.Range,
			Method: req.Method,
		})
	})
}

// Bind attaches the authenticated connection used for push events.
func (m *Multiplexer) Bind(conn *Conn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conn = conn
}

// Serve reads frames asynchronously and routes until ctx is cancelled.
func (m *Multiplexer) Serve(ctx context.Context, conn *Conn) {
	m.Bind(conn)
	readBuf := make([]byte, 0, 4096)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		payload, err := ReadFrame(conn.r, readBuf)
		if err != nil {
			return
		}
		frame := append([]byte(nil), payload...)
		go m.route(conn, frame)
	}
}

func (m *Multiplexer) route(conn *Conn, payload []byte) {
	var req rpcRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		_ = m.writeResponse(conn, rpcResponse{
			JSONRPC: "2.0",
			ID:      nil,
			Error:   &rpcError{Code: ErrParse, Message: "Parse error"},
		})
		return
	}

	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		_ = m.writeResponse(conn, rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: ErrInvalid, Message: "Invalid Request"},
		})
		return
	}

	if req.Method == "" {
		_ = m.writeResponse(conn, rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: ErrInvalid, Message: "Invalid Request"},
		})
		return
	}

	// Notification: method present, id omitted — no response frame.
	if req.ID == nil {
		m.dispatchNotify(req.Method, req.Params)
		return
	}

	m.dispatchRequest(conn, req)
}

func (m *Multiplexer) dispatchNotify(method string, params json.RawMessage) {
	m.mu.RLock()
	fn, ok := m.notifies[method]
	m.mu.RUnlock()
	if ok {
		_ = fn(params)
		return
	}
	// Fall back to request handlers for fire-and-forget compatibility.
	m.mu.RLock()
	handler, ok := m.handlers[method]
	m.mu.RUnlock()
	if ok {
		_, _ = handler(params)
	}
}

func (m *Multiplexer) dispatchRequest(conn *Conn, req rpcRequest) {
	m.mu.RLock()
	fn, ok := m.handlers[req.Method]
	m.mu.RUnlock()
	if !ok {
		_ = m.writeResponse(conn, rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: ErrNotFound, Message: "Method not found"},
		})
		return
	}

	result, err := fn(req.Params)
	if err != nil {
		code := ErrInternal
		if errors.Is(err, ErrSchemaValidation) {
			code = ErrInvalid
		}
		_ = m.writeResponse(conn, rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: code, Message: err.Error()},
		})
		return
	}
	_ = m.writeResponse(conn, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
}

// Push emits an unsolicited event to the shell (no id — UI dispatches CustomEvent).
func (m *Multiplexer) Push(event string, params any) error {
	m.mu.RLock()
	conn := m.conn
	m.mu.RUnlock()
	if conn == nil {
		return errors.New("aero: multiplexer not connected")
	}
	return m.writePush(conn, pushMessage{JSONRPC: "2.0", Method: event, Params: params})
}

// Emit is an alias for Push.
func (m *Multiplexer) Emit(event string, params any) error {
	return m.Push(event, params)
}

func (m *Multiplexer) writeResponse(conn *Conn, resp rpcResponse) error {
	b, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	return m.writeFrame(conn, b)
}

func (m *Multiplexer) writePush(conn *Conn, msg pushMessage) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return m.writeFrame(conn, b)
}

func (m *Multiplexer) writeFrame(conn *Conn, payload []byte) error {
	if conn == nil {
		return errors.New("aero: multiplexer not connected")
	}
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	return WriteFrame(conn.w, payload)
}

// WriteFrame writes a 4-byte big-endian length prefix followed by payload.
func WriteFrame(w io.Writer, payload []byte) error {
	if len(payload) > maxFrameSize {
		return ErrFrameTooLarge
	}
	var hdr [headerSize]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// ReadFrame reads a length-prefixed frame, reusing buf when capacity allows.
func ReadFrame(r *bufio.Reader, buf []byte) ([]byte, error) {
	var hdr [headerSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxFrameSize {
		return nil, ErrFrameTooLarge
	}
	if cap(buf) < int(n) {
		buf = make([]byte, n)
	} else {
		buf = buf[:n]
	}
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// ValidateHMACKey ensures the pipe key is exactly 256 bits.
func ValidateHMACKey(key []byte) error {
	if len(key) != hmacKeyBytes {
		return ErrInvalidHMACKey
	}
	return nil
}

// GenerateHMACKey returns a cryptographically random 256-bit key.
func GenerateHMACKey() ([]byte, error) {
	key := make([]byte, hmacKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("aero: generate HMAC key: %w", err)
	}
	return key, nil
}

// ServerHandshake authenticates the shell (server side, Go engine).
func ServerHandshake(conn *Conn, key []byte) error {
	return performHMACHandshake(conn, key, true)
}

// ClientHandshake authenticates from the shell side.
func ClientHandshake(conn *Conn, key []byte) error {
	return performHMACHandshake(conn, key, false)
}

func performHMACHandshake(conn *Conn, key []byte, server bool) error {
	if err := ValidateHMACKey(key); err != nil {
		return err
	}
	if server {
		challenge := make([]byte, hmacKeyBytes)
		if _, err := rand.Read(challenge); err != nil {
			return err
		}
		if err := WriteFrame(conn.w, challenge); err != nil {
			return err
		}
		response, err := ReadFrame(conn.r, nil)
		if err != nil {
			return err
		}
		if !hmac.Equal(response, sign(key, challenge)) {
			return ErrHandshakeFailed
		}
		return WriteFrame(conn.w, []byte(hmacAck))
	}

	challenge, err := ReadFrame(conn.r, nil)
	if err != nil {
		return err
	}
	if err := WriteFrame(conn.w, sign(key, challenge)); err != nil {
		return err
	}
	ack, err := ReadFrame(conn.r, nil)
	if err != nil {
		return err
	}
	if string(ack) != hmacAck {
		return ErrHandshakeFailed
	}
	return nil
}

func sign(key, msg []byte) []byte {
	m := hmac.New(sha256.New, key)
	_, _ = m.Write(msg)
	return m.Sum(nil)
}

// EventFunc produces unsolicited push payloads (legacy helper type).
type EventFunc func() (any, error)

// Legacy internal aliases used by handshake.go callers during migration.
func writeFrame(w io.Writer, payload []byte) error { return WriteFrame(w, payload) }
func readFrame(r *bufio.Reader, buf []byte) ([]byte, error) { return ReadFrame(r, buf) }
func hmacHandshake(conn *Conn, key []byte, server bool) error {
	return performHMACHandshake(conn, key, server)
}

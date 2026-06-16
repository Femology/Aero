package aero

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"testing"
	"time"
)

func TestWriteReadFrame(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte(`{"jsonrpc":"2.0","method":"ping"}`)
	if err := WriteFrame(&buf, payload); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 4+len(payload) {
		t.Fatalf("frame size: got %d want %d", buf.Len(), 4+len(payload))
	}
	got, err := ReadFrame(bufio.NewReader(bytes.NewReader(buf.Bytes())), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch: %q vs %q", got, payload)
	}
}

func TestHMACHandshake(t *testing.T) {
	key, err := GenerateHMACKey()
	if err != nil {
		t.Fatal(err)
	}

	// Cross-connected pipes: engine <-> shell
	engineRead, shellWrite := io.Pipe()
	shellRead, engineWrite := io.Pipe()

	errCh := make(chan error, 1)
	go func() {
		conn := WrapIO(shellRead, shellWrite)
		errCh <- ClientHandshake(conn, key)
	}()

	engineConn := WrapIO(engineRead, engineWrite)
	if err := ServerHandshake(engineConn, key); err != nil {
		t.Fatalf("server handshake: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("client handshake: %v", err)
	}
}

func TestValidateHMACKey(t *testing.T) {
	if err := ValidateHMACKey(make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateHMACKey(make([]byte, 16)); err != ErrInvalidHMACKey {
		t.Fatalf("expected ErrInvalidHMACKey, got %v", err)
	}
}

func ipcTestPipes() (engineConn, shellConn *Conn, shellWrite io.Writer, shellRead *bufio.Reader) {
	engineRead, sw := io.Pipe()
	sr, engineWrite := io.Pipe()
	return WrapIO(engineRead, engineWrite), WrapIO(sr, sw), sw, bufio.NewReader(sr)
}

func TestMultiplexerMethodNotFound(t *testing.T) {
	m := NewMultiplexer()
	engineConn, shellConn, shellWrite, shellRead := ipcTestPipes()
	key, _ := GenerateHMACKey()

	respCh := make(chan []byte, 1)
	go func() {
		if err := ClientHandshake(shellConn, key); err != nil {
			t.Errorf("client handshake: %v", err)
			return
		}
		req := []byte(`{"jsonrpc":"2.0","id":1,"method":"missing","params":{}}`)
		if err := WriteFrame(shellWrite, req); err != nil {
			t.Errorf("write: %v", err)
			return
		}
		resp, err := ReadFrame(shellRead, nil)
		if err != nil {
			t.Errorf("read: %v", err)
			return
		}
		respCh <- resp
	}()

	if err := ServerHandshake(engineConn, key); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Serve(ctx, engineConn)

	select {
	case resp := <-respCh:
		if !bytes.Contains(resp, []byte(`"code":-32601`)) {
			t.Fatalf("expected -32601, got %s", resp)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for RPC response")
	}
}

func TestMultiplexerParseError(t *testing.T) {
	m := NewMultiplexer()
	engineConn, shellConn, shellWrite, shellRead := ipcTestPipes()
	key, _ := GenerateHMACKey()

	respCh := make(chan []byte, 1)
	go func() {
		_ = ClientHandshake(shellConn, key)
		_ = WriteFrame(shellWrite, []byte(`{not json`))
		resp, _ := ReadFrame(shellRead, nil)
		respCh <- resp
	}()

	_ = ServerHandshake(engineConn, key)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Serve(ctx, engineConn)

	select {
	case resp := <-respCh:
		if !bytes.Contains(resp, []byte(`"code":-32700`)) {
			t.Fatalf("expected -32700, got %s", resp)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestMultiplexerPushOmitsID(t *testing.T) {
	m := NewMultiplexer()
	engineConn, shellConn, _, shellRead := ipcTestPipes()
	key, _ := GenerateHMACKey()

	pushCh := make(chan []byte, 1)
	go func() {
		_ = ClientHandshake(shellConn, key)
		frame, _ := ReadFrame(shellRead, nil)
		pushCh <- frame
	}()

	_ = ServerHandshake(engineConn, key)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Serve(ctx, engineConn)
	m.Bind(engineConn)

	if err := m.Push("aero.tick", map[string]int{"n": 1}); err != nil {
		t.Fatal(err)
	}

	select {
	case frame := <-pushCh:
		if bytes.Contains(frame, []byte(`"id"`)) {
			t.Fatalf("push must omit id, got %s", frame)
		}
		if !bytes.Contains(frame, []byte(`"method":"aero.tick"`)) {
			t.Fatalf("expected method in push, got %s", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for push")
	}
}

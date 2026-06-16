package aero

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
)

// Strict CSP injected on every embedded asset response (production).
const strictCSP = "default-src 'self' aero:; script-src 'self' 'unsafe-inline';"

// DefaultDevOrigin is the default Vite dev server URL.
const DefaultDevOrigin = "http://localhost:5173"

var (
	// ErrAssetNotFound indicates the requested virtual path is not registered.
	ErrAssetNotFound = errors.New("aero: asset not found")
	// ErrDevModeBypass indicates embedded assets are disabled in favour of a dev server.
	ErrDevModeBypass = errors.New("aero: dev mode active — embedded asset server bypassed")
)

// Asset is a byte blob served via the aero:// protocol.
type Asset struct {
	Data        []byte
	ContentType string
}

// AssetRequest mirrors an HTTP-like request sent over IPC from the shell.
type AssetRequest struct {
	Path   string
	Range  string
	Method string
}

// AssetResponse is returned to the shell for aero:// requests.
type AssetResponse struct {
	Status      int               `json:"status"`
	Headers     map[string]string `json:"headers"`
	Body        []byte            `json:"body"`
	ContentType string            `json:"contentType"`
}

// AssetStore holds embedded assets keyed by virtual path and serves them over IPC.
type AssetStore struct {
	mu        sync.RWMutex
	assets    map[string]Asset
	baseCSP   string
	devMode   bool
	devOrigin string
}

// NewAssetStore creates an empty asset registry with strict CSP defaults.
func NewAssetStore() *AssetStore {
	return &AssetStore{
		assets:    make(map[string]Asset),
		baseCSP:   strictCSP,
		devOrigin: DefaultDevOrigin,
	}
}

// SetCSP overrides the Content-Security-Policy injected on every response.
func (s *AssetStore) SetCSP(csp string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.baseCSP = csp
}

// SetDevMode enables Vite dev-server mode. Embedded assets are bypassed; IPC remains active.
func (s *AssetStore) SetDevMode(enabled bool, origin string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.devMode = enabled
	if origin != "" {
		s.devOrigin = strings.TrimRight(origin, "/")
	}
	if enabled {
		s.baseCSP = devCSP(s.devOrigin)
	} else {
		s.baseCSP = strictCSP
	}
}

// DevMode reports whether the store is in dev-server bypass mode.
func (s *AssetStore) DevMode() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.devMode
}

// DevOrigin returns the configured dev server origin.
func (s *AssetStore) DevOrigin() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.devOrigin
}

// EntryURL returns the webview entry URL (dev server or aero:// index).
func (s *AssetStore) EntryURL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.devMode {
		return s.devOrigin + "/"
	}
	return ProtocolURL("/index.html")
}

// Register adds or replaces an asset at virtual path p (e.g. "/index.html").
func (s *AssetStore) Register(p string, data []byte, contentType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.assets[normalizeAssetPath(p)] = Asset{
		Data:        data,
		ContentType: contentType,
	}
}

// LoadEmbedFS registers all files under root from an embedded FS (e.g. //go:embed assets/*).
func (s *AssetStore) LoadEmbedFS(fsys embed.FS, root string) error {
	root = strings.TrimPrefix(root, "./")
	return fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := fsys.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		virtual := strings.TrimPrefix(p, root)
		virtual = strings.TrimPrefix(virtual, "/")
		s.Register("/"+virtual, data, detectContentType(virtual))
		return nil
	})
}

// Handle serves an HTTP-like asset request over IPC (supports Range / 206).
func (s *AssetStore) Handle(req AssetRequest) (*AssetResponse, error) {
	if s.devMode {
		return s.serveDevBypass(req.Path)
	}
	return s.serveEmbedded(req.Path, req.Range)
}

// Serve resolves an aero:// path with optional HTTP Range header value.
func (s *AssetStore) Serve(assetPath, rangeHeader string) (*AssetResponse, error) {
	return s.Handle(AssetRequest{Path: assetPath, Range: rangeHeader, Method: http.MethodGet})
}

func (s *AssetStore) serveDevBypass(assetPath string) (*AssetResponse, error) {
	s.mu.RLock()
	origin := s.devOrigin
	s.mu.RUnlock()

	location := origin + "/"
	if assetPath != "" && assetPath != "/" {
		location = origin + normalizeAssetPath(assetPath)
	}

	return &AssetResponse{
		Status: http.StatusTemporaryRedirect,
		Headers: map[string]string{
			"Location":                     location,
			"X-Aero-Dev-Mode":                "1",
			"Access-Control-Allow-Origin":  origin,
			"Access-Control-Allow-Methods": "GET, HEAD, OPTIONS",
			"Access-Control-Allow-Headers": "Range, Content-Type",
			"Content-Security-Policy":        devCSP(origin),
		},
		Body:        nil,
		ContentType: "text/plain",
	}, nil
}

func (s *AssetStore) serveEmbedded(assetPath, rangeHeader string) (*AssetResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := normalizeAssetPath(assetPath)
	asset, ok := s.assets[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrAssetNotFound, key)
	}

	headers := baseHeaders(s.baseCSP, asset.ContentType)

	if rangeHeader == "" {
		headers["Content-Length"] = strconv.Itoa(len(asset.Data))
		return &AssetResponse{
			Status:      http.StatusOK,
			Headers:     headers,
			Body:        asset.Data,
			ContentType: asset.ContentType,
		}, nil
	}

	start, end, err := parseRange(rangeHeader, len(asset.Data))
	if err != nil {
		return &AssetResponse{
			Status:      http.StatusRequestedRangeNotSatisfiable,
			Headers:     headers,
			Body:        nil,
			ContentType: asset.ContentType,
		}, nil
	}

	chunk := asset.Data[start : end+1]
	headers["Content-Range"] = fmt.Sprintf("bytes %d-%d/%d", start, end, len(asset.Data))
	headers["Content-Length"] = strconv.Itoa(len(chunk))

	return &AssetResponse{
		Status:      http.StatusPartialContent,
		Headers:     headers,
		Body:        chunk,
		ContentType: asset.ContentType,
	}, nil
}

func baseHeaders(csp, contentType string) map[string]string {
	return map[string]string{
		"Content-Security-Policy":   csp,
		"X-Content-Type-Options":  "nosniff",
		"Accept-Ranges":             "bytes",
		"Cache-Control":             "no-cache",
		"Content-Type":              contentType,
	}
}

func devCSP(origin string) string {
	return fmt.Sprintf(
		"default-src 'self' aero: %s; script-src 'self' 'unsafe-inline' %s; connect-src 'self' aero: %s ws: wss:;",
		origin, origin, origin,
	)
}

func normalizeAssetPath(p string) string {
	return path.Clean("/" + strings.TrimPrefix(p, "/"))
}

// parseRange parses a single "bytes=start-end" Range header value.
func parseRange(rangeHeader string, size int) (start, end int, err error) {
	if !strings.HasPrefix(rangeHeader, "bytes=") {
		return 0, 0, fmt.Errorf("unsupported range unit")
	}
	spec := strings.TrimPrefix(rangeHeader, "bytes=")
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("malformed range")
	}

	if parts[0] == "" {
		suffix, err := strconv.Atoi(parts[1])
		if err != nil || suffix <= 0 {
			return 0, 0, fmt.Errorf("invalid suffix range")
		}
		if suffix > size {
			suffix = size
		}
		return size - suffix, size - 1, nil
	}

	start, err = strconv.Atoi(parts[0])
	if err != nil || start < 0 || start >= size {
		return 0, 0, fmt.Errorf("invalid range start")
	}

	if parts[1] == "" {
		return start, size - 1, nil
	}
	end, err = strconv.Atoi(parts[1])
	if err != nil || end < start || end >= size {
		return 0, 0, fmt.Errorf("invalid range end")
	}
	return start, end, nil
}

// EmbedAssets bulk-registers static files from a map.
func (s *AssetStore) EmbedAssets(files map[string][]byte, contentTypes map[string]string) {
	for p, data := range files {
		ct := contentTypes[p]
		if ct == "" {
			ct = detectContentType(p)
		}
		s.Register(p, data, ct)
	}
}

func detectContentType(p string) string {
	switch {
	case strings.HasSuffix(p, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(p, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(p, ".js"), strings.HasSuffix(p, ".mjs"):
		return "application/javascript; charset=utf-8"
	case strings.HasSuffix(p, ".json"):
		return "application/json; charset=utf-8"
	case strings.HasSuffix(p, ".png"):
		return "image/png"
	case strings.HasSuffix(p, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(p, ".woff2"):
		return "font/woff2"
	case strings.HasSuffix(p, ".mp4"):
		return "video/mp4"
	case strings.HasSuffix(p, ".webm"):
		return "video/webm"
	case strings.HasSuffix(p, ".mp3"):
		return "audio/mpeg"
	case strings.HasSuffix(p, ".wav"):
		return "audio/wav"
	default:
		return "application/octet-stream"
	}
}

// ProtocolURL builds an aero:// URL for the webview shell.
func ProtocolURL(assetPath string) string {
	clean := normalizeAssetPath(assetPath)
	var b bytes.Buffer
	b.WriteString("aero://assets")
	b.WriteString(clean)
	return b.String()
}

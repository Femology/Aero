// Package aero is the Go controller engine: lifecycle supervision, shell spawning,
// and the Death Pact that guarantees co-termination with the native UI shell.
package aero

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

const envHMACKey = "AERO_HMAC_KEY"

// Config holds runtime options for the Aero supervisor.
type Config struct {
	ShellPath string
	Assets    *AssetStore
	Width     int
	Height    int
	Title     string
	// DevMode loads the UI from DevOrigin (e.g. Vite) instead of embedded aero:// assets.
	DevMode bool
	// DevOrigin is the dev server base URL (default http://localhost:5173).
	DevOrigin string
}

// ResolveShellPath returns the default shell binary adjacent to the current executable.
func ResolveShellPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(exe)
	name := "aero-shell"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(dir, name), nil
}

func syscallSignalGraceful() os.Signal {
	if runtime.GOOS == "windows" {
		return os.Interrupt
	}
	return syscall.SIGTERM
}

// PipePair is a test helper exposing bidirectional pipe ends.
type PipePair struct {
	EngineRead  io.ReadCloser
	EngineWrite io.WriteCloser
	ShellRead   io.ReadCloser
	ShellWrite  io.WriteCloser
}

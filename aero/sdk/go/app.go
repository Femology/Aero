package aero

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
)

// CtxHandlerFunc handles a JSON-RPC request with context and validated params.
type CtxHandlerFunc func(ctx context.Context, params json.RawMessage) (any, error)

// App is the production Aero application SDK: supervisor, IPC router, and push API.
type App struct {
	cfg        Config
	key        []byte
	shell      *exec.Cmd
	ipc        *Multiplexer
	assets     *AssetStore
	pact       *deathPact
	mu         sync.RWMutex
	handlerCtx context.Context
	shutdown   context.CancelFunc
}

// NewApp constructs an application bound to cfg.
func NewApp(cfg Config) (*App, error) {
	if cfg.ShellPath == "" {
		return nil, fmt.Errorf("aero: ShellPath is required")
	}
	if cfg.Assets == nil {
		cfg.Assets = NewAssetStore()
	}
	if cfg.Width <= 0 {
		cfg.Width = 1024
	}
	if cfg.Height <= 0 {
		cfg.Height = 768
	}
	if cfg.Title == "" {
		cfg.Title = "Aero"
	}
	if cfg.DevMode {
		origin := cfg.DevOrigin
		if origin == "" {
			origin = DefaultDevOrigin
		}
		cfg.Assets.SetDevMode(true, origin)
		cfg.DevOrigin = origin
	}
	key, err := GenerateHMACKey()
	if err != nil {
		return nil, err
	}
	return &App{
		cfg:    cfg,
		key:    key,
		assets: cfg.Assets,
		pact:   newDeathPact(),
	}, nil
}

// NewRuntime is an alias for NewApp (backward compatibility).
func NewRuntime(cfg Config) (*App, error) { return NewApp(cfg) }

// Runtime is an alias for App (backward compatibility).
type Runtime = App

// Assets returns the embedded asset store served via aero://.
func (a *App) Assets() *AssetStore { return a.assets }

// Emit pushes an unsolicited event to the frontend via the IPC multiplexer.
// The shell dispatches this as window.dispatchEvent(new CustomEvent(...)).
func (a *App) Emit(eventName string, payload any) error {
	a.ensureMux()
	return a.ipc.Push(eventName, payload)
}

// Push is an alias for Emit (backward compatibility).
func (a *App) Push(event string, params any) error { return a.Emit(event, params) }

// Handle registers a context-aware handler with manual param validation.
func (a *App) Handle(method string, fn CtxHandlerFunc) {
	a.RegisterMethod(method, func(raw json.RawMessage) (any, error) {
		ctx := a.handlerContext()
		return fn(ctx, raw)
	})
}

// HandleJSON registers a typed handler with generic JSON schema validation before execution.
func HandleJSON[T any](a *App, method string, fn func(ctx context.Context, params T) (any, error)) {
	a.RegisterMethod(method, func(raw json.RawMessage) (any, error) {
		params, err := UnmarshalSchema[T](raw)
		if err != nil {
			return nil, err
		}
		return fn(a.handlerContext(), params)
	})
}

// RegisterMethod binds a low-level JSON-RPC handler (no context injection).
func (a *App) RegisterMethod(name string, fn HandlerFunc) {
	a.ensureMux()
	a.ipc.Register(name, fn)
}

// RegisterNotify binds a one-way notification handler from the shell.
func (a *App) RegisterNotify(name string, fn NotifyFunc) {
	a.ensureMux()
	a.ipc.RegisterNotify(name, fn)
}

// OnEvent registers a legacy push-channel producer.
func (a *App) OnEvent(name string, fn EventFunc) {
	a.ensureMux()
	a.ipc.OnEvent(name, fn)
}

func (a *App) ensureMux() {
	if a.ipc == nil {
		a.ipc = NewMultiplexer()
	}
}

func (a *App) handlerContext() context.Context {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.handlerCtx != nil {
		return a.handlerCtx
	}
	return context.Background()
}

func (a *App) setHandlerContext(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.handlerCtx = ctx
}

// Run launches the Rust shell under death-pact supervision and blocks until shutdown.
func (a *App) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.setHandlerContext(runCtx)

	shellR, shellW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("aero: shell pipe: %w", err)
	}
	engineR, engineW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("aero: engine pipe: %w", err)
	}

	cmd := exec.CommandContext(runCtx, a.cfg.ShellPath)
	entryURL := a.cfg.Assets.EntryURL()
	env := append(os.Environ(),
		envHMACKey+"="+hex.EncodeToString(a.key),
		fmt.Sprintf("AERO_WIDTH=%d", a.cfg.Width),
		fmt.Sprintf("AERO_HEIGHT=%d", a.cfg.Height),
		fmt.Sprintf("AERO_TITLE=%s", a.cfg.Title),
		"AERO_ENTRY_URL="+entryURL,
	)
	if a.cfg.DevMode {
		env = append(env, "AERO_DEV_MODE=1", "AERO_DEV_ORIGIN="+a.cfg.DevOrigin)
	}
	cmd.Env = env
	cmd.Stdin = engineR
	cmd.Stdout = shellW
	cmd.Stderr = os.Stderr

	if err := a.pact.apply(cmd); err != nil {
		return fmt.Errorf("aero: death pact: %w", err)
	}

	a.ensureMux()
	a.ipc.AttachAssets(a.assets)
	a.ipc.RegisterNotify("aero.shutdown", func(_ json.RawMessage) error {
		a.mu.Lock()
		fn := a.shutdown
		a.mu.Unlock()
		if fn != nil {
			fn()
		}
		return nil
	})

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("aero: start shell: %w", err)
	}

	if err := a.pact.assign(cmd); err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("aero: assign death pact: %w", err)
	}

	a.mu.Lock()
	a.shell = cmd
	a.shutdown = cancel
	a.mu.Unlock()

	_ = shellW.Close()
	_ = engineR.Close()

	conn := WrapIO(shellR, engineW)
	if err := ServerHandshake(conn, a.key); err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("aero: handshake: %w", err)
	}

	go a.ipc.Serve(runCtx, conn)

	errCh := make(chan error, 1)
	go func() {
		errCh <- cmd.Wait()
	}()

	select {
	case <-runCtx.Done():
		_ = a.Shutdown()
		a.pact.close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	case err := <-errCh:
		a.pact.close()
		if err != nil {
			return fmt.Errorf("aero: shell exited: %w", err)
		}
		return nil
	}
}

// Shutdown sends a graceful termination signal to the supervised shell.
func (a *App) Shutdown() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.shell == nil || a.shell.Process == nil {
		return nil
	}
	return a.shell.Process.Signal(syscallSignalGraceful())
}

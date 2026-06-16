// Aero demo — end-to-end Go controller + Rust shell + embedded aero:// assets.
package main

import (
	"context"
	"embed"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	aero "github.com/femology/aero/sdk/go"
)

//go:embed assets/*
var ui embed.FS

func main() {
	store := aero.NewAssetStore()
	if err := store.LoadEmbedFS(ui, "assets"); err != nil {
		log.Fatalf("load assets: %v", err)
	}

	shellPath := os.Getenv("AERO_SHELL")
	if shellPath == "" {
		candidates := []string{
			filepath.Join("dist", "aero-shell"),
			filepath.Join("..", "..", "dist", "aero-shell"),
			"aero-shell",
		}
		if resolved, err := aero.ResolveShellPath(); err == nil {
			candidates = append([]string{resolved}, candidates...)
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				shellPath = c
				break
			}
		}
		if shellPath == "" {
			shellPath = filepath.Join("dist", "aero-shell")
		}
	}

	app, err := aero.NewApp(aero.Config{
		ShellPath: shellPath,
		Assets:    store,
		Width:     960,
		Height:    640,
		Title:     "Aero Demo",
	})
	if err != nil {
		log.Fatalf("app: %v", err)
	}

	aero.HandleJSON(app, "demo.ping", func(ctx context.Context, req struct {
		TS int64 `json:"ts"`
	}) (any, error) {
		return map[string]any{
			"pong": true,
			"ts":   req.TS,
			"now":  time.Now().Unix(),
		}, nil
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case t := <-ticker.C:
				if err := app.Emit("aero.tick", map[string]any{"unix": t.Unix()}); err != nil {
					return
				}
			}
		}
	}()

	log.Printf("starting Aero demo (shell=%s)", shellPath)
	if err := app.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("run: %v", err)
	}
	log.Println("Aero demo exited cleanly")
}

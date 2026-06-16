package aero

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type pingParams struct {
	TS int64 `json:"ts"`
}

func (p pingParams) Validate() error {
	if p.TS <= 0 {
		return errors.New("ts must be positive")
	}
	return nil
}

func TestUnmarshalSchema(t *testing.T) {
	raw := json.RawMessage(`{"ts":42}`)
	got, err := UnmarshalSchema[pingParams](raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.TS != 42 {
		t.Fatalf("ts: got %d want 42", got.TS)
	}
}

func TestUnmarshalSchemaInvalidJSON(t *testing.T) {
	_, err := UnmarshalSchema[pingParams]([]byte(`{bad`))
	if !errors.Is(err, ErrSchemaValidation) {
		t.Fatalf("expected ErrSchemaValidation, got %v", err)
	}
}

func TestUnmarshalSchemaValidator(t *testing.T) {
	_, err := UnmarshalSchema[pingParams]([]byte(`{"ts":0}`))
	if !errors.Is(err, ErrSchemaValidation) {
		t.Fatalf("expected validation failure, got %v", err)
	}
}

func TestHandleJSONIntegration(t *testing.T) {
	app, err := NewApp(Config{ShellPath: "/bin/true"})
	if err != nil {
		t.Fatal(err)
	}
	HandleJSON(app, "demo.ping", func(ctx context.Context, p pingParams) (any, error) {
		if ctx == nil {
			t.Fatal("nil context")
		}
		return map[string]any{"ts": p.TS}, nil
	})

	// Simulate IPC dispatch by re-invoking the registered handler closure.
	app.ensureMux()
	fn, ok := app.ipc.handlers["demo.ping"]
	if !ok {
		t.Fatal("handler not registered")
	}
	app.setHandlerContext(context.Background())

	result, err := fn([]byte(`{"ts":99}`))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := result.(map[string]any)
	if !ok || m["ts"].(int64) != 99 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestAppEmitNotConnected(t *testing.T) {
	app, err := NewApp(Config{ShellPath: "/bin/true"})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Emit("aero.tick", nil); err == nil {
		t.Fatal("expected error when IPC not connected")
	}
}

func TestNewRuntimeAlias(t *testing.T) {
	rt, err := NewRuntime(Config{ShellPath: "/bin/true"})
	if err != nil {
		t.Fatal(err)
	}
	if rt == nil {
		t.Fatal("nil runtime")
	}
}

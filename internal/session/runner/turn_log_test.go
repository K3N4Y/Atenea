package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"atenea/internal/llm"
	"atenea/internal/session"
	"atenea/internal/tool"
)

// failingTool falla siempre en Execute. Sirve para ejercitar el camino de fallo
// de una tool local sin depender de cancelacion ni de un store que rechace.
type failingTool struct{}

func (failingTool) Name() string            { return "failing" }
func (failingTool) Description() string     { return "Falla siempre." }
func (failingTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (failingTool) Execute(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.Result{}, errors.New("boom de la tool")
}

// TestRunner_LogsToolFailureForDev afirma que cuando una tool local falla al
// asentar, el runner escribe ademas una linea de log (visible en `wails dev`) con
// el nombre de la tool y la causa. Sin esa linea el fallo solo vive en el log
// durable y en el mensaje al modelo: el dev no tiene como enterarse de que las
// tools fallan. El test inyecta logf para capturar la salida sin tocar stderr.
func TestRunner_LogsToolFailureForDev(t *testing.T) {
	ctx := context.Background()
	store := newRecordingStore()
	seedUser(t, store, "s1")

	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.ToolCall, CallID: "c1", ToolName: "failing", Input: json.RawMessage(`{}`)},
		llm.Event{Kind: llm.StepEnded},
	)
	reg := tool.NewRegistry(tool.NewOutputStore(0), failingTool{})
	r := NewRunner(store, session.NewMemoryInbox(), fake, reg, tool.Permissions{"failing": true}, func() string { return "a1" })

	var buf strings.Builder
	r.logf = func(format string, args ...any) { fmt.Fprintf(&buf, format, args...) }

	if _, err := r.runTurn(ctx, "s1"); err != nil {
		t.Fatalf("runTurn error inesperado: %v", err)
	}

	line := buf.String()
	if line == "" {
		t.Fatalf("no se logueo el fallo de la tool; el dev no tiene como enterarse")
	}
	if !strings.Contains(line, "failing") {
		t.Errorf("log = %q, quiero que nombre la tool 'failing'", line)
	}
	if !strings.Contains(line, "boom de la tool") {
		t.Errorf("log = %q, quiero que incluya la causa 'boom de la tool'", line)
	}
}

// TestRunner_LogsDeniedToolForDev triangula con el otro camino de fallo de
// settle: una tool no permitida devuelve UnknownToolError ANTES de ejecutar, y
// tambien debe quedar logueada (el dev ve que el modelo pidio algo fuera del set).
func TestRunner_LogsDeniedToolForDev(t *testing.T) {
	ctx := context.Background()
	store := newRecordingStore()
	seedUser(t, store, "s1")

	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.ToolCall, CallID: "c1", ToolName: "echo", Input: json.RawMessage(`{"text":"x"}`)},
		llm.Event{Kind: llm.StepEnded},
	)
	// echo esta en el registry pero NO en los permisos: settle la deniega.
	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	r := NewRunner(store, session.NewMemoryInbox(), fake, reg, tool.Permissions{}, func() string { return "a1" })

	var buf strings.Builder
	r.logf = func(format string, args ...any) { fmt.Fprintf(&buf, format, args...) }

	if _, err := r.runTurn(ctx, "s1"); err != nil {
		t.Fatalf("runTurn error inesperado: %v", err)
	}

	line := buf.String()
	if !strings.Contains(line, "echo") {
		t.Errorf("log = %q, quiero que nombre la tool 'echo'", line)
	}
	if !strings.Contains(line, "desconocida o no permitida") {
		t.Errorf("log = %q, quiero que incluya la causa de denegacion", line)
	}
}

// TestRunner_DoesNotLogSuccessfulTool es la otra cara: una tool que asienta bien
// NO debe loguear nada. Evita un falso verde donde se loguea siempre (el dev se
// inundaria de ruido y el log perderia su senal).
func TestRunner_DoesNotLogSuccessfulTool(t *testing.T) {
	ctx := context.Background()
	store := newRecordingStore()
	seedUser(t, store, "s1")

	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.ToolCall, CallID: "c1", ToolName: "echo", Input: json.RawMessage(`{"text":"pong"}`)},
		llm.Event{Kind: llm.StepEnded},
	)
	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	r := NewRunner(store, session.NewMemoryInbox(), fake, reg, tool.Permissions{"echo": true}, func() string { return "a1" })

	var buf strings.Builder
	r.logf = func(format string, args ...any) { fmt.Fprintf(&buf, format, args...) }

	if _, err := r.runTurn(ctx, "s1"); err != nil {
		t.Fatalf("runTurn error inesperado: %v", err)
	}

	if line := buf.String(); line != "" {
		t.Errorf("se logueo en el camino feliz: %q, quiero nada", line)
	}
}

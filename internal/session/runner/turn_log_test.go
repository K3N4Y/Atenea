package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool"
)

// failingTool always fails Execute, exercising local-tool failure without
// depending on cancellation or a store failure.
type failingTool struct{}

func (failingTool) Name() string            { return "failing" }
func (failingTool) Description() string     { return "Always fails." }
func (failingTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (failingTool) Execute(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.Result{}, errors.New("tool exploded")
}

// TestRunner_LogsToolFailureForDev verifies that a failed local tool produces a
// development log line containing the tool name and cause.
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
		t.Fatalf("runTurn returned an unexpected error: %v", err)
	}

	line := buf.String()
	if line == "" {
		t.Fatalf("tool failure was not logged")
	}
	if !strings.Contains(line, "failing") {
		t.Errorf("log = %q, want tool name 'failing'", line)
	}
	if !strings.Contains(line, "tool exploded") {
		t.Errorf("log = %q, want cause 'tool exploded'", line)
	}
}

// TestRunner_LogsDeniedToolForDev verifies that an unallowed call is logged as
// an UnknownToolError before execution.
func TestRunner_LogsDeniedToolForDev(t *testing.T) {
	ctx := context.Background()
	store := newRecordingStore()
	seedUser(t, store, "s1")

	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.ToolCall, CallID: "c1", ToolName: "echo", Input: json.RawMessage(`{"text":"x"}`)},
		llm.Event{Kind: llm.StepEnded},
	)
	// Echo is registered but excluded from the materialized permissions.
	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	r := NewRunner(store, session.NewMemoryInbox(), fake, reg, tool.Permissions{}, func() string { return "a1" })

	var buf strings.Builder
	r.logf = func(format string, args ...any) { fmt.Fprintf(&buf, format, args...) }

	if _, err := r.runTurn(ctx, "s1"); err != nil {
		t.Fatalf("runTurn returned an unexpected error: %v", err)
	}

	line := buf.String()
	if !strings.Contains(line, "echo") {
		t.Errorf("log = %q, want tool name 'echo'", line)
	}
	if !strings.Contains(line, "unknown or not allowed") {
		t.Errorf("log = %q, want denial cause", line)
	}
}

// TestRunner_DoesNotLogSuccessfulTool verifies that successful calls produce no
// failure log noise.
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
		t.Fatalf("runTurn returned an unexpected error: %v", err)
	}

	if line := buf.String(); line != "" {
		t.Errorf("successful path logged %q, want no output", line)
	}
}

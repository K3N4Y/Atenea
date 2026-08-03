package tool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDebugAdapterProcess(t *testing.T) {
	if os.Getenv("ATENEA_FAKE_DAP") != "1" {
		return
	}
	r := bufio.NewReader(os.Stdin)
	seq := 0
	send := func(v map[string]any) { seq++; v["seq"] = seq; _ = writeDebugMessage(os.Stdout, v) }
	for {
		body, err := readDebugMessage(r)
		if err != nil {
			return
		}
		var req struct {
			Seq       int             `json:"seq"`
			Command   string          `json:"command"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(body, &req) != nil {
			os.Exit(2)
		}
		if req.Command == "hang" {
			select {}
		}
		responseBody := any(map[string]any{})
		switch req.Command {
		case "launch", "attach":
			send(map[string]any{"type": "event", "event": "initialized", "body": map[string]any{}})
		case "configurationDone":
			send(map[string]any{"type": "event", "event": "output", "body": map[string]any{"category": "console", "output": "adapter ready\n"}})
			send(map[string]any{"type": "event", "event": "stopped", "body": map[string]any{"reason": "breakpoint", "threadId": 7}})
		case "threads":
			responseBody = map[string]any{"threads": []any{map[string]any{"id": 7, "name": "main"}}}
		case "stackTrace":
			responseBody = map[string]any{"stackFrames": []any{map[string]any{"id": 42, "name": "main", "line": 3, "column": 1}}}
		case "scopes":
			responseBody = map[string]any{"scopes": []any{map[string]any{"name": "Locals", "variablesReference": 9}}}
		case "variables":
			responseBody = map[string]any{"variables": []any{map[string]any{"name": "x", "value": "1", "variablesReference": 0}}}
		case "evaluate":
			responseBody = map[string]any{"result": "2", "variablesReference": 0}
		case "disconnect":
			send(map[string]any{"type": "response", "request_seq": req.Seq, "command": req.Command, "success": true, "body": responseBody})
			return
		}
		send(map[string]any{"type": "response", "request_seq": req.Seq, "command": req.Command, "success": true, "body": responseBody})
	}
}

func debugCall(t *testing.T, d *DebugTool, in map[string]any) string {
	t.Helper()
	raw, _ := json.Marshal(in)
	res, err := d.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	return res.Output
}
func newFakeDebug(t *testing.T) (*DebugTool, string) {
	t.Helper()
	root := t.TempDir()
	program := filepath.Join(root, "main.go")
	if err := os.WriteFile(program, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := NewDebugTool(root)
	t.Cleanup(func() { _ = d.Close() })
	return d, program
}
func launchFake(t *testing.T, d *DebugTool, program string) string {
	return debugCall(t, d, map[string]any{"operation": "launch", "adapter_command": os.Args[0], "adapter_args": []string{"-test.run=TestDebugAdapterProcess"}, "program": program, "timeout_seconds": 2})
}

func TestDebugToolLaunchInspectControlAndTerminate(t *testing.T) {
	t.Setenv("ATENEA_FAKE_DAP", "1")
	d, program := newFakeDebug(t)
	if got := launchFake(t, d, program); !strings.Contains(got, "session started") {
		t.Fatal(got)
	}
	if got := debugCall(t, d, map[string]any{"operation": "sessions"}); !strings.Contains(got, "stopped on thread 7") {
		t.Fatal(got)
	}
	if got := debugCall(t, d, map[string]any{"operation": "threads"}); !strings.Contains(got, "main") {
		t.Fatal(got)
	}
	if got := debugCall(t, d, map[string]any{"operation": "stack_trace"}); !strings.Contains(got, "42") {
		t.Fatal(got)
	}
	if got := debugCall(t, d, map[string]any{"operation": "scopes"}); !strings.Contains(got, "Locals") {
		t.Fatal(got)
	}
	if got := debugCall(t, d, map[string]any{"operation": "variables", "variables_reference": 9}); !strings.Contains(got, "\"x\"") {
		t.Fatal(got)
	}
	if got := debugCall(t, d, map[string]any{"operation": "evaluate", "expression": "1+1"}); !strings.Contains(got, "\"2\"") {
		t.Fatal(got)
	}
	debugCall(t, d, map[string]any{"operation": "set_breakpoint", "file": "main.go", "line": 1, "condition": "x > 0"})
	debugCall(t, d, map[string]any{"operation": "remove_breakpoint", "file": "main.go", "line": 1})
	for _, op := range []string{"continue", "next", "step_in", "step_out", "pause"} {
		if got := debugCall(t, d, map[string]any{"operation": op}); !strings.Contains(got, "thread 7") {
			t.Fatalf("%s: %s", op, got)
		}
	}
	if got := debugCall(t, d, map[string]any{"operation": "output"}); !strings.Contains(got, "adapter ready") || !strings.Contains(got, "stopped (breakpoint)") {
		t.Fatal(got)
	}
	if got := debugCall(t, d, map[string]any{"operation": "terminate"}); !strings.Contains(got, "reaped") {
		t.Fatal(got)
	}
	if d.session != nil {
		t.Fatal("session retained")
	}
}

func TestDebugToolEffectsTraversalAndValidation(t *testing.T) {
	d := NewDebugTool(t.TempDir())
	read := Call{Input: json.RawMessage(`{"operation":"threads"}`)}
	if d.CallEffects(read) != NoEffects {
		t.Fatal("threads must be passive")
	}
	eval := Call{Input: json.RawMessage(`{"operation":"evaluate","expression":"x"}`)}
	if d.CallEffects(eval) != RunsCommands {
		t.Fatal("evaluate must run commands")
	}
	outside := filepath.Join(t.TempDir(), "main.go")
	// A nonexistent adapter keeps this from spawning anything if the
	// traversal check ever regresses; the error asserted below fires first.
	adapter := filepath.Join(t.TempDir(), "missing-adapter")
	_, err := d.Execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"operation":"launch","adapter_command":%q,"program":%q}`, adapter, outside)))
	if err == nil || !strings.Contains(err.Error(), "fuera del workspace") {
		t.Fatalf("traversal error = %v", err)
	}
}

func TestDebugFramingAndCancellation(t *testing.T) {
	var b strings.Builder
	if err := writeDebugMessage(&b, map[string]any{"type": "event", "event": "ok"}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(b.String(), "Content-Length: ") {
		t.Fatal(b.String())
	}
	body, err := readDebugMessage(bufio.NewReader(strings.NewReader(b.String())))
	if err != nil || !strings.Contains(string(body), "ok") {
		t.Fatalf("body=%s err=%v", body, err)
	}
	s := &debugSession{in: blockWriter{}, pending: map[int]chan debugResponse{}, done: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err = s.request(ctx, "hang", map[string]any{}, nil)
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("error = %v", err)
	}
}

type blockWriter struct{}

func (blockWriter) Write(p []byte) (int, error) { return len(p), nil }
func (blockWriter) Close() error                { return nil }

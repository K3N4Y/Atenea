package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/session"
)

// TestApp_ReadToolCallStreamsSuccess verifies the built-in read wiring: with the
// working directory as root, a read call for a real file travels through the bus
// as Tool.Success and its output contains the [file#HASH] hashline header. The
// read behavior itself is covered by unit tests; this test covers app wiring.
func TestApp_ReadToolCallStreamsSuccess(t *testing.T) {
	dir := t.TempDir()
	const name = "greeting.txt"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	rec := &recordingEmit{}
	provider := &scriptedProvider{turns: [][]llm.Event{
		{
			{Kind: llm.StepStarted},
			{Kind: llm.ToolCall, CallID: "c1", ToolName: "read", Input: json.RawMessage(`{"path":"` + name + `"}`)},
			{Kind: llm.StepEnded},
		},
		{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: "done"},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		},
	}}
	app := newApp(t, provider, rec.emit)

	if err := app.SendPrompt("s1", "read the file"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	header := "[" + name + "#"
	found := false
	for _, ev := range rec.eventsOn("session:s1") {
		if ev.Kind == session.KindToolSuccess && ev.CallID == "c1" && strings.Contains(ev.Text, header) {
			found = true
		}
	}
	if !found {
		t.Errorf("no Tool.Success for c1 contained header %q", header)
	}
	provider.mu.Lock()
	calls := provider.calls
	provider.mu.Unlock()
	if calls != 2 {
		t.Errorf("provider turns = %d, want 2: the terminal response must finish the activity", calls)
	}
}

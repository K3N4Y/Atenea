package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"atenea/internal/llm"
	"atenea/internal/session"
)

// TestApp_ReadToolCallStreamsSuccess verifica el cableado del builtin read en la
// app: con root = cwd, una tool call a read sobre un archivo real viaja por el bus
// como Tool.Success cuyo output trae el header hashline [archivo#HASH]. Cubre solo
// el cableado (el comportamiento del read ya esta en sus unit tests); por eso
// chdir al TempDir para que root (os.Getwd en newAppWithStore) apunte al archivo.
func TestApp_ReadToolCallStreamsSuccess(t *testing.T) {
	dir := t.TempDir()
	const name = "saludo.txt"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("hola\nmundo\n"), 0o644); err != nil {
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
	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.ToolCall, CallID: "c1", ToolName: "read", Input: json.RawMessage(`{"path":"` + name + `"}`)},
		llm.Event{Kind: llm.StepEnded},
	)
	app := newApp(fake, rec.emit)

	if err := app.SendPrompt("s1", "lee el archivo"); err != nil {
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
		t.Errorf("no llego un Tool.Success de c1 con el header %q en el output", header)
	}
}

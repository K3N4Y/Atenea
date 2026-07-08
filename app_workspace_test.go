package main

import (
	"strings"
	"sync"
	"testing"

	"atenea/internal/llm"
	"atenea/internal/session"
)

// summaryFor busca el resumen de sessionID en ListSessions; falla si no esta.
func summaryFor(t *testing.T, app *App, sessionID string) session.SessionSummary {
	t.Helper()
	got, err := app.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	for _, s := range got {
		if s.ID == sessionID {
			return s
		}
	}
	t.Fatalf("ListSessions no incluye %q: %+v", sessionID, got)
	return session.SessionSummary{}
}

func workspaceFake() *llm.FakeProvider {
	return llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "ok"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)
}

// TestApp_CapturesWorkspaceCwdOnNewSession: al crear una sesion (primer prompt),
// la app graba la carpeta de trabajo vigente como Session.Cwd, y ListSessions la
// expone en SessionSummary.Cwd para que la sidebar agrupe por carpeta.
func TestApp_CapturesWorkspaceCwdOnNewSession(t *testing.T) {
	rec := &recordingEmit{}
	app := newApp(workspaceFake(), rec.emit)
	root := app.Workspace()

	if err := app.SendPrompt("s1", "hola"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	if got := summaryFor(t, app, "s1").Cwd; got != root {
		t.Fatalf("Cwd de s1 = %q, want %q (el root vigente)", got, root)
	}
}

// TestApp_SetWorkspaceChangesCwdForNewSessions: SetWorkspace cambia la carpeta de
// trabajo vigente (Workspace la refleja) y las sesiones nuevas la capturan.
func TestApp_SetWorkspaceChangesCwdForNewSessions(t *testing.T) {
	rec := &recordingEmit{}
	app := newApp(workspaceFake(), rec.emit)
	dir := t.TempDir()

	if err := app.SetWorkspace(dir); err != nil {
		t.Fatalf("SetWorkspace(%q): %v", dir, err)
	}
	if got := app.Workspace(); got != dir {
		t.Fatalf("Workspace() = %q, want %q", got, dir)
	}

	if err := app.SendPrompt("s1", "hola"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	if got := summaryFor(t, app, "s1").Cwd; got != dir {
		t.Fatalf("Cwd de s1 = %q, want %q (la carpeta nueva)", got, dir)
	}
}

// TestApp_SetWorkspaceRejectsNonDir: SetWorkspace con una ruta inexistente (o que
// no es carpeta) falla y no cambia la carpeta vigente.
func TestApp_SetWorkspaceRejectsNonDir(t *testing.T) {
	rec := &recordingEmit{}
	app := newApp(workspaceFake(), rec.emit)
	before := app.Workspace()

	if err := app.SetWorkspace(before + "/no-existe-xyz"); err == nil {
		t.Fatal("SetWorkspace con ruta inexistente: se esperaba error")
	}
	if got := app.Workspace(); got != before {
		t.Fatalf("Workspace cambio tras error: got %q, want %q", got, before)
	}
}

// TestApp_SetWorkspaceRepointsSystemPrompt: SetWorkspace reconstruye el wiring
// anclado al root, asi el system prompt del siguiente turno lleva la carpeta
// nueva en su bloque <env> (Working directory).
func TestApp_SetWorkspaceRepointsSystemPrompt(t *testing.T) {
	rec := &recordingEmit{}
	prov := &requestRecordingProvider{FakeProvider: workspaceFake()}
	app := newApp(prov, rec.emit)
	dir := t.TempDir()

	if err := app.SetWorkspace(dir); err != nil {
		t.Fatalf("SetWorkspace(%q): %v", dir, err)
	}
	if err := app.SendPrompt("s1", "hola"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	if sys := prov.captured().System; !strings.Contains(sys, dir) {
		t.Fatalf("el system prompt no lleva la carpeta nueva %q:\n%s", dir, sys)
	}
}

// TestApp_Race_ConcurrentSetWorkspaceAndSendPrompt: SetWorkspace y SendPrompt
// llamados concurrentemente no tienen data races ni dejan el runner en estado
// inconsistente. Se repite 20 veces con -race para forzar interleavings. No
// verifica que workspace gana (es indeterminista), solo que no hay carreras.
func TestApp_Race_ConcurrentSetWorkspaceAndSendPrompt(t *testing.T) {
	for i := 0; i < 20; i++ {
		rec := &recordingEmit{}
		prov := workspaceFake()
		app := newApp(prov, rec.emit)
		dir := t.TempDir()

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = app.SetWorkspace(dir)
		}()
		go func() {
			defer wg.Done()
			_ = app.SendPrompt("s1", "hola")
		}()
		wg.Wait()
		app.wait()
	}
}

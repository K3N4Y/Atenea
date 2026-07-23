package main

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"atenea/internal/session"
)

// sawChannel informa si el emit fake registro alguna emision en channel.
// Concurrency-safe: el watcher emite desde su goroutine mientras el test mira.
func (r *recordingEmit) sawChannel(channel string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Contains(r.channels, channel)
}

// TestApp_EmitsSessionsChangedOnExternalDBWrite es el wiring end-to-end del
// refresco en vivo de la sidebar: la app abre un SQLiteStore real sobre archivo,
// startup lanza el watcher del data_version, y cuando OTRO proceso (aqui un
// segundo NewSQLiteStore sobre el mismo archivo, como la TUI) escribe la base,
// la app emite "sessions:changed" para que el frontend re-pida ListSessions.
// Correr con -race.
func TestApp_EmitsSessionsChangedOnExternalDBWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	path := filepath.Join(t.TempDir(), "atenea.db")
	store, err := session.NewSQLiteStore(path)

	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}

	t.Cleanup(func() { store.Close() })

	rec := &recordingEmit{}
	a := newAppWithStore(store, demoProvider(), rec.emit)
	a.sessions.SetWatchPeriod(10 * time.Millisecond) // acelera el polling en el test
	a.startup(ctx)

	// El "otro proceso": un segundo store (otro pool) sobre el mismo archivo.
	other, err := session.NewSQLiteStore(path)

	if err != nil {
		t.Fatalf("NewSQLiteStore (otro proceso): %v", err)
	}

	t.Cleanup(func() { other.Close() })

	if _, err := other.AppendEvent(context.Background(), "sesion-tui",
		session.SessionEvent{Kind: session.KindStepStarted}); err != nil {
		t.Fatalf("AppendEvent (otro proceso): %v", err)
	}

	// Margen holgado: bajo la suite completa (los tests PTY lanzan binarios
	// TUI reales en paralelo) el watcher puede quedar hambriento de CPU y 2s
	// no alcanzan; el caso feliz retorna apenas ve la emision, sin esperar.
	waitFor(t, 2*time.Second, func() bool {
		return rec.sawChannel("sessions:changed")
	}, "the app did not emit sessions:changed after the external write to the DB")
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatal(msg)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

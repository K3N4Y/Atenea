package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"atenea/internal/session"
)

// sawChannel informa si el emit fake registro alguna emision en channel.
// Concurrency-safe: el watcher emite desde su goroutine mientras el test mira.
func (r *recordingEmit) sawChannel(channel string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ch := range r.channels {
		if ch == channel {
			return true
		}
	}
	return false
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
	a.watchInterval = 10 * time.Millisecond // acelera el polling en el test
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

	deadline := time.After(2 * time.Second)
	for {
		if rec.sawChannel("sessions:changed") {
			return
		}
		select {
		case <-deadline:
			t.Fatal("la app no emitio sessions:changed tras la escritura externa a la DB")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

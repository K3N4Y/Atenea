package main

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/K3N4Y/atenea/internal/session"
)

// sawChannel reports whether the fake emitter recorded the channel. It is safe
// while the watcher emits concurrently with the test.
func (r *recordingEmit) sawChannel(channel string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Contains(r.channels, channel)
}

// This is the end-to-end wiring for live sidebar refresh: the app opens a real
// file-backed SQLite store, startup starts the data_version watcher, and a
// second store writes as another process such as the TUI would. The app must
// emit "sessions:changed" so the frontend requests ListSessions again.
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
	a := newAppWithStore(store, inertProviderService(t, demoProvider()), rec.emit)
	a.sessions.SetWatchPeriod(10 * time.Millisecond) // Speed up polling in this test.
	a.startup(ctx)

	// Use a second connection pool to represent the other process.
	other, err := session.NewSQLiteStore(path)

	if err != nil {
		t.Fatalf("NewSQLiteStore (other process): %v", err)
	}

	t.Cleanup(func() { other.Close() })

	if _, err := other.AppendEvent(context.Background(), "sesion-tui",
		session.SessionEvent{Kind: session.KindStepStarted}); err != nil {
		t.Fatalf("AppendEvent (other process): %v", err)
	}

	// The successful path returns as soon as the watcher emits.
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

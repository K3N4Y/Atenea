package session

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/K3N4Y/atenea/internal/paths"
)

// These tests define the shared opening contract used by the TUI and Wails app.

func TestDefaultDBPath_UsesEnvOverride(t *testing.T) {
	// ATENEA_DB always takes precedence and is returned unchanged.
	want := filepath.Join(t.TempDir(), "custom.db")
	t.Setenv(paths.DatabaseEnv, want)

	if got := DefaultDBPath(); got != want {
		t.Fatalf("DefaultDBPath() = %q, want ATENEA_DB unchanged: %q", got, want)
	}
}

func TestDefaultDBPath_DefaultsToXDGDataHome(t *testing.T) {
	// The default database belongs under the durable XDG data root.
	xdg := t.TempDir()
	t.Setenv(paths.DatabaseEnv, "")
	t.Setenv("XDG_DATA_HOME", xdg)

	want := filepath.Join(xdg, "atenea", "atenea.db")
	if got := DefaultDBPath(); got != want {
		t.Fatalf("DefaultDBPath() = %q, want %q", got, want)
	}
	appDir := filepath.Join(xdg, "atenea")
	info, err := os.Stat(appDir)
	if err != nil {
		t.Fatalf("os.Stat(%q) = %v: DefaultDBPath must create the data directory", appDir, err)
	}
	if !info.IsDir() {
		t.Fatalf("%q exists but is not a directory", appDir)
	}
}

func TestDefaultCheckpointPath_UsesEnvOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "custom-checkpoints")
	t.Setenv(paths.CheckpointsEnv, want)
	if got := DefaultCheckpointPath(); got != want {
		t.Fatalf("DefaultCheckpointPath() = %q, want %q", got, want)
	}
}

func TestDefaultCheckpointPath_DefaultsToXDGDataHome(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv(paths.CheckpointsEnv, "")
	t.Setenv("XDG_DATA_HOME", xdg)
	want := filepath.Join(xdg, "atenea", "checkpoints")
	if got := DefaultCheckpointPath(); got != want {
		t.Fatalf("DefaultCheckpointPath() = %q, want %q", got, want)
	}
	info, err := os.Stat(want)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %o, want 700", info.Mode().Perm())
	}
}

func TestOpenDefault_OpensSQLiteAtDefaultPath(t *testing.T) {
	// OpenDefault returns a usable durable store and creates the database file.
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "atenea.db")
	t.Setenv("ATENEA_DB", path)

	store, err := OpenDefault()
	if err != nil {
		t.Fatalf("OpenDefault() error = %v, want nil for a writable path", err)
	}
	if store == nil {
		t.Fatal("OpenDefault() returned a nil store")
	}
	if c, ok := store.(io.Closer); ok {
		t.Cleanup(func() { c.Close() })
	}

	if _, err := store.AppendEvent(ctx, "s1", SessionEvent{Message: &Message{ID: "m1", Role: RoleUser, Text: "hola"}}); err != nil {
		t.Fatalf("AppendEvent = %v", err)
	}
	sums, err := store.Sessions(ctx)
	if err != nil {
		t.Fatalf("Sessions = %v", err)
	}
	if len(sums) != 1 || sums[0].ID != "s1" {
		t.Fatalf("Sessions() = %v, want the newly written s1 session", sums)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("os.Stat(%q) = %v: OpenDefault must create the database", path, err)
	}
}

func TestOpenDefault_TwoInstancesShareSessions(t *testing.T) {
	// Two host instances using the same database observe each other's writes.
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "atenea.db")
	t.Setenv("ATENEA_DB", path)

	open := func(name string) Store {
		t.Helper()
		store, err := OpenDefault()
		if err != nil {
			t.Fatalf("OpenDefault (%s) error = %v", name, err)
		}
		c, ok := store.(io.Closer)
		if !ok {
			t.Fatalf("OpenDefault (%s) returned non-closable %T", name, store)
		}
		t.Cleanup(func() { c.Close() })
		return store
	}
	app := open("app")
	tui := open("tui")

	seq1, err := app.AppendEvent(ctx, "compartida", SessionEvent{Message: &Message{ID: "m1", Role: RoleUser, Text: "desde la app"}})
	if err != nil {
		t.Fatalf("AppendEvent (app) = %v", err)
	}

	sums, err := tui.Sessions(ctx)
	if err != nil {
		t.Fatalf("Sessions (tui) = %v", err)
	}
	if len(sums) != 1 || sums[0].ID != "compartida" {
		t.Fatalf("Sessions (tui) = %+v, want the session written by the first instance", sums)
	}

	seq2, err := tui.AppendEvent(ctx, "compartida", SessionEvent{Message: &Message{ID: "m2", Role: RoleUser, Text: "desde la tui"}})
	if err != nil {
		t.Fatalf("AppendEvent (tui) = %v", err)
	}
	if seq2 != seq1+1 {
		t.Fatalf("AppendEvent (tui) Seq = %d, want %d", seq2, seq1+1)
	}

	got, err := app.Messages(ctx, "compartida", 0)
	if err != nil {
		t.Fatalf("Messages (app) = %v", err)
	}
	if len(got) != 2 || got[0].ID != "m1" || got[1].ID != "m2" {
		t.Fatalf("Messages (app) = %+v, want m1 and m2 in order", got)
	}
}

func TestOpenDefault_FallsBackToMemoryOnError(t *testing.T) {
	// A SQLite failure returns both the error and a usable in-memory store.
	ctx := context.Background()
	plain := filepath.Join(t.TempDir(), "plano")
	if err := os.WriteFile(plain, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) = %v", plain, err)
	}
	t.Setenv("ATENEA_DB", filepath.Join(plain, "sub", "atenea.db"))

	store, err := OpenDefault()
	if err == nil {
		t.Fatal("OpenDefault() error = nil, want an error for a file parent")
	}
	if store == nil {
		t.Fatal("OpenDefault() returned a nil fallback store")
	}
	if _, err := store.AppendEvent(ctx, "s1", SessionEvent{Message: &Message{ID: "m1", Role: RoleUser, Text: "hola"}}); err != nil {
		t.Fatalf("AppendEvent on fallback = %v", err)
	}
	if _, err := store.LoadSession(ctx, "s1"); err != nil {
		t.Fatalf("LoadSession on fallback = %v", err)
	}
}

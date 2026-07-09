package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestSQLiteStore_ReopenResumesLog verifica la propiedad que solo el store
// durable sobre archivo puede expresar: tras cerrar y reabrir la base en la
// misma ruta, el log se reanuda. Los mensajes se reconstruyen en orden y el
// siguiente AppendEvent continua la secuencia desde el ultimo Seq persistido.
func TestSQLiteStore_ReopenResumesLog(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "atenea.db")

	s1, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore (primera): %v", err)
	}
	if _, err := s1.AppendEvent(ctx, "s1", SessionEvent{Message: &Message{ID: "m1", Role: RoleUser, Text: "uno"}}); err != nil {
		t.Fatalf("AppendEvent (m1): %v", err)
	}
	lastSeq, err := s1.AppendEvent(ctx, "s1", SessionEvent{Message: &Message{ID: "m2", Role: RoleAssistant, Text: "dos"}})
	if err != nil {
		t.Fatalf("AppendEvent (m2): %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close (primera): %v", err)
	}

	s2, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore (reabrir): %v", err)
	}
	t.Cleanup(func() { s2.Close() })

	got, err := s2.Messages(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("Messages tras reabrir: %v", err)
	}
	want := []Message{
		{ID: "m1", Role: RoleUser, Text: "uno", Seq: 1},
		{ID: "m2", Role: RoleAssistant, Text: "dos", Seq: 2},
	}
	if len(got) != len(want) {
		t.Fatalf("Messages tras reabrir: got %d, want %d (%+v)", len(got), len(want), got)
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Fatalf("Messages[%d] tras reabrir: got %+v, want %+v", i, got[i], want[i])
		}
	}

	next, err := s2.AppendEvent(ctx, "s1", SessionEvent{})
	if err != nil {
		t.Fatalf("AppendEvent tras reabrir: %v", err)
	}
	if next != lastSeq+1 {
		t.Fatalf("AppendEvent tras reabrir: got Seq %d, want %d (la secuencia no continuo)", next, lastSeq+1)
	}
}

// TestSQLiteStore_SessionsOrderSurvivesReopen verifica que el orden por recencia
// de Sessions se apoya en el rowid persistido, no en estado en memoria: tras
// cerrar y reabrir la base, la sesion con actividad mas reciente sigue primero.
func TestSQLiteStore_SessionsOrderSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "atenea.db")

	s1, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore (primera): %v", err)
	}
	// "old" recibe el primer evento; "new" el ultimo: "new" es la mas reciente.
	if _, err := s1.AppendEvent(ctx, "old", SessionEvent{Message: &Message{ID: "u1", Role: RoleUser, Text: "vieja"}}); err != nil {
		t.Fatalf("AppendEvent (old): %v", err)
	}
	if _, err := s1.AppendEvent(ctx, "new", SessionEvent{Message: &Message{ID: "u2", Role: RoleUser, Text: "nueva"}}); err != nil {
		t.Fatalf("AppendEvent (new): %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore (reabrir): %v", err)
	}
	t.Cleanup(func() { s2.Close() })

	got, err := s2.Sessions(ctx)
	if err != nil {
		t.Fatalf("Sessions tras reabrir: %v", err)
	}
	want := []SessionSummary{
		{ID: "new", Title: "nueva"},
		{ID: "old", Title: "vieja"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Sessions tras reabrir: got %+v, want %+v (el orden por recencia no sobrevivio)", got, want)
	}
}

// TestSQLiteStore_DeleteSessionSurvivesReopen verifica que el borrado se
// persiste en la base, no solo en estado en memoria: tras borrar una sesion,
// cerrar y reabrir en la misma ruta, la sesion borrada sigue sin existir y la
// que sobrevivio se mantiene. Sin durabilidad, reabrir reviviria los eventos.
func TestSQLiteStore_DeleteSessionSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "atenea.db")

	s1, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore (primera): %v", err)
	}
	if _, err := s1.AppendEvent(ctx, "keep", SessionEvent{Message: &Message{ID: "u1", Role: RoleUser, Text: "me quedo"}}); err != nil {
		t.Fatalf("AppendEvent (keep): %v", err)
	}
	if _, err := s1.AppendEvent(ctx, "drop", SessionEvent{Message: &Message{ID: "u2", Role: RoleUser, Text: "me borran"}}); err != nil {
		t.Fatalf("AppendEvent (drop): %v", err)
	}
	if err := s1.DeleteSession(ctx, "drop"); err != nil {
		t.Fatalf("DeleteSession (drop): %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore (reabrir): %v", err)
	}
	t.Cleanup(func() { s2.Close() })

	if _, err := s2.LoadSession(ctx, "drop"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("LoadSession(drop) tras reabrir: got %v, want ErrSessionNotFound (el borrado no se persistio)", err)
	}

	got, err := s2.Sessions(ctx)
	if err != nil {
		t.Fatalf("Sessions tras reabrir: %v", err)
	}
	want := []SessionSummary{
		{ID: "keep", Title: "me quedo"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Sessions tras reabrir: got %+v, want %+v (el borrado o el sobreviviente no sobrevivieron)", got, want)
	}
}

// TestSQLiteStore_ProjectsToolCallsAndToolCallID fija la paridad de SQLite con
// MemoryStore para las partes ricas de la proyeccion: el assistant con tool calls
// y el resultado de la tool con su tool_call_id deben sobrevivir el round-trip por
// la base. Apende ambos eventos y afirma que Messages reconstruye los DOS mensajes
// con sus ToolCalls / ToolCallID intactos (incluido Seq). Hoy SQLite no persiste
// esas columnas, asi que el round-trip las pierde.
func TestSQLiteStore_ProjectsToolCallsAndToolCallID(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if _, err := store.AppendEvent(ctx, "s1", SessionEvent{Kind: KindStepEnded, Message: &Message{
		ID: "a1", Role: RoleAssistant,
		ToolCalls: []ToolCall{{ID: "call_1", Name: "read", Arguments: `{"path":"foo.go"}`}},
	}}); err != nil {
		t.Fatalf("AppendEvent (assistant): %v", err)
	}
	if _, err := store.AppendEvent(ctx, "s1", SessionEvent{Kind: KindToolSuccess, Message: &Message{
		ID: "call_1", Role: RoleTool, Text: "contenido", ToolCallID: "call_1",
	}}); err != nil {
		t.Fatalf("AppendEvent (tool result): %v", err)
	}

	got, err := store.Messages(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	want := []Message{
		{ID: "a1", Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_1", Name: "read", Arguments: `{"path":"foo.go"}`}}, Seq: 1},
		{ID: "call_1", Role: RoleTool, Text: "contenido", ToolCallID: "call_1", Seq: 2},
	}
	if len(got) != len(want) {
		t.Fatalf("Messages: got %d, want %d (%+v)", len(got), len(want), got)
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Fatalf("Messages[%d]: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSQLiteStore_CompactionSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "atenea.db")

	first, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore (first): %v", err)
	}
	coveredSeq := appendCompactionMessage(t, first, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "old"})
	anchorSeq := appendCompactionMessage(t, first, ctx, "s1", Message{ID: "u2", Role: RoleUser, Text: "current"})
	checkpoint := compactionCheckpoint(compactionEpoch(t, first, ctx, "s1"), coveredSeq, anchorSeq, anchorSeq)
	checkpoint.Model = "claude-sonnet-4"
	checkpoint.InputTokensBefore = 800
	checkpoint.EstimatedTokensAfter = 300
	if _, err := first.CommitCompaction(ctx, "s1", checkpoint); err != nil {
		first.Close()
		t.Fatalf("CommitCompaction: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close first: %v", err)
	}

	second, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore (second): %v", err)
	}
	t.Cleanup(func() { second.Close() })

	got, err := second.ContextForRunner(ctx, "s1")
	if err != nil {
		t.Fatalf("ContextForRunner after reopen: %v", err)
	}
	if got.Epoch != (ContextEpoch{BaselineSeq: coveredSeq, Revision: 1}) {
		t.Fatalf("epoch after reopen = %+v", got.Epoch)
	}
	if got.Checkpoint == nil || !reflect.DeepEqual(*got.Checkpoint, checkpoint) {
		t.Fatalf("checkpoint after reopen = %+v, want %+v", got.Checkpoint, checkpoint)
	}
	if got.Anchor != nil || len(got.Messages) != 1 || got.Messages[0].Seq != anchorSeq {
		t.Fatalf("runner context after reopen = %+v", got)
	}

	events, err := second.Events(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("Events after reopen: %v", err)
	}
	last := events[len(events)-1]
	if last.Kind != KindContextCompacted || last.Compaction == nil || !reflect.DeepEqual(*last.Compaction, checkpoint) {
		t.Fatalf("compaction event after reopen = %+v", last)
	}
}

func TestSQLiteStore_ContextForRunnerRejectsInvalidDurableCheckpoint(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "current"})
	invalid := compactionCheckpoint(ContextEpoch{}, 1, 1, 2)
	invalid.Model = ""
	raw, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO session_context (session_id, checkpoint) VALUES (?, ?)`, "s1", raw,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ContextForRunner(ctx, "s1"); !errors.Is(err, ErrInvalidCompactionCheckpoint) {
		t.Fatalf("ContextForRunner error = %v, want ErrInvalidCompactionCheckpoint", err)
	}
}

func TestSQLiteStore_CompactionCASAcrossStoreInstances(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "atenea.db")
	first, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore first: %v", err)
	}
	t.Cleanup(func() { first.Close() })
	second, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore second: %v", err)
	}
	t.Cleanup(func() { second.Close() })

	anchorSeq := appendCompactionMessage(t, first, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "current"})
	preservedSeq := appendCompactionMessage(t, first, ctx, "s1", Message{ID: "a1", Role: RoleAssistant, Text: "preserved"})
	checkpoint := compactionCheckpoint(compactionEpoch(t, first, ctx, "s1"), anchorSeq, anchorSeq, preservedSeq)

	start := make(chan struct{})
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, store := range []*SQLiteStore{first, second} {
		go func(store *SQLiteStore) {
			ready.Done()
			<-start
			_, err := store.CommitCompaction(ctx, "s1", checkpoint)
			results <- err
		}(store)
	}
	ready.Wait()
	close(start)

	var successes, conflicts int
	for range 2 {
		var err error
		select {
		case err = <-results:
		case <-time.After(time.Second):
			t.Fatal("cross-store commits timed out")
		}
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrCompactionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected commit error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	events, err := first.Events(ctx, "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[2].Kind != KindContextCompacted {
		t.Fatalf("events after competing commits = %+v", events)
	}
}

func TestSQLiteStore_CompactionCASAcrossFileURIStores(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "atenea.db") + "?mode=rwc"
	first, err := NewSQLiteStore(dsn)
	if err != nil {
		t.Fatalf("NewSQLiteStore first: %v", err)
	}
	t.Cleanup(func() { first.Close() })
	second, err := NewSQLiteStore(dsn)
	if err != nil {
		t.Fatalf("NewSQLiteStore second: %v", err)
	}
	t.Cleanup(func() { second.Close() })

	anchorSeq := appendCompactionMessage(t, first, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "current"})
	preservedSeq := appendCompactionMessage(t, first, ctx, "s1", Message{ID: "a1", Role: RoleAssistant, Text: "preserved"})
	checkpoint := compactionCheckpoint(compactionEpoch(t, first, ctx, "s1"), anchorSeq, anchorSeq, preservedSeq)

	start := make(chan struct{})
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, store := range []*SQLiteStore{first, second} {
		go func(store *SQLiteStore) {
			ready.Done()
			<-start
			_, err := store.CommitCompaction(ctx, "s1", checkpoint)
			results <- err
		}(store)
	}
	ready.Wait()
	close(start)

	var successes, conflicts int
	for range 2 {
		var err error
		select {
		case err = <-results:
		case <-time.After(time.Second):
			t.Fatal("file URI cross-store commits timed out")
		}
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrCompactionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected commit error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestSQLiteDSN_PreservesFileURIParametersAndLeavesMemoryUnchanged(t *testing.T) {
	got, err := sqliteDSN("file:/tmp/atenea.db?mode=rwc&cache=shared&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("mode") != "rwc" || query.Get("cache") != "shared" || query.Get("_txlock") != "immediate" {
		t.Fatalf("normalized query = %v", query)
	}
	pragmas := query["_pragma"]
	for _, want := range []string{"foreign_keys(1)", "journal_mode(WAL)", "busy_timeout(5000)"} {
		if !slices.Contains(pragmas, want) {
			t.Fatalf("pragmas = %v, missing %q", pragmas, want)
		}
	}

	memory, err := sqliteDSN(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if memory != ":memory:" {
		t.Fatalf("memory dsn = %q, want :memory:", memory)
	}
}

func TestSQLiteStore_PlainPathEscapesReservedURLCharacters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atenea ?#% data.db")
	dsn, err := sqliteDSN(path)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != path || parsed.RawQuery == "" || parsed.Fragment != "" {
		t.Fatalf("normalized dsn = %q, parsed path=%q query=%q fragment=%q", dsn, parsed.Path, parsed.RawQuery, parsed.Fragment)
	}
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database path not created exactly: %v", err)
	}
}

func TestSQLiteStore_CommitCompactionIgnoresMalformedToolCallsBeforePreservedSuffix(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO events (session_id, seq, kind, has_message, msg_id, role, text, tool_calls)
		 VALUES ('s1', 1, '', 1, 'old', 'assistant', 'covered', '{')`,
	); err != nil {
		t.Fatal(err)
	}
	anchorSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "current"})
	preservedSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "a1", Role: RoleAssistant, Text: "preserved"})
	checkpoint := compactionCheckpoint(compactionEpoch(t, store, ctx, "s1"), 1, anchorSeq, preservedSeq)
	if _, err := store.CommitCompaction(ctx, "s1", checkpoint); err != nil {
		t.Fatalf("CommitCompaction decoded covered tool_calls: %v", err)
	}
}

func TestSQLiteStore_CompactionErrorsIncludeOperationAndSession(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.ContextForRunner(ctx, "missing"); !errors.Is(err, ErrSessionNotFound) || !strings.Contains(err.Error(), "ContextForRunner") || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("ContextForRunner error = %v", err)
	}
	checkpoint := compactionCheckpoint(ContextEpoch{}, 1, 2, 2)
	if _, err := store.CommitCompaction(ctx, "missing", checkpoint); !errors.Is(err, ErrSessionNotFound) || !strings.Contains(err.Error(), "CommitCompaction") || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("CommitCompaction error = %v", err)
	}
}

func TestSQLiteStore_MigratesLegacyDatabaseWithZeroEpoch(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE events (
		  session_id TEXT NOT NULL, seq INTEGER NOT NULL, kind TEXT NOT NULL,
		  has_message INTEGER NOT NULL, msg_id TEXT, role TEXT, text TEXT,
		  call_id TEXT, tool_name TEXT, input BLOB, usage BLOB, error TEXT,
		  PRIMARY KEY (session_id, seq)
		);
		INSERT INTO events (session_id, seq, kind, has_message, msg_id, role, text)
		VALUES ('s1', 1, '', 1, 'u1', 'user', 'legacy');
	`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore legacy: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	got, err := store.ContextForRunner(ctx, "s1")
	if err != nil {
		t.Fatalf("ContextForRunner legacy: %v", err)
	}
	if got.Epoch != (ContextEpoch{}) || got.Checkpoint != nil || len(got.Messages) != 1 || got.Messages[0].Text != "legacy" {
		t.Fatalf("legacy context = %+v", got)
	}
}

func TestSQLiteStore_ConcurrentLegacyMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE events (
		session_id TEXT NOT NULL, seq INTEGER NOT NULL, kind TEXT NOT NULL,
		has_message INTEGER NOT NULL, msg_id TEXT, role TEXT, text TEXT,
		call_id TEXT, tool_name TEXT, input BLOB, usage BLOB, error TEXT,
		PRIMARY KEY (session_id, seq)
	)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	stores := make(chan *SQLiteStore, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			store, err := NewSQLiteStore(path)
			if err == nil {
				stores <- store
			}
			results <- err
		}()
	}
	ready.Wait()
	close(start)
	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("concurrent migration: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent migration timed out")
		}
	}
	close(stores)
	for store := range stores {
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSQLiteStore_ContextForRunnerRejectsEpochCheckpointMismatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ContextEpoch, *CompactionCheckpoint)
	}{
		{"baseline", func(epoch *ContextEpoch, checkpoint *CompactionCheckpoint) { epoch.BaselineSeq-- }},
		{"revision", func(epoch *ContextEpoch, checkpoint *CompactionCheckpoint) { epoch.Revision++ }},
		{"agent", func(epoch *ContextEpoch, checkpoint *CompactionCheckpoint) { epoch.Agent = "other-agent" }},
		{"active model", func(epoch *ContextEpoch, checkpoint *CompactionCheckpoint) { epoch.Model = "other-model" }},
		{"generation model", func(epoch *ContextEpoch, checkpoint *CompactionCheckpoint) { checkpoint.Model = "other-model" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := NewSQLiteStore(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { store.Close() })
			coveredSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "old"})
			anchorSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u2", Role: RoleUser, Text: "current"})
			checkpoint := compactionCheckpoint(ContextEpoch{Agent: "agent", Model: "model"}, coveredSeq, anchorSeq, anchorSeq)
			checkpoint.Model = "model"
			epoch := ContextEpoch{Agent: "agent", Model: "model", BaselineSeq: coveredSeq, Revision: 1}
			test.mutate(&epoch, &checkpoint)
			raw, err := json.Marshal(checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx,
				`INSERT INTO session_context (session_id, agent, model, baseline_seq, revision, checkpoint) VALUES (?, ?, ?, ?, ?, ?)`,
				"s1", epoch.Agent, epoch.Model, epoch.BaselineSeq, epoch.Revision, raw,
			); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ContextForRunner(ctx, "s1"); !errors.Is(err, ErrInvalidCompactionCheckpoint) || !strings.Contains(err.Error(), "corrupt") {
				t.Fatalf("ContextForRunner error = %v", err)
			}
		})
	}
}

func TestSQLiteStore_EventsRejectsCorruptCompactionPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
	}{
		{"malformed json", []byte(`{`)},
		{"null arrays", func() []byte {
			checkpoint := compactionCheckpoint(ContextEpoch{}, 1, 2, 2)
			checkpoint.Summary.Pending = nil
			raw, err := json.Marshal(checkpoint)
			if err != nil {
				panic(err)
			}
			return raw
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := NewSQLiteStore(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { store.Close() })
			if _, err := store.db.ExecContext(ctx,
				`INSERT INTO events (session_id, seq, kind, has_message, compaction) VALUES ('s1', 1, ?, 0, ?)`,
				string(KindContextCompacted), test.payload,
			); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Events(ctx, "s1", 0); !errors.Is(err, ErrInvalidCompactionCheckpoint) {
				t.Fatalf("Events error = %v, want ErrInvalidCompactionCheckpoint", err)
			}
		})
	}
}

func TestSQLiteStore_OpenErrorsSanitizeFileDSNQuery(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "missing", "atenea.db") + "?mode=ro&api_key=super-secret"
	_, err := NewSQLiteStore(dsn)
	if err == nil {
		t.Fatal("NewSQLiteStore must fail")
	}
	if strings.Contains(err.Error(), "super-secret") || strings.Contains(err.Error(), "api_key") {
		t.Fatalf("error leaked query parameters: %v", err)
	}
	if !strings.Contains(err.Error(), "initialize sqlite schema") || !strings.Contains(err.Error(), "atenea.db") {
		t.Fatalf("error lacks operation/path context: %v", err)
	}
}

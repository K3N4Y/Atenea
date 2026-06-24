package session

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
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

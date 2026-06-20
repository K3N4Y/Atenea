package session

import (
	"context"
	"path/filepath"
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
		if got[i] != want[i] {
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

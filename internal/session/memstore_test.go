package session

import (
	"context"
	"testing"
)

// TestMemoryStore_AppendEventAssignsMonotonicSeq fija el contrato central del
// store: cada AppendEvent devuelve el siguiente Seq monotonico de la sesion,
// empezando en 1 y creciendo de a 1.
func TestMemoryStore_AppendEventAssignsMonotonicSeq(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	for want := Seq(1); want <= 3; want++ {
		got, err := store.AppendEvent(ctx, "s1", SessionEvent{})
		if err != nil {
			t.Fatalf("AppendEvent #%d: unexpected error: %v", want, err)
		}
		if got != want {
			t.Fatalf("AppendEvent #%d: got Seq %d, want %d", want, got, want)
		}
	}
}

// appendMessage es un helper de test: agrega un evento que materializa un
// mensaje y devuelve el Seq asignado.
func appendMessage(t *testing.T, store *MemoryStore, sessionID string, m Message) Seq {
	t.Helper()
	seq, err := store.AppendEvent(context.Background(), sessionID, SessionEvent{Message: &m})
	if err != nil {
		t.Fatalf("AppendEvent(%q): unexpected error: %v", sessionID, err)
	}
	return seq
}

// TestMemoryStore_MessagesReturnsInSeqOrder verifica que Messages reproyecta los
// mensajes en orden de Seq y que los eventos sin mensaje no aportan a la
// proyeccion. Cada mensaje devuelto lleva el Seq del evento que lo materializo.
func TestMemoryStore_MessagesReturnsInSeqOrder(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	appendMessage(t, store, "s1", Message{ID: "m1", Role: RoleUser, Text: "hola"})
	if _, err := store.AppendEvent(ctx, "s1", SessionEvent{}); err != nil { // evento sin mensaje
		t.Fatalf("AppendEvent (sin mensaje): unexpected error: %v", err)
	}
	appendMessage(t, store, "s1", Message{ID: "m2", Role: RoleAssistant, Text: "que tal"})

	got, err := store.Messages(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("Messages: unexpected error: %v", err)
	}

	want := []Message{
		{ID: "m1", Role: RoleUser, Text: "hola", Seq: 1},
		{ID: "m2", Role: RoleAssistant, Text: "que tal", Seq: 3},
	}
	if len(got) != len(want) {
		t.Fatalf("Messages: got %d messages, want %d (%+v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Messages[%d]: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

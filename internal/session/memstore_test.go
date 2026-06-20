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

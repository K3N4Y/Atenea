package session

import (
	"context"
	"errors"
	"sort"
	"sync"
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

// TestMemoryStore_MessagesSinceSeqFiltersOlder verifica que sinceSeq devuelve
// solo los mensajes materializados por eventos con Seq estrictamente mayor.
func TestMemoryStore_MessagesSinceSeqFiltersOlder(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	appendMessage(t, store, "s1", Message{ID: "m1", Role: RoleUser, Text: "uno"})
	seq2 := appendMessage(t, store, "s1", Message{ID: "m2", Role: RoleAssistant, Text: "dos"})
	appendMessage(t, store, "s1", Message{ID: "m3", Role: RoleUser, Text: "tres"})

	got, err := store.Messages(ctx, "s1", seq2)
	if err != nil {
		t.Fatalf("Messages: unexpected error: %v", err)
	}

	if len(got) != 1 || got[0].ID != "m3" || got[0].Seq != 3 {
		t.Fatalf("Messages(sinceSeq=%d): got %+v, want only m3 with Seq 3", seq2, got)
	}
}

// TestMemoryStore_MessagesSinceSeqBeyondLastReturnsEmpty verifica que un
// sinceSeq mayor que el ultimo Seq devuelve vacio sin error.
func TestMemoryStore_MessagesSinceSeqBeyondLastReturnsEmpty(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	last := appendMessage(t, store, "s1", Message{ID: "m1", Role: RoleUser, Text: "uno"})

	got, err := store.Messages(ctx, "s1", last+5)
	if err != nil {
		t.Fatalf("Messages: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Messages(sinceSeq beyond last): got %+v, want empty", got)
	}
}

// TestMemoryStore_UnknownSessionReturnsNotFound verifica que leer una sesion que
// nunca recibio un evento devuelve ErrSessionNotFound tanto en LoadSession como
// en Messages.
func TestMemoryStore_UnknownSessionReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	if _, err := store.LoadSession(ctx, "ghost"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("LoadSession(ghost): got %v, want ErrSessionNotFound", err)
	}
	if _, err := store.Messages(ctx, "ghost", 0); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Messages(ghost): got %v, want ErrSessionNotFound", err)
	}
}

// TestMemoryStore_EpochIsStableAndNotFound fija el contrato del epoch que el runner
// asume: Epoch de una sesion inexistente devuelve ErrSessionNotFound (igual que
// LoadSession/Messages), y tras un AppendEvent dos lecturas consecutivas devuelven el
// MISMO valor (estable). La estabilidad es lo que evita un rebuild espurio en el
// camino feliz: el snapshot y el recheck del attempt leen el mismo epoch y no
// reconstruyen.
func TestMemoryStore_EpochIsStableAndNotFound(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	// Sesion inexistente: mismo contrato de no-encontrado que LoadSession/Messages.
	if _, err := store.Epoch(ctx, "ghost"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Epoch(ghost): got %v, want ErrSessionNotFound", err)
	}

	if _, err := store.AppendEvent(ctx, "s1", SessionEvent{}); err != nil {
		t.Fatalf("AppendEvent: unexpected error: %v", err)
	}

	// Dos lecturas consecutivas devuelven el mismo epoch: estable, sin rebuild espurio.
	first, err := store.Epoch(ctx, "s1")
	if err != nil {
		t.Fatalf("Epoch (primera): unexpected error: %v", err)
	}
	second, err := store.Epoch(ctx, "s1")
	if err != nil {
		t.Fatalf("Epoch (segunda): unexpected error: %v", err)
	}
	if first != second {
		t.Fatalf("Epoch no es estable: primera %+v, segunda %+v (rebuild espurio)", first, second)
	}
}

// TestMemoryStore_ConcurrentAppendsAssignUniqueSeqs verifica que bajo appends
// concurrentes sobre la misma sesion los Seq devueltos forman exactamente
// {1..N} sin huecos ni duplicados. Se corre con -race.
func TestMemoryStore_ConcurrentAppendsAssignUniqueSeqs(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	const n = 100
	seqs := make([]Seq, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			seq, err := store.AppendEvent(ctx, "s1", SessionEvent{})
			if err != nil {
				t.Errorf("AppendEvent: unexpected error: %v", err)
				return
			}
			seqs[i] = seq
		}(i)
	}
	wg.Wait()

	sort.Slice(seqs, func(a, b int) bool { return seqs[a] < seqs[b] })
	for i := 0; i < n; i++ {
		if seqs[i] != Seq(i+1) {
			t.Fatalf("sorted seqs[%d] = %d, want %d (gaps or duplicates: %v)", i, seqs[i], i+1, seqs)
		}
	}
}

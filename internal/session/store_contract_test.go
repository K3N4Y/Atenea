package session

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
)

// testStoreContract corre el contrato durable del Store contra cualquier
// implementacion. newStore devuelve un store vacio y listo (un MemoryStore
// nuevo, o un SQLiteStore sobre ":memory:" o un archivo temporal). Replica el
// comportamiento que memstore_test.go fija para MemoryStore: SQLiteStore debe
// comportarse identico.
func testStoreContract(t *testing.T, newStore func(t *testing.T) Store) {
	// appendContractMessage agrega un evento que materializa un mensaje sobre
	// cualquier Store y devuelve el Seq asignado.
	appendContractMessage := func(t *testing.T, store Store, sessionID string, m Message) Seq {
		t.Helper()
		seq, err := store.AppendEvent(context.Background(), sessionID, SessionEvent{Message: &m})
		if err != nil {
			t.Fatalf("AppendEvent(%q): unexpected error: %v", sessionID, err)
		}
		return seq
	}

	t.Run("AppendEventAssignsMonotonicSeq", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)
		for want := Seq(1); want <= 3; want++ {
			got, err := store.AppendEvent(ctx, "s1", SessionEvent{})
			if err != nil {
				t.Fatalf("AppendEvent #%d: %v", want, err)
			}
			if got != want {
				t.Fatalf("AppendEvent #%d: got Seq %d, want %d", want, got, want)
			}
		}
	})

	t.Run("MessagesReturnInSeqOrder", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		appendContractMessage(t, store, "s1", Message{ID: "m1", Role: RoleUser, Text: "hola"})
		if _, err := store.AppendEvent(ctx, "s1", SessionEvent{}); err != nil { // evento sin mensaje
			t.Fatalf("AppendEvent (sin mensaje): unexpected error: %v", err)
		}
		appendContractMessage(t, store, "s1", Message{ID: "m2", Role: RoleAssistant, Text: "que tal"})

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
	})

	t.Run("MessagesSinceSeqFiltersOlder", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		appendContractMessage(t, store, "s1", Message{ID: "m1", Role: RoleUser, Text: "uno"})
		seq2 := appendContractMessage(t, store, "s1", Message{ID: "m2", Role: RoleAssistant, Text: "dos"})
		appendContractMessage(t, store, "s1", Message{ID: "m3", Role: RoleUser, Text: "tres"})

		got, err := store.Messages(ctx, "s1", seq2)
		if err != nil {
			t.Fatalf("Messages: unexpected error: %v", err)
		}
		if len(got) != 1 || got[0].ID != "m3" || got[0].Seq != 3 {
			t.Fatalf("Messages(sinceSeq=%d): got %+v, want only m3 with Seq 3", seq2, got)
		}
	})

	t.Run("MessagesSinceSeqBeyondLastReturnsEmpty", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		last := appendContractMessage(t, store, "s1", Message{ID: "m1", Role: RoleUser, Text: "uno"})

		got, err := store.Messages(ctx, "s1", last+5)
		if err != nil {
			t.Fatalf("Messages: unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("Messages(sinceSeq beyond last): got %+v, want empty", got)
		}
	})

	t.Run("UnknownSessionReturnsNotFound", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		if _, err := store.LoadSession(ctx, "ghost"); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("LoadSession(ghost): got %v, want ErrSessionNotFound", err)
		}
		if _, err := store.Messages(ctx, "ghost", 0); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("Messages(ghost): got %v, want ErrSessionNotFound", err)
		}
		if _, err := store.Epoch(ctx, "ghost"); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("Epoch(ghost): got %v, want ErrSessionNotFound", err)
		}
		if _, err := store.PendingToolCalls(ctx, "ghost"); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("PendingToolCalls(ghost): got %v, want ErrSessionNotFound", err)
		}
	})

	t.Run("LoadSessionReturnsSessionAfterAppend", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		if _, err := store.AppendEvent(ctx, "s1", SessionEvent{}); err != nil {
			t.Fatalf("AppendEvent: unexpected error: %v", err)
		}
		got, err := store.LoadSession(ctx, "s1")
		if err != nil {
			t.Fatalf("LoadSession(s1): unexpected error: %v", err)
		}
		if got != (Session{ID: "s1"}) {
			t.Fatalf("LoadSession(s1): got %+v, want {ID:s1}", got)
		}
	})

	t.Run("EpochStableAndNotFound", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

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
	})

	t.Run("PendingToolCallsFoldsCalledVsResolved", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		if _, err := store.AppendEvent(ctx, "s1", SessionEvent{Kind: KindToolCalled, CallID: "c1", ToolName: "echo"}); err != nil {
			t.Fatalf("AppendEvent (c1 called): unexpected error: %v", err)
		}
		if _, err := store.AppendEvent(ctx, "s1", SessionEvent{Kind: KindToolCalled, CallID: "c2", ToolName: "read"}); err != nil {
			t.Fatalf("AppendEvent (c2 called): unexpected error: %v", err)
		}
		if _, err := store.AppendEvent(ctx, "s1", SessionEvent{Kind: KindToolSuccess, CallID: "c2", ToolName: "read"}); err != nil {
			t.Fatalf("AppendEvent (c2 success): unexpected error: %v", err)
		}

		pending, err := store.PendingToolCalls(ctx, "s1")
		if err != nil {
			t.Fatalf("PendingToolCalls(s1): unexpected error: %v", err)
		}
		if len(pending) != 1 {
			t.Fatalf("PendingToolCalls(s1) = %+v, want only c1", pending)
		}
		if pending[0] != (PendingTool{CallID: "c1", ToolName: "echo"}) {
			t.Fatalf("PendingToolCalls(s1)[0] = %+v, want c1 echo", pending[0])
		}

		// Sesion con solo un Message (sin tool calls): proyeccion vacia.
		if _, err := store.AppendEvent(ctx, "empty", SessionEvent{Message: &Message{ID: "u1", Role: RoleUser, Text: "hola"}}); err != nil {
			t.Fatalf("AppendEvent (empty session): unexpected error: %v", err)
		}
		empty, err := store.PendingToolCalls(ctx, "empty")
		if err != nil {
			t.Fatalf("PendingToolCalls(empty): unexpected error: %v", err)
		}
		if len(empty) != 0 {
			t.Fatalf("PendingToolCalls(empty) = %+v, want empty", empty)
		}
	})

	t.Run("ConcurrentAppendsAssignUniqueSeqs", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

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
	})
}

func TestMemoryStore_Contract(t *testing.T) {
	testStoreContract(t, func(t *testing.T) Store { return NewMemoryStore() })
}

func TestSQLiteStore_Contract(t *testing.T) {
	testStoreContract(t, func(t *testing.T) Store {
		s, err := NewSQLiteStore(":memory:")
		if err != nil {
			t.Fatalf("NewSQLiteStore: %v", err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	})
}

package session

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
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
			if !reflect.DeepEqual(got[i], want[i]) {
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

	t.Run("SessionsEmptyWhenNoSessions", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		got, err := store.Sessions(ctx)
		if err != nil {
			t.Fatalf("Sessions: unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("Sessions on empty store: got %+v, want empty", got)
		}
	})

	t.Run("SessionsOrdersByRecencyWithFirstUserMessageTitle", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		// s1 recibe su primer mensaje primero; su titulo es ese primer prompt
		// aunque luego lleguen mas mensajes del usuario.
		appendContractMessage(t, store, "s1", Message{ID: "m1", Role: RoleUser, Text: "primera pregunta"})
		appendContractMessage(t, store, "s1", Message{ID: "m2", Role: RoleAssistant, Text: "respuesta"})
		// s2 llega despues: debe quedar antes que s1 (mas reciente).
		appendContractMessage(t, store, "s2", Message{ID: "m3", Role: RoleUser, Text: "otra cosa"})
		// s1 vuelve a tener actividad: pasa a ser la mas reciente.
		appendContractMessage(t, store, "s1", Message{ID: "m4", Role: RoleUser, Text: "segunda pregunta"})

		got, err := store.Sessions(ctx)
		if err != nil {
			t.Fatalf("Sessions: unexpected error: %v", err)
		}
		want := []SessionSummary{
			{ID: "s1", Title: "primera pregunta"},
			{ID: "s2", Title: "otra cosa"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Sessions: got %+v, want %+v", got, want)
		}
	})

	t.Run("SessionsTitleEmptyWhenNoUserMessage", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		// Sesion con eventos pero sin mensaje de usuario: Title vacio.
		if _, err := store.AppendEvent(ctx, "s1", SessionEvent{Kind: KindStepStarted}); err != nil {
			t.Fatalf("AppendEvent: unexpected error: %v", err)
		}

		got, err := store.Sessions(ctx)
		if err != nil {
			t.Fatalf("Sessions: unexpected error: %v", err)
		}
		want := []SessionSummary{{ID: "s1", Title: ""}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Sessions: got %+v, want %+v", got, want)
		}
	})

	t.Run("SessionsPrefersTitleEventOverFirstUserMessage", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		// Primer mensaje del usuario (el fallback) y luego un evento Session.Title:
		// el titulo generado gana sobre el primer prompt. El ultimo Session.Title
		// es el vigente.
		appendContractMessage(t, store, "s1", Message{ID: "m1", Role: RoleUser, Text: "como configuro el proxy"})
		if _, err := store.AppendEvent(ctx, "s1", SessionEvent{Kind: KindSessionTitle, Text: "Configuracion del proxy"}); err != nil {
			t.Fatalf("AppendEvent (Session.Title): unexpected error: %v", err)
		}

		got, err := store.Sessions(ctx)
		if err != nil {
			t.Fatalf("Sessions: unexpected error: %v", err)
		}
		want := []SessionSummary{{ID: "s1", Title: "Configuracion del proxy"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Sessions: got %+v, want %+v", got, want)
		}

		// El titulo generado tambien se trunca a 80 runes, igual que el fallback.
		long := strings.Repeat("ñ", 200)
		if _, err := store.AppendEvent(ctx, "s2", SessionEvent{Kind: KindSessionTitle, Text: long}); err != nil {
			t.Fatalf("AppendEvent (Session.Title largo): unexpected error: %v", err)
		}
		got, err = store.Sessions(ctx)
		if err != nil {
			t.Fatalf("Sessions: unexpected error: %v", err)
		}
		var s2 *SessionSummary
		for i := range got {
			if got[i].ID == "s2" {
				s2 = &got[i]
			}
		}
		if s2 == nil {
			t.Fatalf("Sessions: no aparece s2 en %+v", got)
		}
		if wantTitle := strings.Repeat("ñ", 80); s2.Title != wantTitle {
			t.Fatalf("Session.Title no truncado a 80 runes: got %d runes", len([]rune(s2.Title)))
		}
	})

	t.Run("SessionsTruncatesLongTitleTo80Runes", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		// Titulo con runes multibyte para verificar corte por rune, no por byte.
		long := strings.Repeat("ñ", 200)
		appendContractMessage(t, store, "s1", Message{ID: "m1", Role: RoleUser, Text: long})

		got, err := store.Sessions(ctx)
		if err != nil {
			t.Fatalf("Sessions: unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("Sessions: got %+v, want one session", got)
		}
		if want := strings.Repeat("ñ", 80); got[0].Title != want {
			t.Fatalf("Sessions title not truncated to 80 runes: got %d runes", len([]rune(got[0].Title)))
		}
	})

	t.Run("EventsRoundTripsLogInSeqOrder", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		// Una secuencia representativa: prompt de usuario, assistant con tool call
		// (coalescido en Step.Ended con Usage), tool result, y eventos de streaming
		// sin mensaje. Events debe devolverla identica (salvo SessionID/Seq que fija
		// el store).
		in := []SessionEvent{
			{Kind: KindStepStarted},
			{Message: &Message{ID: "u1", Role: RoleUser, Text: "hola"}},
			{Kind: KindToolCalled, CallID: "c1", ToolName: "read", Input: json.RawMessage(`{"path":"foo.go"}`)},
			{Kind: KindToolSuccess, CallID: "c1", ToolName: "read", Message: &Message{ID: "c1", Role: RoleTool, Text: "contenido", ToolCallID: "c1"}},
			{Kind: KindToolFailed, CallID: "c2", ToolName: "bash", Error: "boom"},
			{Kind: KindStepEnded, Message: &Message{
				ID: "a1", Role: RoleAssistant, Text: "listo",
				ToolCalls: []ToolCall{{ID: "c1", Name: "read", Arguments: `{"path":"foo.go"}`}},
			}, Usage: &Usage{InputTokens: 10, OutputTokens: 5, ReasoningTokens: 2, CacheReadTokens: 1, CacheWriteTokens: 3}},
		}
		for i, ev := range in {
			if _, err := store.AppendEvent(ctx, "s1", ev); err != nil {
				t.Fatalf("AppendEvent #%d: unexpected error: %v", i, err)
			}
		}

		got, err := store.Events(ctx, "s1", 0)
		if err != nil {
			t.Fatalf("Events: unexpected error: %v", err)
		}
		if len(got) != len(in) {
			t.Fatalf("Events: got %d events, want %d (%+v)", len(got), len(in), got)
		}
		for i := range in {
			want := in[i]
			want.SessionID = "s1"
			want.Seq = Seq(i + 1)
			if !reflect.DeepEqual(got[i], want) {
				t.Fatalf("Events[%d]: got %+v, want %+v", i, got[i], want)
			}
		}
	})

	t.Run("EventsRoundTripsTopLevelText", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		// El contenido del asistente y del razonamiento viaja en el Text top-level
		// del SessionEvent (no en un Message): Reasoning.Ended y Text.Ended lo
		// cargan completo. Events debe devolverlo intacto, o la rehidratacion pierde
		// "lo que penso" y "la respuesta" del agente. Un tool result que ademas trae
		// Text top-level (como el publisher real) debe round-trippear ev.Text Y
		// Message.Text a la vez.
		in := []SessionEvent{
			{Kind: KindReasoningStarted},
			{Kind: KindReasoningDelta, Text: "pien"},
			{Kind: KindReasoningEnded, Text: "pienso, luego existo"},
			{Kind: KindTextStarted},
			{Kind: KindTextDelta, Text: "ho"},
			{Kind: KindTextEnded, Text: "hola mundo"},
			{Kind: KindToolSuccess, CallID: "c1", ToolName: "read", Text: "contenido",
				Message: &Message{ID: "c1", Role: RoleTool, Text: "contenido", ToolCallID: "c1"}},
		}
		for i, ev := range in {
			if _, err := store.AppendEvent(ctx, "s1", ev); err != nil {
				t.Fatalf("AppendEvent #%d: unexpected error: %v", i, err)
			}
		}

		got, err := store.Events(ctx, "s1", 0)
		if err != nil {
			t.Fatalf("Events: unexpected error: %v", err)
		}
		if len(got) != len(in) {
			t.Fatalf("Events: got %d events, want %d (%+v)", len(got), len(in), got)
		}
		for i := range in {
			want := in[i]
			want.SessionID = "s1"
			want.Seq = Seq(i + 1)
			if !reflect.DeepEqual(got[i], want) {
				t.Fatalf("Events[%d]: got %+v, want %+v", i, got[i], want)
			}
		}
	})

	t.Run("EventsRoundTripsDiff", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		// El diff (solo-UI) de edit/write viaja en SessionEvent.Diff. Events debe
		// devolverlo intacto o la rehidratacion pierde el diff coloreado al reabrir
		// una sesion pasada.
		in := SessionEvent{
			Kind: KindToolSuccess, CallID: "c1", ToolName: "edit", Text: "[foo.go#ab12]",
			Diff:    "--- a/foo.go\n+++ b/foo.go\n@@ -1 +1 @@\n-a\n+b\n",
			Message: &Message{ID: "c1", Role: RoleTool, Text: "[foo.go#ab12]", ToolCallID: "c1"},
		}
		if _, err := store.AppendEvent(ctx, "s1", in); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}

		got, err := store.Events(ctx, "s1", 0)
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("Events: got %d, want 1", len(got))
		}
		if got[0].Diff != in.Diff {
			t.Fatalf("Events[0].Diff round-trip:\n got %q\nwant %q", got[0].Diff, in.Diff)
		}
	})

	t.Run("EventsSinceSeqFiltersOlder", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		if _, err := store.AppendEvent(ctx, "s1", SessionEvent{Kind: KindStepStarted}); err != nil {
			t.Fatalf("AppendEvent: unexpected error: %v", err)
		}
		seq2, err := store.AppendEvent(ctx, "s1", SessionEvent{Message: &Message{ID: "u1", Role: RoleUser, Text: "hola"}})
		if err != nil {
			t.Fatalf("AppendEvent: unexpected error: %v", err)
		}
		if _, err := store.AppendEvent(ctx, "s1", SessionEvent{Kind: KindStepEnded}); err != nil {
			t.Fatalf("AppendEvent: unexpected error: %v", err)
		}

		got, err := store.Events(ctx, "s1", seq2)
		if err != nil {
			t.Fatalf("Events: unexpected error: %v", err)
		}
		if len(got) != 1 || got[0].Kind != KindStepEnded || got[0].Seq != 3 {
			t.Fatalf("Events(sinceSeq=%d): got %+v, want only the Step.Ended with Seq 3", seq2, got)
		}
	})

	t.Run("EventsUnknownSessionReturnsNotFound", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		if _, err := store.Events(ctx, "ghost", 0); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("Events(ghost): got %v, want ErrSessionNotFound", err)
		}
	})

	t.Run("DeleteSessionRemovesSessionLeavingOthers", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		appendContractMessage(t, store, "s1", Message{ID: "m1", Role: RoleUser, Text: "hola"})
		appendContractMessage(t, store, "s2", Message{ID: "m2", Role: RoleUser, Text: "otra"})

		if err := store.DeleteSession(ctx, "s1"); err != nil {
			t.Fatalf("DeleteSession(s1): unexpected error: %v", err)
		}

		if _, err := store.LoadSession(ctx, "s1"); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("LoadSession(s1) tras borrar: got %v, want ErrSessionNotFound", err)
		}
		if _, err := store.Events(ctx, "s1", 0); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("Events(s1) tras borrar: got %v, want ErrSessionNotFound", err)
		}

		got, err := store.Sessions(ctx)
		if err != nil {
			t.Fatalf("Sessions: unexpected error: %v", err)
		}
		if len(got) != 1 || got[0].ID != "s2" {
			t.Fatalf("Sessions tras borrar s1: got %+v, want only s2", got)
		}
	})

	t.Run("DeleteUnknownSessionReturnsNotFound", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)

		// Borrar una sesion que no existe (sin eventos) no toca nada: el mismo
		// contrato de no-encontrado que LoadSession/Messages/Events.
		if err := store.DeleteSession(ctx, "ghost"); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("DeleteSession(ghost): got %v, want ErrSessionNotFound", err)
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

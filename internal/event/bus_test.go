package event

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"

	"atenea/internal/llm"
	"atenea/internal/session"
	"atenea/internal/session/runner"
	"atenea/internal/tool"
)

// eventsOn devuelve, en orden de emision, los payloads que son SessionEvent
// emitidos en channel. Filtra el resto (p.ej. strings de error). Concurrency-safe.
func (f *fakeEmit) eventsOn(channel string) []session.SessionEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []session.SessionEvent
	for i, ch := range f.channels {
		if ch != channel {
			continue
		}
		if ev, ok := f.payloads[i].(session.SessionEvent); ok {
			out = append(out, ev)
		}
	}
	return out
}

// errorsOn devuelve, en orden de emision, los payloads string emitidos en
// channel (los mensajes de error que publica PublishError). Concurrency-safe.
func (f *fakeEmit) errorsOn(channel string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for i, ch := range f.channels {
		if ch != channel {
			continue
		}
		if s, ok := f.payloads[i].(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// count devuelve el total de emisiones registradas. Concurrency-safe.
func (f *fakeEmit) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.channels)
}

// TestBus_PublishEmitsOnSessionChannel: el bus reenvia un SessionEvent durable al
// canal session:<id> con el payload intacto.
func TestBus_PublishEmitsOnSessionChannel(t *testing.T) {
	fake := &fakeEmit{}
	bus := NewBus(fake.emit)

	bus.Publish(session.SessionEvent{SessionID: "s1", Seq: 7, Kind: session.KindTextDelta, Text: "hi"})

	evs := fake.eventsOn("session:s1")
	if len(evs) != 1 {
		t.Fatalf("emisiones en session:s1 = %d, quiero 1", len(evs))
	}
	if fake.count() != 1 {
		t.Fatalf("emisiones totales = %d, quiero 1", fake.count())
	}
	ev := evs[0]
	if ev.Seq != 7 {
		t.Errorf("ev.Seq = %d, quiero 7", ev.Seq)
	}
	if ev.Text != "hi" {
		t.Errorf("ev.Text = %q, quiero %q", ev.Text, "hi")
	}
	if ev.Kind != session.KindTextDelta {
		t.Errorf("ev.Kind = %q, quiero %q", ev.Kind, session.KindTextDelta)
	}
}

// TestBus_PublishErrorEmitsOnErrorChannel: un error duro de Run viaja por el canal
// session:<id>:error como string (err.Error()).
func TestBus_PublishErrorEmitsOnErrorChannel(t *testing.T) {
	fake := &fakeEmit{}
	bus := NewBus(fake.emit)

	bus.PublishError("s1", errors.New("boom"))

	errs := fake.errorsOn("session:s1:error")
	if len(errs) != 1 {
		t.Fatalf("emisiones en session:s1:error = %d, quiero 1", len(errs))
	}
	if errs[0] != "boom" {
		t.Errorf("payload = %q, quiero %q", errs[0], "boom")
	}
}

// TestBus_NilEmitIsNoop: un bus sin EmitFunc (nil) es inerte: ni Publish ni
// PublishError deben paniquear.
func TestBus_NilEmitIsNoop(t *testing.T) {
	bus := NewBus(nil)
	bus.Publish(session.SessionEvent{SessionID: "s1", Kind: session.KindStepStarted})
	bus.PublishError("s1", errors.New("x"))
}

// TestEmittingStore_ReadMethodsDelegate: las lecturas del decorador delegan en
// inner y NO emiten al bus. Se siembra inner directo (sin bus) para que el conteo
// de emisiones del decorador sea cero.
func TestEmittingStore_ReadMethodsDelegate(t *testing.T) {
	ctx := context.Background()
	inner := session.NewMemoryStore()

	if _, err := inner.AppendEvent(ctx, "s1", session.SessionEvent{
		Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hola"},
	}); err != nil {
		t.Fatalf("sembrar user: %v", err)
	}
	if _, err := inner.AppendEvent(ctx, "s1", session.SessionEvent{
		Kind: session.KindToolCalled, CallID: "c1", ToolName: "echo",
	}); err != nil {
		t.Fatalf("sembrar tool called: %v", err)
	}

	fake := &fakeEmit{}
	store := NewEmittingStore(inner, NewBus(fake.emit))

	msgs, err := store.Messages(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	foundUser := false
	for _, m := range msgs {
		if m.Role == session.RoleUser && m.Text == "hola" {
			foundUser = true
		}
	}
	if !foundUser {
		t.Errorf("Messages no trajo el user 'hola': %+v", msgs)
	}

	if _, err := store.Epoch(ctx, "s1"); err != nil {
		t.Fatalf("Epoch: %v", err)
	}

	pending, err := store.PendingToolCalls(ctx, "s1")
	if err != nil {
		t.Fatalf("PendingToolCalls: %v", err)
	}
	foundCall := false
	for _, p := range pending {
		if p.CallID == "c1" {
			foundCall = true
		}
	}
	if !foundCall {
		t.Errorf("PendingToolCalls no trajo c1: %+v", pending)
	}

	if _, err := store.LoadSession(ctx, "s1"); err != nil {
		t.Fatalf("LoadSession: %v", err)
	}

	if fake.count() != 0 {
		t.Errorf("las lecturas emitieron %d veces, quiero 0", fake.count())
	}
}

// failingStore implementa session.Store con un AppendEvent que siempre falla; los
// demas metodos devuelven cero/nil y no se llaman en este test.
type failingStore struct{}

func (failingStore) AppendEvent(ctx context.Context, sessionID string, ev session.SessionEvent) (session.Seq, error) {
	return 0, errors.New("store down")
}
func (failingStore) LoadSession(ctx context.Context, sessionID string) (session.Session, error) {
	return session.Session{}, nil
}
func (failingStore) Messages(ctx context.Context, sessionID string, sinceSeq session.Seq) ([]session.Message, error) {
	return nil, nil
}
func (failingStore) Sessions(ctx context.Context) ([]session.SessionSummary, error) {
	return nil, nil
}
func (failingStore) Events(ctx context.Context, sessionID string, sinceSeq session.Seq) ([]session.SessionEvent, error) {
	return nil, nil
}
func (failingStore) Epoch(ctx context.Context, sessionID string) (session.ContextEpoch, error) {
	return session.ContextEpoch{}, nil
}
func (failingStore) PendingToolCalls(ctx context.Context, sessionID string) ([]session.PendingTool, error) {
	return nil, nil
}

// TestEmittingStore_AppendErrorDoesNotEmit: si inner.AppendEvent falla, el
// decorador propaga el error y NO emite al bus.
func TestEmittingStore_AppendErrorDoesNotEmit(t *testing.T) {
	fake := &fakeEmit{}
	store := NewEmittingStore(failingStore{}, NewBus(fake.emit))

	_, err := store.AppendEvent(context.Background(), "s1", session.SessionEvent{Kind: session.KindStepStarted})
	if err == nil {
		t.Fatal("AppendEvent no devolvio error, quiero el error de inner")
	}
	if fake.count() != 0 {
		t.Errorf("emitio %d veces ante append fallido, quiero 0", fake.count())
	}
}

// twoTurnProvider devuelve un guion DISTINTO por turno (el loop llama Stream una
// vez por turno). El 1er turno pide una tool; el 2do responde con texto. Delega
// cada guion en un FakeProvider fresco y cuenta las llamadas bajo mutex.
type twoTurnProvider struct {
	mu    sync.Mutex
	calls int
}

var _ llm.Provider = (*twoTurnProvider)(nil)

func (p *twoTurnProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	n := p.calls
	p.calls++
	p.mu.Unlock()

	var script []llm.Event
	switch n {
	case 0:
		script = []llm.Event{
			{Kind: llm.StepStarted},
			{Kind: llm.ToolCall, CallID: "c1", ToolName: "echo", Input: json.RawMessage(`{"text":"pong"}`)},
			{Kind: llm.StepEnded},
		}
	case 1:
		script = []llm.Event{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: "listo"},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		}
	}
	return llm.NewFakeProvider(script...).Stream(ctx, req)
}

// TestEmittingStore_TurnStreamsEventsInSeqOrder: un Runner REAL end-to-end sobre el
// decorador entrega al bus TODOS los eventos durables del turno, en orden de Seq y
// contiguos desde 1. Como el bus recibe cada AppendEvent exitoso, la contiguidad
// 1..N prueba orden total y completitud. Correr con -race.
func TestEmittingStore_TurnStreamsEventsInSeqOrder(t *testing.T) {
	ctx := context.Background()
	fake := &fakeEmit{}
	bus := NewBus(fake.emit)
	inner := session.NewMemoryStore()
	store := NewEmittingStore(inner, bus)

	inbox := session.NewMemoryInbox()
	if err := inbox.Admit(ctx, "s1", session.Prompt{Text: "hola"}, session.DeliveryQueue); err != nil {
		t.Fatalf("Admit: %v", err)
	}

	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	prov := &twoTurnProvider{}

	n := 0
	idGen := func() string {
		n++
		return "m" + strconv.Itoa(n)
	}
	r := runner.NewRunner(store, inbox, prov, reg, tool.Permissions{"echo": true}, idGen)

	if err := r.Run(ctx, "s1", false); err != nil {
		t.Fatalf("Run: %v", err)
	}

	evs := fake.eventsOn("session:s1")
	if len(evs) == 0 {
		t.Fatal("el bus no recibio ningun evento")
	}

	// Seq estrictamente crecientes y contiguos 1..N (orden total + completitud).
	for i, ev := range evs {
		want := session.Seq(i + 1)
		if ev.Seq != want {
			t.Fatalf("evs[%d].Seq = %d, quiero %d (debe ser contiguo 1..N): %+v", i, ev.Seq, want, evs)
		}
	}

	hasUser := false
	hasToolCalled := false
	hasToolSuccess := false
	hasTextEnded := false
	for _, ev := range evs {
		if ev.Message != nil && ev.Message.Role == session.RoleUser && ev.Message.Text == "hola" {
			hasUser = true
		}
		if ev.Kind == session.KindToolCalled && ev.CallID == "c1" {
			hasToolCalled = true
		}
		if ev.Kind == session.KindToolSuccess {
			if ev.Text == "pong" || (ev.Message != nil && ev.Message.Text == "pong") {
				hasToolSuccess = true
			}
		}
		if ev.Kind == session.KindTextEnded && ev.Text == "listo" {
			hasTextEnded = true
		}
	}
	if !hasUser {
		t.Error("falta el evento con Message.Role==user y Text=='hola'")
	}
	if !hasToolCalled {
		t.Error("falta Tool.Called con CallID 'c1'")
	}
	if !hasToolSuccess {
		t.Error("falta Tool.Success con Text/Message.Text 'pong'")
	}
	if !hasTextEnded {
		t.Error("falta Text.Ended con Text 'listo'")
	}
}

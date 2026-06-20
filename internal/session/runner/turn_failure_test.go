package runner

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"atenea/internal/llm"
	"atenea/internal/session"
	"atenea/internal/tool"
)

type rejectCanceledStore struct {
	*recordingStore
}

var _ session.Store = (*rejectCanceledStore)(nil)

func newRejectCanceledStore() *rejectCanceledStore {
	return &rejectCanceledStore{recordingStore: newRecordingStore()}
}

func (s *rejectCanceledStore) AppendEvent(ctx context.Context, sessionID string, ev session.SessionEvent) (session.Seq, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return s.recordingStore.AppendEvent(ctx, sessionID, ev)
}

type blockingUntilCanceledTool struct {
	started chan struct{}
	once    *sync.Once
}

func (blockingUntilCanceledTool) Name() string {
	return "blocking"
}

func (blockingUntilCanceledTool) Description() string {
	return "Bloquea hasta que el contexto se cancela."
}

func (blockingUntilCanceledTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}

func (t blockingUntilCanceledTool) Execute(ctx context.Context, input json.RawMessage) (tool.Result, error) {
	t.once.Do(func() { close(t.started) })
	<-ctx.Done()
	return tool.Result{}, ctx.Err()
}

// TestRunner_CancelDuringTurnFailsInFlightTool cubre el RED de M8: si se cancela
// el turno mientras una tool local esta en vuelo, el runner debe cerrar el estado
// ambiguo con Tool.Failed + Step.Failed usando escrituras durables desacopladas
// del ctx cancelado, devolver context.Canceled y no dejar Tool.Called colgado.
func TestRunner_CancelDuringTurnFailsInFlightTool(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := newRejectCanceledStore()
	seedUser(t, store, "s1")

	started := make(chan struct{})
	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.ToolCall, CallID: "c1", ToolName: "blocking"},
	)
	reg := tool.NewRegistry(
		tool.NewOutputStore(0),
		blockingUntilCanceledTool{started: started, once: &sync.Once{}},
	)
	r := NewRunner(store, session.NewMemoryInbox(), fake, reg, tool.Permissions{"blocking": true}, func() string { return "a1" })

	type turnResult struct {
		cont bool
		err  error
	}
	done := make(chan turnResult, 1)
	go func() {
		cont, err := r.runTurn(ctx, "s1")
		done <- turnResult{cont: cont, err: err}
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("la tool bloqueante no arranco")
	}

	cancel()

	var res turnResult
	select {
	case res = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runTurn no retorno tras cancelar el contexto")
	}

	if !errors.Is(res.err, context.Canceled) {
		t.Fatalf("runTurn err = %v, quiero context.Canceled", res.err)
	}
	if res.cont {
		t.Errorf("runTurn cont = true, quiero false (turno interrumpido)")
	}

	log := store.snapshot()
	if _, ok := seqOfKind(log, session.KindToolSuccess, "c1"); ok {
		t.Errorf("se persistio Tool.Success de c1, no debe haberlo (la tool fue cancelada)")
	}
	if _, ok := seqOfKind(log, session.KindToolFailed, "c1"); !ok {
		t.Errorf("no se persistio Tool.Failed de c1")
	}
	if _, ok := seqOfKind(log, session.KindStepFailed, ""); !ok {
		t.Errorf("no se persistio Step.Failed")
	}
	if unresolved := unresolvedToolCalls(log); len(unresolved) != 0 {
		t.Errorf("quedaron tool calls sin resolver: %v", unresolved)
	}
}

func TestRunner_ProviderStreamErrorEmitsStepFailed(t *testing.T) {
	ctx := context.Background()
	store := newRecordingStore()
	seedUser(t, store, "s1")

	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.StepFailed, Text: "boom"},
	)
	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	r := NewRunner(store, session.NewMemoryInbox(), fake, reg, tool.Permissions{"echo": true}, func() string { return "a1" })

	cont, err := r.runTurn(ctx, "s1")
	if err == nil {
		t.Fatalf("runTurn err = nil, quiero *ProviderError")
	}
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("runTurn err = %T %v, quiero *ProviderError reconocible con errors.As", err, err)
	}
	if providerErr.Message != "boom" {
		t.Errorf("ProviderError.Message = %q, quiero %q", providerErr.Message, "boom")
	}
	if cont {
		t.Errorf("runTurn cont = true, quiero false (fallo del provider corta el turno)")
	}

	log := store.snapshot()
	var foundStepFailed bool
	for _, ev := range log {
		if ev.Kind == session.KindStepFailed {
			foundStepFailed = true
			if !strings.Contains(ev.Error, "boom") {
				t.Errorf("Step.Failed.Error = %q, quiero que contenga boom", ev.Error)
			}
		}
	}
	if !foundStepFailed {
		t.Fatalf("no se persistio Step.Failed")
	}
	if unresolved := unresolvedToolCalls(log); len(unresolved) != 0 {
		t.Errorf("quedaron tool calls sin resolver: %v", unresolved)
	}
}

func TestRunner_ProviderExecutedToolNeverResolvesIsClosed(t *testing.T) {
	ctx := context.Background()
	store := newRecordingStore()
	seedUser(t, store, "s1")

	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.ToolCall, CallID: "c1", ToolName: "echo", ProviderExecuted: true},
		llm.Event{Kind: llm.StepFailed, Text: "boom"},
	)
	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	r := NewRunner(store, session.NewMemoryInbox(), fake, reg, tool.Permissions{"echo": true}, func() string { return "a1" })

	cont, err := r.runTurn(ctx, "s1")
	if err == nil {
		t.Fatalf("runTurn err = nil, quiero *ProviderError")
	}
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("runTurn err = %T %v, quiero *ProviderError reconocible con errors.As", err, err)
	}
	if providerErr.Message != "boom" {
		t.Errorf("ProviderError.Message = %q, quiero %q", providerErr.Message, "boom")
	}
	if cont {
		t.Errorf("runTurn cont = true, quiero false (fallo del provider corta el turno)")
	}

	log := store.snapshot()
	if _, ok := seqOfKind(log, session.KindToolCalled, "c1"); !ok {
		t.Fatalf("no se persistio Tool.Called de c1")
	}
	if _, ok := seqOfKind(log, session.KindToolFailed, "c1"); !ok {
		t.Fatalf("no se persistio Tool.Failed de c1")
	}
	if unresolved := unresolvedToolCalls(log); len(unresolved) != 0 {
		t.Errorf("quedaron tool calls sin resolver: %v", unresolved)
	}
}

func TestRunner_RunFailsInterruptedToolsBeforeTurn(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	inbox := session.NewMemoryInbox()

	if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{
		Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hola"},
	}); err != nil {
		t.Fatalf("AppendEvent (semilla usuario) error inesperado: %v", err)
	}
	if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{
		Kind: session.KindToolCalled, CallID: "c1", ToolName: "echo",
	}); err != nil {
		t.Fatalf("AppendEvent (tool colgada) error inesperado: %v", err)
	}
	if err := inbox.Admit(ctx, "s1", session.Prompt{Text: "sigue"}, session.DeliveryQueue); err != nil {
		t.Fatalf("Admit (queue) error inesperado: %v", err)
	}

	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "ok"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)
	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	r := NewRunner(store, inbox, fake, reg, tool.Permissions{"echo": true}, idCounter())

	if err := r.Run(ctx, "s1", false); err != nil {
		t.Fatalf("Run error inesperado: %v", err)
	}

	pending, err := store.PendingToolCalls(ctx, "s1")
	if err != nil {
		t.Fatalf("PendingToolCalls error inesperado: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("PendingToolCalls = %+v, quiero vacio", pending)
	}

	msgs, err := store.Messages(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("Messages error inesperado: %v", err)
	}
	if !hasToolMessage(msgs, "c1", interruptedToolMessage) {
		t.Errorf("la proyeccion no contiene Message{ID:c1, Role:tool, Text:%q}; mensajes = %+v", interruptedToolMessage, msgs)
	}
	var foundAssistant bool
	for _, m := range msgs {
		if m.Role == session.RoleAssistant && m.Text == "ok" {
			foundAssistant = true
		}
	}
	if !foundAssistant {
		t.Errorf("el turno nuevo no materializo asistente con Text ok; mensajes = %+v", msgs)
	}
}

func unresolvedToolCalls(log []session.SessionEvent) []string {
	open := make(map[string]bool)
	order := make([]string, 0)
	for _, ev := range log {
		switch ev.Kind {
		case session.KindToolCalled:
			if !open[ev.CallID] {
				open[ev.CallID] = true
				order = append(order, ev.CallID)
			}
		case session.KindToolSuccess, session.KindToolFailed:
			delete(open, ev.CallID)
		}
	}

	unresolved := make([]string, 0, len(open))
	for _, callID := range order {
		if open[callID] {
			unresolved = append(unresolved, callID)
		}
	}
	return unresolved
}

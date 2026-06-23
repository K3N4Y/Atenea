package runner

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"

	"atenea/internal/llm"
	"atenea/internal/session"
	"atenea/internal/tool"
)

// TestRunner_TextOnlyTurnPersistsEventsAndStops es el RED de M5: un turno de solo
// texto persiste sus eventos (con el Message coalescido del asistente) y devuelve
// needsContinuation == false. Siembra un mensaje de usuario, guiona un
// FakeProvider con StepStarted, un bloque de texto en deltas y StepEnded, y afirma
// que runTurn no continua y que la proyeccion queda con el usuario y el asistente
// coalescido. Referencia NewRunner y el metodo runTurn, que aun no existen: el
// paquete de test no compila y esa es la falla RED honesta en Go.
func TestRunner_TextOnlyTurnPersistsEventsAndStops(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()

	// Semilla: un mensaje de usuario en la sesion "s1".
	if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{
		Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hola"},
	}); err != nil {
		t.Fatalf("AppendEvent (semilla usuario) error inesperado: %v", err)
	}

	// Guion de solo texto: el turno coalesce "hola " + "mundo" -> "hola mundo".
	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "hola "},
		llm.Event{Kind: llm.TextDelta, Text: "mundo"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)

	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	r := NewRunner(store, session.NewMemoryInbox(), fake, reg, tool.Permissions{"echo": true}, func() string { return "a1" })

	cont, err := r.runTurn(ctx, "s1")
	if err != nil {
		t.Fatalf("runTurn error inesperado: %v", err)
	}
	// Un turno de solo texto no continua: no hubo tool call local.
	if cont {
		t.Errorf("runTurn cont = true, quiero false (turno de solo texto no continua)")
	}

	msgs, err := store.Messages(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("Messages error inesperado: %v", err)
	}
	// La proyeccion queda con el usuario sembrado y el asistente coalescido.
	if len(msgs) != 2 {
		t.Fatalf("mensajes proyectados = %d, quiero 2 (usuario + asistente)", len(msgs))
	}

	asst := msgs[1]
	if asst.ID != "a1" {
		t.Errorf("asistente.ID = %q, quiero %q", asst.ID, "a1")
	}
	if asst.Role != session.RoleAssistant {
		t.Errorf("asistente.Role = %q, quiero %q", asst.Role, session.RoleAssistant)
	}
	if asst.Text != "hola mundo" {
		t.Errorf("asistente.Text = %q, quiero %q (coalescido)", asst.Text, "hola mundo")
	}
}

// recordingStore embebe el MemoryStore real y ademas captura cada SessionEvent
// apendido (con su Seq) en un slice protegido por mutex. El MemoryStore no expone
// el log crudo, asi que runTurn lee Messages del embebido mientras el test afirma
// el ORDEN del log capturado (incluidos eventos sin Message como Tool.Called).
type recordingStore struct {
	*session.MemoryStore

	mu  sync.Mutex
	log []session.SessionEvent
}

// var _ session.Store = (*recordingStore)(nil) asegura que el recordingStore
// sigue cumpliendo la interface Store que runTurn espera.
var _ session.Store = (*recordingStore)(nil)

func newRecordingStore() *recordingStore {
	return &recordingStore{MemoryStore: session.NewMemoryStore()}
}

// seedUser siembra un mensaje de usuario "hola" en la sesion, asi runTurn tiene un
// historial que leer (Messages devuelve ErrSessionNotFound si la sesion no existe).
func seedUser(t *testing.T, store session.Store, sessionID string) {
	t.Helper()
	if _, err := store.AppendEvent(context.Background(), sessionID, session.SessionEvent{
		Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hola"},
	}); err != nil {
		t.Fatalf("AppendEvent (semilla usuario) error inesperado: %v", err)
	}
}

// AppendEvent delega en el MemoryStore embebido (que asigna SessionID/Seq) y
// registra el evento ya sellado en el log capturado.
func (s *recordingStore) AppendEvent(ctx context.Context, sessionID string, ev session.SessionEvent) (session.Seq, error) {
	seq, err := s.MemoryStore.AppendEvent(ctx, sessionID, ev)
	if err != nil {
		return seq, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ev.SessionID = sessionID
	ev.Seq = seq
	s.log = append(s.log, ev)
	return seq, nil
}

// snapshot devuelve una copia del log capturado, segura para leer tras runTurn.
func (s *recordingStore) snapshot() []session.SessionEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]session.SessionEvent, len(s.log))
	copy(out, s.log)
	return out
}

// seqOfKind devuelve el Seq del primer evento del log con ese Kind y CallID, y si
// lo encontro. Ayuda a afirmar el orden relativo de dos eventos en el log.
func seqOfKind(log []session.SessionEvent, kind session.EventKind, callID string) (session.Seq, bool) {
	for _, ev := range log {
		if ev.Kind == kind && ev.CallID == callID {
			return ev.Seq, true
		}
	}
	return 0, false
}

// TestRunner_LocalToolCallRegistersCalledThenSettlesSuccess afirma el contrato de
// una tool call local: el Tool.Called se persiste ANTES de ejecutar, al asentar se
// publica Tool.Success con el output, runTurn continua (cont == true) y la
// proyeccion gana un Message{Role: tool} con el resultado para el siguiente turno.
func TestRunner_LocalToolCallRegistersCalledThenSettlesSuccess(t *testing.T) {
	ctx := context.Background()
	store := newRecordingStore()
	seedUser(t, store, "s1")

	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.ToolCall, CallID: "c1", ToolName: "echo", Input: json.RawMessage(`{"text":"pong"}`)},
		llm.Event{Kind: llm.StepEnded},
	)
	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	r := NewRunner(store, session.NewMemoryInbox(), fake, reg, tool.Permissions{"echo": true}, func() string { return "a1" })

	cont, err := r.runTurn(ctx, "s1")
	if err != nil {
		t.Fatalf("runTurn error inesperado: %v", err)
	}
	if !cont {
		t.Errorf("runTurn cont = false, quiero true (hubo una tool call local)")
	}

	log := store.snapshot()
	calledSeq, okCalled := seqOfKind(log, session.KindToolCalled, "c1")
	if !okCalled {
		t.Fatalf("no se persistio Tool.Called de c1")
	}
	successSeq, okSuccess := seqOfKind(log, session.KindToolSuccess, "c1")
	if !okSuccess {
		t.Fatalf("no se persistio Tool.Success de c1")
	}
	// Tool.Called se registra antes del efecto: su Seq es menor que el del Success.
	if !(calledSeq < successSeq) {
		t.Errorf("Tool.Called (Seq %d) no aparece antes de Tool.Success (Seq %d)", calledSeq, successSeq)
	}

	// El Tool.Success lleva el output de la tool.
	for _, ev := range log {
		if ev.Kind == session.KindToolSuccess && ev.CallID == "c1" {
			if ev.Text != "pong" {
				t.Errorf("Tool.Success.Text = %q, quiero %q", ev.Text, "pong")
			}
		}
	}

	// La proyeccion incluye el Message{ID:"c1", Role: tool, Text:"pong"}.
	msgs, err := store.Messages(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("Messages error inesperado: %v", err)
	}
	if !hasToolMessage(msgs, "c1", "pong") {
		t.Errorf("la proyeccion no contiene Message{ID:c1, Role:tool, Text:pong}; mensajes = %+v", msgs)
	}
}

// hasToolMessage afirma que la proyeccion contiene un mensaje Role:tool con el ID
// y el texto dados.
func hasToolMessage(msgs []session.Message, id, text string) bool {
	for _, m := range msgs {
		if m.Role == session.RoleTool && m.ID == id && m.Text == text {
			return true
		}
	}
	return false
}

// barrierTool es una tool de rendezvous: su Execute hace wg.Done() y luego
// wg.Wait(), asi que solo retorna cuando TODAS las instancias esperadas estan
// dentro a la vez. Si las tools corrieran en serie, la primera bloquearia para
// siempre: que el turno complete prueba que corren concurrentemente.
type barrierTool struct {
	wg *sync.WaitGroup
}

func (barrierTool) Name() string            { return "barrier" }
func (barrierTool) Description() string     { return "Tool de rendezvous para probar concurrencia." }
func (barrierTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (b barrierTool) Execute(ctx context.Context, input json.RawMessage) (tool.Result, error) {
	b.wg.Done()
	b.wg.Wait()
	return tool.Result{Output: b.Name()}, nil
}

// TestRunner_TwoToolCallsSettleConcurrentlyAndTurnWaits guiona dos tool calls a un
// barrierTool con WaitGroup(2): solo terminan si corren a la vez. Afirma que
// runTurn continua (cont == true) y que AMBOS Tool.Success quedaron persistidos al
// retornar (el turno espera a las dos). Corre con -race: ejerce el candado del
// Publisher y la concurrencia real de las goroutines de settle.
func TestRunner_TwoToolCallsSettleConcurrentlyAndTurnWaits(t *testing.T) {
	ctx := context.Background()
	store := newRecordingStore()
	seedUser(t, store, "s1")

	var wg sync.WaitGroup
	wg.Add(2)

	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.ToolCall, CallID: "c1", ToolName: "barrier"},
		llm.Event{Kind: llm.ToolCall, CallID: "c2", ToolName: "barrier"},
		llm.Event{Kind: llm.StepEnded},
	)
	reg := tool.NewRegistry(tool.NewOutputStore(0), barrierTool{wg: &wg})
	r := NewRunner(store, session.NewMemoryInbox(), fake, reg, tool.Permissions{"barrier": true}, func() string { return "a1" })

	cont, err := r.runTurn(ctx, "s1")
	if err != nil {
		t.Fatalf("runTurn error inesperado: %v", err)
	}
	if !cont {
		t.Errorf("runTurn cont = false, quiero true (hubo tool calls locales)")
	}

	log := store.snapshot()
	if _, ok := seqOfKind(log, session.KindToolSuccess, "c1"); !ok {
		t.Errorf("no se persistio Tool.Success de c1 al retornar runTurn")
	}
	if _, ok := seqOfKind(log, session.KindToolSuccess, "c2"); !ok {
		t.Errorf("no se persistio Tool.Success de c2 al retornar runTurn")
	}
}

// TestRunner_LocalToolFailureRecordsToolFailed guiona una tool call con input
// invalido (Echo.Execute falla al parsear). El fallo es in-band: afirma cont ==
// true, err == nil; que se persistio un Tool.Failed con Error no vacio y NO un
// Tool.Success; y que la proyeccion tiene un Message{Role: tool} cuyo Text es el
// mensaje de error (igual al Error del evento).
func TestRunner_LocalToolFailureRecordsToolFailed(t *testing.T) {
	ctx := context.Background()
	store := newRecordingStore()
	seedUser(t, store, "s1")

	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.ToolCall, CallID: "c1", ToolName: "echo", Input: json.RawMessage(`{`)},
		llm.Event{Kind: llm.StepEnded},
	)
	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	r := NewRunner(store, session.NewMemoryInbox(), fake, reg, tool.Permissions{"echo": true}, func() string { return "a1" })

	cont, err := r.runTurn(ctx, "s1")
	if err != nil {
		t.Fatalf("runTurn error inesperado: %v (el fallo de tool es in-band)", err)
	}
	if !cont {
		t.Errorf("runTurn cont = false, quiero true (hubo una tool call local)")
	}

	log := store.snapshot()
	if _, ok := seqOfKind(log, session.KindToolSuccess, "c1"); ok {
		t.Errorf("se persistio un Tool.Success de c1, no debe haberlo (la tool fallo)")
	}

	var failMsg string
	var foundFailed bool
	for _, ev := range log {
		if ev.Kind == session.KindToolFailed && ev.CallID == "c1" {
			foundFailed = true
			failMsg = ev.Error
		}
	}
	if !foundFailed {
		t.Fatalf("no se persistio un Tool.Failed de c1")
	}
	if failMsg == "" {
		t.Errorf("Tool.Failed.Error vacio, quiero el mensaje del fallo")
	}

	// La proyeccion tiene un Message{Role: tool} con el texto del error.
	msgs, err := store.Messages(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("Messages error inesperado: %v", err)
	}
	if !hasToolMessage(msgs, "c1", failMsg) {
		t.Errorf("la proyeccion no contiene Message{Role:tool} con el texto del error %q; mensajes = %+v", failMsg, msgs)
	}
}

// recordingProvider captura el llm.Request recibido y delega en un FakeProvider
// embebido (el fake ignora el Request, asi que asi se verifica "construir el
// request desde el historial" sin tocar el fake de M2).
type recordingProvider struct {
	*llm.FakeProvider

	mu  sync.Mutex
	req llm.Request
}

func (p *recordingProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	p.req = req
	p.mu.Unlock()
	return p.FakeProvider.Stream(ctx, req)
}

func (p *recordingProvider) captured() llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.req
}

// TestRunner_BuildsRequestFromHistoryAndMaterializedTools afirma que runTurn arma
// el Request desde el historial proyectado (un mensaje de usuario) y las tools
// materializadas (la def de echo), capturando el Request con un recordingProvider.
func TestRunner_BuildsRequestFromHistoryAndMaterializedTools(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()

	if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{
		Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hola"},
	}); err != nil {
		t.Fatalf("AppendEvent (semilla usuario) error inesperado: %v", err)
	}

	prov := &recordingProvider{FakeProvider: llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.StepEnded},
	)}
	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	r := NewRunner(store, session.NewMemoryInbox(), prov, reg, tool.Permissions{"echo": true}, func() string { return "a1" })

	if _, err := r.runTurn(ctx, "s1"); err != nil {
		t.Fatalf("runTurn error inesperado: %v", err)
	}

	req := prov.captured()
	// El Request refleja el historial: al menos un mensaje user con Text "hola".
	var foundUser bool
	for _, m := range req.Messages {
		if m.Role == string(session.RoleUser) && m.Text == "hola" {
			foundUser = true
		}
	}
	if !foundUser {
		t.Errorf("Request.Messages no refleja el historial (user 'hola'); messages = %+v", req.Messages)
	}

	// El Request lleva la def materializada de echo.
	var foundEcho bool
	for _, d := range req.Tools {
		if d.Name == "echo" {
			foundEcho = true
		}
	}
	if !foundEcho {
		t.Errorf("Request.Tools no contiene la def de echo; tools = %+v", req.Tools)
	}
}

// TestRunner_InjectsSystemPromptFromBuilder asserts that, with a system prompt
// builder injected via SetSystemPrompt, runTurn populates Request.System with its
// output and passes the epoch's model (so internal/session/prompt can pick the
// base prompt by family). Without it the system prompt never reaches the provider.
func TestRunner_InjectsSystemPromptFromBuilder(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{
		Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hello"},
	}); err != nil {
		t.Fatalf("AppendEvent (user seed) unexpected error: %v", err)
	}

	prov := &recordingProvider{FakeProvider: llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.StepEnded},
	)}
	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	r := NewRunner(store, session.NewMemoryInbox(), prov, reg, tool.Permissions{"echo": true}, func() string { return "a1" })

	var gotModel string
	r.SetSystemPrompt(func(model string) string {
		gotModel = model
		return "SYS[" + model + "]"
	})

	if _, err := r.runTurn(ctx, "s1"); err != nil {
		t.Fatalf("runTurn unexpected error: %v", err)
	}

	ep, err := store.Epoch(ctx, "s1")
	if err != nil {
		t.Fatalf("Epoch unexpected error: %v", err)
	}
	if gotModel != ep.Model {
		t.Errorf("builder received model %q; want the epoch model %q", gotModel, ep.Model)
	}
	if got, want := prov.captured().System, "SYS["+ep.Model+"]"; got != want {
		t.Errorf("Request.System: got %q, want %q", got, want)
	}
}

// TestRunner_NoSystemBuilderLeavesSystemEmpty is the edge case: without a builder
// the Request leaves System empty (the default does not invent a baseline prompt).
func TestRunner_NoSystemBuilderLeavesSystemEmpty(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{
		Message: &session.Message{ID: "u1", Role: session.RoleUser, Text: "hello"},
	}); err != nil {
		t.Fatalf("AppendEvent (user seed) unexpected error: %v", err)
	}

	prov := &recordingProvider{FakeProvider: llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.StepEnded},
	)}
	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	r := NewRunner(store, session.NewMemoryInbox(), prov, reg, tool.Permissions{"echo": true}, func() string { return "a1" })

	if _, err := r.runTurn(ctx, "s1"); err != nil {
		t.Fatalf("runTurn unexpected error: %v", err)
	}

	if got := prov.captured().System; got != "" {
		t.Errorf("without a builder Request.System must be empty; got %q", got)
	}
}

// countingTool incrementa un contador en cada Execute: prueba que una tool
// provider-executed NO se asienta localmente (su Execute no corre).
type countingTool struct {
	calls *int
}

func (countingTool) Name() string            { return "counter" }
func (countingTool) Description() string     { return "Cuenta cuantas veces se ejecuto." }
func (countingTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (c countingTool) Execute(ctx context.Context, input json.RawMessage) (tool.Result, error) {
	*c.calls++
	return tool.Result{Output: "counted"}, nil
}

// TestRunner_ProviderExecutedToolIsOnlyPersisted guiona una tool call marcada
// ProviderExecuted: afirma que el contador de Execute quedo en 0 (no se asento
// localmente), que el Tool.Called se persistio, y que cont == false (no hubo tool
// LOCAL que continue el loop).
func TestRunner_ProviderExecutedToolIsOnlyPersisted(t *testing.T) {
	ctx := context.Background()
	store := newRecordingStore()
	seedUser(t, store, "s1")

	var calls int
	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.ToolCall, CallID: "c1", ToolName: "counter", ProviderExecuted: true},
		llm.Event{Kind: llm.StepEnded},
	)
	reg := tool.NewRegistry(tool.NewOutputStore(0), countingTool{calls: &calls})
	r := NewRunner(store, session.NewMemoryInbox(), fake, reg, tool.Permissions{"counter": true}, func() string { return "a1" })

	cont, err := r.runTurn(ctx, "s1")
	if err != nil {
		t.Fatalf("runTurn error inesperado: %v", err)
	}
	if calls != 0 {
		t.Errorf("Execute corrio %d veces, quiero 0 (provider-executed no se asienta localmente)", calls)
	}
	if _, ok := seqOfKind(store.snapshot(), session.KindToolCalled, "c1"); !ok {
		t.Errorf("no se persistio Tool.Called de c1 (provider-executed igual se persiste)")
	}
	if cont {
		t.Errorf("runTurn cont = true, quiero false (no hubo tool call local)")
	}
}

// TestToLLMMessages_CarriesToolCallsAndToolCallID fija que la proyeccion
// session.Message -> llm.Message propaga las partes ricas: el assistant con tool
// calls cruza con sus ToolCalls mapeadas a []llm.ToolCallPart (Arguments como
// json.RawMessage), y el resultado de la tool cruza con su ToolCallID. Hoy
// toLLMMessages solo copia Role/Text, asi que el emparejamiento que el proveedor
// necesita se pierde al armar el Request.
func TestToLLMMessages_CarriesToolCallsAndToolCallID(t *testing.T) {
	msgs := []session.Message{
		{Role: session.RoleAssistant, Text: "voy a leer", ToolCalls: []session.ToolCall{{ID: "call_1", Name: "read", Arguments: `{"path":"foo.go"}`}}},
		{Role: session.RoleTool, Text: "contenido", ToolCallID: "call_1"},
	}
	got := toLLMMessages(msgs)

	if len(got) != 2 {
		t.Fatalf("toLLMMessages devolvio %d mensajes, quiero 2", len(got))
	}

	asst := got[0]
	if asst.Role != "assistant" {
		t.Errorf("got[0].Role = %q, quiero %q", asst.Role, "assistant")
	}
	if asst.Text != "voy a leer" {
		t.Errorf("got[0].Text = %q, quiero %q", asst.Text, "voy a leer")
	}
	if len(asst.ToolCalls) != 1 {
		t.Fatalf("got[0].ToolCalls = %+v, quiero 1 tool call", asst.ToolCalls)
	}
	wantPart := llm.ToolCallPart{ID: "call_1", Name: "read", Arguments: json.RawMessage(`{"path":"foo.go"}`)}
	if !reflect.DeepEqual(asst.ToolCalls[0], wantPart) {
		t.Errorf("got[0].ToolCalls[0] = %+v, quiero %+v", asst.ToolCalls[0], wantPart)
	}

	tool := got[1]
	if tool.Role != "tool" {
		t.Errorf("got[1].Role = %q, quiero %q", tool.Role, "tool")
	}
	if tool.Text != "contenido" {
		t.Errorf("got[1].Text = %q, quiero %q", tool.Text, "contenido")
	}
	if tool.ToolCallID != "call_1" {
		t.Errorf("got[1].ToolCallID = %q, quiero %q", tool.ToolCallID, "call_1")
	}
}

package runner

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool"
)

// idCounter devuelve un generador de IDs incremental ("m1","m2",...): el runner lo
// usa para el ID del mensaje de usuario promovido y para el del asistente, asi no
// colisionan y la proyeccion queda legible. Usa strconv.Itoa para soportar mas de
// nueve IDs (el caso de step-limit corre ~26 promociones/asentamientos).
func idCounter() func() string {
	n := 0
	return func() string {
		n++
		return "m" + strconv.Itoa(n)
	}
}

// scriptedProvider devuelve un guion DISTINTO por turno: el loop llama Stream una
// vez por turno y el FakeProvider de M2 replayea SIEMPRE el mismo guion, asi que
// para encadenar turnos con respuestas distintas (tool en el turno 1, texto en el
// turno 2) hace falta un proveedor que cambie de guion turno a turno. El i-esimo
// Stream reproduce turns[i] (o un guion vacio si se agotaron) delegando en un
// FakeProvider fresco, y cuenta las llamadas.
type scriptedProvider struct {
	turns    [][]llm.Event
	requests []llm.Request
	calls    int
	mu       sync.Mutex
}

// var _ llm.Provider = (*scriptedProvider)(nil) asegura que cumple la interface.
var _ llm.Provider = (*scriptedProvider)(nil)

func (p *scriptedProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	var script []llm.Event
	if p.calls < len(p.turns) {
		script = p.turns[p.calls]
	}
	p.calls++
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	return llm.NewFakeProvider(script...).Stream(ctx, req)
}

func (p *scriptedProvider) capturedRequests() []llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.Request(nil), p.requests...)
}

func TestRunner_ProvidesStableOpaqueSessionKey(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	inbox := session.NewMemoryInbox()
	provider := &scriptedProvider{turns: [][]llm.Event{
		{{Kind: llm.StepStarted}, {Kind: llm.ToolCall, CallID: "call-1", ToolName: "echo", Input: json.RawMessage(`{"text":"continue"}`)}, {Kind: llm.StepEnded}},
		{{Kind: llm.StepEnded}},
		{{Kind: llm.StepEnded}},
	}}
	runner := NewRunner(store, inbox, provider, tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{}), tool.Permissions{"echo": true}, idCounter())

	for _, sessionID := range []string{"session-alpha", "session-beta"} {
		if err := inbox.Admit(ctx, sessionID, session.Prompt{Text: "continue"}, session.DeliveryQueue); err != nil {
			t.Fatal(err)
		}
		if err := runner.Run(ctx, sessionID, false, 0); err != nil {
			t.Fatal(err)
		}
	}

	requests := provider.capturedRequests()
	if len(requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(requests))
	}
	if requests[0].SessionKey == "" || requests[0].SessionKey == "session-alpha" {
		t.Fatalf("first SessionKey = %q, want non-empty opaque identity", requests[0].SessionKey)
	}
	if requests[1].SessionKey != requests[0].SessionKey {
		t.Fatalf("same session keys differ: %q != %q", requests[1].SessionKey, requests[0].SessionKey)
	}
	if requests[2].SessionKey == requests[0].SessionKey {
		t.Fatalf("different sessions share SessionKey %q", requests[0].SessionKey)
	}
}

func (p *scriptedProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// steeringProvider envuelve un Provider y, en su PRIMER Stream (sync.Once), admite
// un steer en el inbox antes de delegar. Simula "steer admitido durante la
// corrida": el turno corre y, al terminar, el loop encuentra el steer pendiente y
// fuerza una continuacion que lo materializa.
type steeringProvider struct {
	inner     llm.Provider
	inbox     session.Inbox
	sessionID string
	steer     session.Prompt
	once      sync.Once
}

// var _ llm.Provider = (*steeringProvider)(nil) asegura que cumple la interface.
var _ llm.Provider = (*steeringProvider)(nil)

func (p *steeringProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	p.once.Do(func() {
		_ = p.inbox.Admit(ctx, p.sessionID, p.steer, session.DeliverySteer)
	})
	return p.inner.Stream(ctx, req)
}

// TestRunner_RunProcessesQueuedPromptThenIdle es el RED de M6: tras admitir un
// prompt en el inbox como DeliveryQueue y correr el loop externo Run, el loop
// promueve el prompt a un Message{Role: user}, ejecuta UN turno de solo texto y
// deja la sesion idle. La proyeccion queda con el usuario y el asistente.
//
// El test referencia simbolos que aun NO existen: la firma nueva de NewRunner (con
// inbox como segundo argumento), session.NewMemoryInbox, session.Prompt,
// session.DeliveryQueue y el metodo Run. En Go eso es un fallo de compilacion del
// paquete de test, y ese es el RED honesto de este milestone (igual que M5 y los
// specs previos): el fallo se demuestra corriendo este test nuevo.
func TestRunner_RunProcessesQueuedPromptThenIdle(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	inbox := session.NewMemoryInbox()

	// Se admite un prompt en cola: aun no es un mensaje del historial; el loop lo
	// promueve a Message{Role: user} antes del turno.
	if err := inbox.Admit(ctx, "s1", session.Prompt{Text: "hola"}, session.DeliveryQueue); err != nil {
		t.Fatalf("Admit (queue) error inesperado: %v", err)
	}

	// Guion de SOLO TEXTO: un turno que coalesce "ok" y no continua (no hay tool).
	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "ok"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)

	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	r := NewRunner(store, inbox, fake, reg, tool.Permissions{"echo": true}, idCounter())

	// La sesion no esta idle (hay un queue pendiente): Run lo drena en una actividad.
	if err := r.Run(ctx, "s1", false, 0); err != nil {
		t.Fatalf("Run error inesperado: %v", err)
	}

	msgs, err := store.Messages(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("Messages error inesperado: %v", err)
	}
	// La proyeccion queda con el usuario promovido y el asistente del turno.
	if len(msgs) != 2 {
		t.Fatalf("mensajes proyectados = %d, quiero 2 (usuario + asistente)", len(msgs))
	}

	user := msgs[0]
	if user.Role != session.RoleUser {
		t.Errorf("usuario.Role = %q, quiero %q", user.Role, session.RoleUser)
	}
	if user.Text != "hola" {
		t.Errorf("usuario.Text = %q, quiero %q (queue promovido)", user.Text, "hola")
	}

	asst := msgs[1]
	if asst.Role != session.RoleAssistant {
		t.Errorf("asistente.Role = %q, quiero %q", asst.Role, session.RoleAssistant)
	}
	if asst.Text != "ok" {
		t.Errorf("asistente.Text = %q, quiero %q", asst.Text, "ok")
	}
}

// TestRunner_RunContinuesWhileToolCalls afirma que un turno con tool call local
// encadena otro turno: el primer turno pide una echo (needsContinuation == true) y
// el loop corre un segundo turno (con el resultado de la tool en el historial) que
// es de solo texto y cierra la actividad. Verifica que corrieron DOS turnos y que
// la proyeccion tiene el Message{Role: tool} de la echo y el texto final.
func TestRunner_RunContinuesWhileToolCalls(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	inbox := session.NewMemoryInbox()

	if err := inbox.Admit(ctx, "s1", session.Prompt{Text: "hola"}, session.DeliveryQueue); err != nil {
		t.Fatalf("Admit (queue) error inesperado: %v", err)
	}

	// Turno 1: una tool call local a echo. Turno 2: solo texto que cierra.
	prov := &scriptedProvider{turns: [][]llm.Event{
		{
			{Kind: llm.StepStarted},
			{Kind: llm.ToolCall, CallID: "c1", ToolName: "echo", Input: json.RawMessage(`{"text":"pong"}`)},
			{Kind: llm.StepEnded},
		},
		{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: "listo"},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		},
	}}

	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	r := NewRunner(store, inbox, prov, reg, tool.Permissions{"echo": true}, idCounter())

	if err := r.Run(ctx, "s1", false, 0); err != nil {
		t.Fatalf("Run error inesperado: %v", err)
	}

	// Continuo por la tool del turno 1 y paro al quedar estable en el turno 2.
	if prov.callCount() != 2 {
		t.Errorf("turnos corridos = %d, quiero 2 (continuo por la tool y paro al estabilizar)", prov.callCount())
	}

	msgs, err := store.Messages(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("Messages error inesperado: %v", err)
	}
	if !hasToolMessage(msgs, "c1", "pong") {
		t.Errorf("la proyeccion no contiene Message{ID:c1, Role:tool, Text:pong}; mensajes = %+v", msgs)
	}
	var foundFinal bool
	for _, m := range msgs {
		if m.Role == session.RoleAssistant && m.Text == "listo" {
			foundFinal = true
		}
	}
	if !foundFinal {
		t.Errorf("la proyeccion no contiene el asistente final con Text 'listo'; mensajes = %+v", msgs)
	}
}

// TestRunner_RunAssistantTextDoesNotContinueAlone aisla la regla central del loop:
// un turno de solo texto, sin steer pendiente, NO encadena otro turno. Complemento
// del caso de tools: verifica que corrio UN solo turno y que Run cerro la actividad.
func TestRunner_RunAssistantTextDoesNotContinueAlone(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	inbox := session.NewMemoryInbox()

	if err := inbox.Admit(ctx, "s1", session.Prompt{Text: "hola"}, session.DeliveryQueue); err != nil {
		t.Fatalf("Admit (queue) error inesperado: %v", err)
	}

	// Varios guiones de SOLO TEXTO: si el loop encadenara por texto, correria mas
	// de un turno; el contrato dice que no.
	textTurn := []llm.Event{
		{Kind: llm.StepStarted},
		{Kind: llm.TextStarted},
		{Kind: llm.TextDelta, Text: "ok"},
		{Kind: llm.TextEnded},
		{Kind: llm.StepEnded},
	}
	prov := &scriptedProvider{turns: [][]llm.Event{textTurn, textTurn, textTurn}}

	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	r := NewRunner(store, inbox, prov, reg, tool.Permissions{"echo": true}, idCounter())

	if err := r.Run(ctx, "s1", false, 0); err != nil {
		t.Fatalf("Run error inesperado: %v", err)
	}

	if prov.callCount() != 1 {
		t.Errorf("turnos corridos = %d, quiero 1 (el texto del asistente no encadena)", prov.callCount())
	}
}

// TestRunner_RunSteerAdmittedDuringRunEntersNextContinuation afirma que un steer
// admitido mientras el turno corre fuerza una continuacion y se materializa como
// Message{Role: user} en el siguiente paso, aunque el turno 1 sea de solo texto. El
// steeringProvider admite "sigue" en su primer Stream; el scriptedProvider interno
// de dos turnos de solo texto verifica que el loop corre el segundo turno solo por
// el steer (el texto no lo habria encadenado).
func TestRunner_RunSteerAdmittedDuringRunEntersNextContinuation(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	inbox := session.NewMemoryInbox()

	if err := inbox.Admit(ctx, "s1", session.Prompt{Text: "hola"}, session.DeliveryQueue); err != nil {
		t.Fatalf("Admit (queue) error inesperado: %v", err)
	}

	// Dos turnos de SOLO TEXTO: sin el steer, el turno 1 cerraria la actividad.
	inner := &scriptedProvider{turns: [][]llm.Event{
		{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: "uno"},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		},
		{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: "dos"},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		},
	}}
	prov := &steeringProvider{
		inner:     inner,
		inbox:     inbox,
		sessionID: "s1",
		steer:     session.Prompt{Text: "sigue"},
	}

	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	r := NewRunner(store, inbox, prov, reg, tool.Permissions{"echo": true}, idCounter())

	if err := r.Run(ctx, "s1", false, 0); err != nil {
		t.Fatalf("Run error inesperado: %v", err)
	}

	// Corrieron dos turnos: el steer admitido en el primero fuerza el segundo.
	if inner.callCount() != 2 {
		t.Errorf("turnos corridos = %d, quiero 2 (el steer fuerza la continuacion)", inner.callCount())
	}

	msgs, err := store.Messages(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("Messages error inesperado: %v", err)
	}
	var foundSteer bool
	for _, m := range msgs {
		if m.Role == session.RoleUser && m.Text == "sigue" {
			foundSteer = true
		}
	}
	if !foundSteer {
		t.Errorf("la proyeccion no contiene Message{Role:user, Text:sigue} (el steer no se materializo); mensajes = %+v", msgs)
	}
}

// TestRunner_RunSecondQueueOpensNewActivity afirma que con dos prompts en queue,
// Run procesa el primero (una actividad), lo cierra, detecta el segundo y abre una
// actividad nueva. La proyeccion queda en orden FIFO: user p1, asistente, user p2,
// asistente. Usa el FakeProvider replayable de solo texto (cada turno responde "ok").
func TestRunner_RunSecondQueueOpensNewActivity(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	inbox := session.NewMemoryInbox()

	if err := inbox.Admit(ctx, "s1", session.Prompt{Text: "p1"}, session.DeliveryQueue); err != nil {
		t.Fatalf("Admit (queue p1) error inesperado: %v", err)
	}
	if err := inbox.Admit(ctx, "s1", session.Prompt{Text: "p2"}, session.DeliveryQueue); err != nil {
		t.Fatalf("Admit (queue p2) error inesperado: %v", err)
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

	if err := r.Run(ctx, "s1", false, 0); err != nil {
		t.Fatalf("Run error inesperado: %v", err)
	}

	msgs, err := store.Messages(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("Messages error inesperado: %v", err)
	}
	// Dos actividades en orden FIFO: user p1, asistente, user p2, asistente.
	if len(msgs) != 4 {
		t.Fatalf("mensajes proyectados = %d, quiero 4 (dos pares usuario/asistente); mensajes = %+v", len(msgs), msgs)
	}
	want := []struct {
		role session.Role
		text string
	}{
		{session.RoleUser, "p1"},
		{session.RoleAssistant, "ok"},
		{session.RoleUser, "p2"},
		{session.RoleAssistant, "ok"},
	}
	for i, w := range want {
		if msgs[i].Role != w.role || msgs[i].Text != w.text {
			t.Errorf("msgs[%d] = {Role:%q Text:%q}, quiero {Role:%q Text:%q}", i, msgs[i].Role, msgs[i].Text, w.role, w.text)
		}
	}
}

func TestRunner_FinitePolicyUsesLastTurnForToolFreeSummary(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	inbox := session.NewMemoryInbox()

	if err := inbox.Admit(ctx, "s1", session.Prompt{Text: "loop"}, session.DeliveryQueue); err != nil {
		t.Fatalf("Admit (queue) error inesperado: %v", err)
	}

	provider := &scriptedProvider{turns: [][]llm.Event{
		{{Kind: llm.StepStarted}, {Kind: llm.ToolCall, CallID: "c1", ToolName: "echo", Input: json.RawMessage(`{"text":"x"}`)}, {Kind: llm.StepEnded}},
		{{Kind: llm.StepStarted}, {Kind: llm.TextDelta, Text: "summary"}, {Kind: llm.StepEnded}},
	}}

	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	r := NewRunner(store, inbox, provider, reg, tool.Permissions{"echo": true}, idCounter())
	r.SetSystemPrompt(func(string) string { return "base" })

	if err := r.Run(ctx, "s1", false, 2); err != nil {
		t.Fatalf("Run: %v", err)
	}
	requests := provider.capturedRequests()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	if len(requests[0].Tools) == 0 || len(requests[1].Tools) != 0 {
		t.Fatalf("tool counts = %d, %d; want tools then no tools", len(requests[0].Tools), len(requests[1].Tools))
	}
	if requests[1].System != "base\n\n"+FinalTurnInstruction {
		t.Fatalf("final system = %q", requests[1].System)
	}
}

func TestRunner_FinalTurnRejectsProviderToolCallWithoutExecutingIt(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	inbox := session.NewMemoryInbox()
	if err := inbox.Admit(ctx, "s1", session.Prompt{Text: "finish"}, session.DeliveryQueue); err != nil {
		t.Fatal(err)
	}
	var calls int
	provider := llm.NewFakeProvider(
		llm.Event{Kind: llm.ToolCall, CallID: "local", ToolName: "counter", Input: json.RawMessage(`{}`)},
		llm.Event{Kind: llm.ToolCall, CallID: "provider", ToolName: "external", ProviderExecuted: true, Input: json.RawMessage(`{}`)},
	)
	reg := tool.NewRegistry(tool.NewOutputStore(0), countingTool{calls: &calls})
	r := NewRunner(store, inbox, provider, reg, tool.Permissions{"counter": true}, idCounter())
	if err := r.Run(ctx, "s1", false, 1); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls != 0 {
		t.Fatalf("counter executions = %d, want 0", calls)
	}
	if pending, err := store.PendingToolCalls(ctx, "s1"); err != nil || len(pending) != 0 {
		t.Fatalf("pending tools = %v, %v; want none", pending, err)
	}
	events, err := store.Events(ctx, "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	failed := map[string]string{}
	for _, event := range events {
		if event.Kind == session.KindToolFailed {
			failed[event.CallID] = event.Error
		}
	}
	for _, callID := range []string{"local", "provider"} {
		if failed[callID] != finalTurnToolError {
			t.Errorf("failure for %s = %q, want %q", callID, failed[callID], finalTurnToolError)
		}
	}
}

func TestRunner_SteerAdmittedDuringFinalTurnStartsNewActivity(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	inbox := session.NewMemoryInbox()
	if err := inbox.Admit(ctx, "s1", session.Prompt{Text: "first"}, session.DeliveryQueue); err != nil {
		t.Fatal(err)
	}
	inner := &scriptedProvider{turns: [][]llm.Event{
		{{Kind: llm.TextDelta, Text: "first summary"}},
		{{Kind: llm.TextDelta, Text: "steered summary"}},
	}}
	provider := &steeringProvider{inner: inner, inbox: inbox, sessionID: "s1", steer: session.Prompt{Text: "new direction"}}
	r := NewRunner(store, inbox, provider, tool.NewRegistry(tool.NewOutputStore(0)), nil, idCounter())
	if err := r.Run(ctx, "s1", false, 1); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if inner.callCount() != 2 {
		t.Fatalf("turns = %d, want one final turn per activity", inner.callCount())
	}
	msgs, err := store.Messages(ctx, "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, msg := range msgs {
		found = found || msg.Role == session.RoleUser && msg.Text == "new direction"
	}
	if !found {
		t.Fatalf("steer was not promoted: %+v", msgs)
	}
}

func TestRunner_CancellationWinsOnFiniteFinalTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := session.NewMemoryStore()
	inbox := session.NewMemoryInbox()
	if err := inbox.Admit(ctx, "s1", session.Prompt{Text: "finish"}, session.DeliveryQueue); err != nil {
		t.Fatal(err)
	}
	cancel()
	r := NewRunner(store, inbox, llm.NewFakeProvider(), tool.NewRegistry(tool.NewOutputStore(0)), nil, idCounter())
	if err := r.Run(ctx, "s1", false, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
}

func TestRunner_UnlimitedPolicyCanExceedTwentyFiveTurns(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	inbox := session.NewMemoryInbox()
	if err := inbox.Admit(ctx, "s1", session.Prompt{Text: "continue"}, session.DeliveryQueue); err != nil {
		t.Fatal(err)
	}
	turns := make([][]llm.Event, 27)
	for i := range 26 {
		turns[i] = []llm.Event{{Kind: llm.ToolCall, CallID: "c" + strconv.Itoa(i), ToolName: "echo", Input: json.RawMessage(`{"text":"x"}`)}}
	}
	turns[26] = []llm.Event{{Kind: llm.TextDelta, Text: "done"}}
	provider := &scriptedProvider{turns: turns}
	r := NewRunner(store, inbox, provider, tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{}), tool.Permissions{"echo": true}, idCounter())
	if err := r.Run(ctx, "s1", false, 0); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(provider.capturedRequests()); got != 27 {
		t.Fatalf("requests = %d, want 27", got)
	}
}

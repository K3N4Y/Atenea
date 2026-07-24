package subagent

// Tests de propagacion del PermissionGate al subagente: sin este wiring el runner
// hijo corre con r.gate == nil y NUNCA pide aprobacion, asi que un subagente
// "general" podria correr bash sin la confirmacion ask-before-run que el chat
// principal SI exige. Es una via directa para evadir el gate. Aqui se fija el
// contrato: el gate (y su needsApproval) del padre se inyectan en el TaskTool y se
// propagan al runner hijo, de modo que una tool gateada quede igual de gateada en
// el subagente.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"atenea/internal/agent"
	"atenea/internal/llm"
	"atenea/internal/permission"
	"atenea/internal/session"
	"atenea/internal/tool"
)

// countingTool registra cuantas veces se ejecuto: si el gate niega y la tool no
// corre, el contador queda en 0. Seguro para uso concurrente.
type countingTool struct {
	name  string
	mu    sync.Mutex
	calls int
}

func (c *countingTool) Name() string          { return c.name }
func (c *countingTool) Description() string   { return "cuenta " + c.name }
func (*countingTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (c *countingTool) Execute(context.Context, json.RawMessage) (tool.Result, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return tool.Result{Output: "ran"}, nil
}

func (c *countingTool) ran() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// recordingGate registra cada permission.Request que recibe y responde con una
// decision fija, sin bloquear. Captura el sessionID/callID/toolName con que el
// runner hijo pide aprobacion.
type recordingGate struct {
	mu       sync.Mutex
	approved bool
	asked    []permission.Request
}

func (g *recordingGate) Ask(ctx context.Context, req permission.Request) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.asked = append(g.asked, req)
	return g.approved, nil
}

func (g *recordingGate) calls() []permission.Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]permission.Request, len(g.asked))
	copy(out, g.asked)
	return out
}

// TestTaskTool_PropagatesGateAndDenyDoesNotRunTool es el RED de seguridad: con el
// gate del padre inyectado en el TaskTool (SetPermissionGate) y una needsApproval
// que marca "bash", un subagente que invoca bash dispara la solicitud de permiso y,
// si el gate NIEGA, la tool NO corre. Tumba la version actual donde el runner hijo
// se crea sin SetPermissionGate (r.gate == nil): ahi bash correria sin gate y el
// contador quedaria en 1.
func TestTaskTool_PropagatesGateAndDenyDoesNotRunTool(t *testing.T) {
	ctx := context.Background()

	// Guion replayable que SIEMPRE pide una "bash": cada turno encadena la tool.
	prov := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.ToolCall, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"rm -rf /"}`)},
		llm.Event{Kind: llm.StepEnded},
	)

	bash := &countingTool{name: "bash"}
	children := tool.NewRegistry(tool.NewOutputStore(0), bash)
	defs := []agent.Def{{Name: "general", Tools: []string{"bash"}, Description: "full", Prompt: "x"}}

	tt := NewTaskTool(defs, prov, children, idCounter())

	gate := &recordingGate{approved: false} // niega
	tt.SetPermissionGate(gate, askBash{})

	_, _ = tt.Execute(ctx, json.RawMessage(`{"subagent_type":"general","prompt":"borra todo"}`))

	// El gate fue consultado para la bash del hijo.
	asked := gate.calls()
	if len(asked) == 0 {
		t.Fatalf("el gate no fue consultado: el subagente corrio bash sin pedir permiso")
	}
	found := false
	for _, req := range asked {
		if req.ToolName == "bash" && req.CallID == "c1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("gate.Ask = %+v, quiero una solicitud para bash/c1 del hijo", asked)
	}

	// Negado: la tool NO se ejecuto.
	if got := bash.ran(); got != 0 {
		t.Errorf("bash corrio %d veces pese a la negacion; quiero 0 (gate propagado)", got)
	}
}

// TestTaskTool_PropagatedGateApprovalRunsTool es el contrapunto (happy path): con el
// mismo wiring pero un gate que APRUEBA, la tool gateada SI corre. Asegura que la
// propagacion del gate no rompe el camino normal (aprobar deja correr bash).
func TestTaskTool_PropagatedGateApprovalRunsTool(t *testing.T) {
	ctx := context.Background()

	prov := &scriptedProvider{turns: [][]llm.Event{
		{
			{Kind: llm.StepStarted},
			{Kind: llm.ToolCall, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)},
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

	bash := &countingTool{name: "bash"}
	children := tool.NewRegistry(tool.NewOutputStore(0), bash)
	defs := []agent.Def{{Name: "general", Tools: []string{"bash"}, Description: "full", Prompt: "x"}}

	tt := NewTaskTool(defs, prov, children, idCounter())

	gate := &recordingGate{approved: true} // aprueba
	tt.SetPermissionGate(gate, askBash{})

	res, err := tt.Execute(ctx, json.RawMessage(`{"subagent_type":"general","prompt":"lista"}`))
	if err != nil {
		t.Fatalf("Execute error inesperado: %v", err)
	}

	if len(gate.calls()) == 0 {
		t.Fatalf("el gate no fue consultado pese a estar gateada la tool")
	}
	if got := bash.ran(); got != 1 {
		t.Errorf("bash corrio %d veces; quiero 1 (aprobado deja correr)", got)
	}
	if res.Output != "listo" {
		t.Errorf("res.Output = %q, quiero %q (reporte final del hijo)", res.Output, "listo")
	}
}

// recordingStore decora un session.Store: registra cada SessionEvent appendeado
// (con su sessionID) y delega en inner. Es el doble del EmittingStore real: si el
// runner hijo usa el store decorado, su Tool.Permission.Requested pasa por aqui;
// si usa un store aislado, no se registra nada.
type recordingStore struct {
	session.Store
	mu     sync.Mutex
	events []recordedEvent
}

type recordedEvent struct {
	sessionID string
	ev        session.SessionEvent
}

func (s *recordingStore) AppendEvent(ctx context.Context, sessionID string, ev session.SessionEvent) (session.Seq, error) {
	seq, err := s.Store.AppendEvent(ctx, sessionID, ev)
	if err == nil {
		s.mu.Lock()
		s.events = append(s.events, recordedEvent{sessionID: sessionID, ev: ev})
		s.mu.Unlock()
	}
	return seq, err
}

func (s *recordingStore) recorded() []recordedEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]recordedEvent, len(s.events))
	copy(out, s.events)
	return out
}

// TestTaskTool_StoreDecoratorReceivesChildPermissionRequest es el RED del
// surfacing: con un decorator de store inyectado (SetStoreDecorator), el runner
// hijo DEBE usar el store decorado, de modo que su Tool.Permission.Requested pase
// por el (en produccion ese decorator es el EmittingStore que lo publica al bus en
// session:<childID>, para que la UI muestre Aprobar/Denegar). Tumba la version
// donde Execute crea su propio session.NewMemoryStore aislado: ahi el decorator
// nunca ve el evento del hijo y la UI nunca se entera del permiso pendiente.
func TestTaskTool_StoreDecoratorReceivesChildPermissionRequest(t *testing.T) {
	// El runner padre anota su sessionID en el ctx de Execute via tool.WithSessionID;
	// el TaskTool lo lee con tool.SessionIDFrom para pasarlo al decorator (el canal
	// que atiende la UI).
	ctx := tool.WithSessionID(context.Background(), "parent")

	prov := &scriptedProvider{turns: [][]llm.Event{
		{
			{Kind: llm.StepStarted},
			{Kind: llm.ToolCall, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)},
			{Kind: llm.StepEnded},
		},
		{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: "ok"},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		},
	}}

	bash := &countingTool{name: "bash"}
	children := tool.NewRegistry(tool.NewOutputStore(0), bash)
	defs := []agent.Def{{Name: "general", Tools: []string{"bash"}, Description: "full", Prompt: "x"}}

	tt := NewTaskTool(defs, prov, children, idCounter())
	gate := &recordingGate{approved: false} // niega: el hijo no corre bash y cierra
	tt.SetPermissionGate(gate, askBash{})

	var dec *recordingStore
	var gotParent string
	tt.SetStoreDecorator(func(parentSessionID string, inner session.Store) session.Store {
		gotParent = parentSessionID
		dec = &recordingStore{Store: inner}
		return dec
	})

	if _, err := tt.Execute(ctx, json.RawMessage(`{"subagent_type":"general","prompt":"lista"}`)); err != nil {
		t.Fatalf("Execute error inesperado: %v", err)
	}

	if dec == nil {
		t.Fatal("el decorator de store no fue aplicado: el runner hijo uso un store aislado")
	}
	if gotParent != "parent" {
		t.Errorf("parentSessionID del decorator = %q, quiero %q (el canal del padre que atiende la UI)", gotParent, "parent")
	}
	found := false
	for _, r := range dec.recorded() {
		if r.ev.Kind == session.KindToolPermissionRequested && r.ev.CallID == "c1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("el store decorado no recibio el Tool.Permission.Requested del hijo; eventos = %+v", dec.recorded())
	}
}

// TestTaskTool_NoGatePropagatedRunsToolUngated fija que sin SetPermissionGate el
// TaskTool NO gatea nada (comportamiento por defecto, compatible con los tests
// existentes): un hijo que invoca bash la corre directo. Esto deja claro que el gate
// es opt-in via el setter y que su ausencia no rompe el camino sin gate.
func TestTaskTool_NoGatePropagatedRunsToolUngated(t *testing.T) {
	ctx := context.Background()

	prov := &scriptedProvider{turns: [][]llm.Event{
		{
			{Kind: llm.StepStarted},
			{Kind: llm.ToolCall, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)},
			{Kind: llm.StepEnded},
		},
		{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: "ok"},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		},
	}}

	bash := &countingTool{name: "bash"}
	children := tool.NewRegistry(tool.NewOutputStore(0), bash)
	defs := []agent.Def{{Name: "general", Tools: []string{"bash"}, Description: "full", Prompt: "x"}}

	tt := NewTaskTool(defs, prov, children, idCounter()) // sin SetPermissionGate

	if _, err := tt.Execute(ctx, json.RawMessage(`{"subagent_type":"general","prompt":"lista"}`)); err != nil {
		t.Fatalf("Execute error inesperado: %v", err)
	}
	if got := bash.ran(); got != 1 {
		t.Errorf("bash corrio %d veces; quiero 1 (sin gate corre directo)", got)
	}
}

// askBash is a test permission.Policy that asks only for "bash".
type askBash struct{}

func (askBash) Decide(c tool.Call) permission.Decision {
	if c.Name == "bash" {
		return permission.Ask
	}
	return permission.Allow
}

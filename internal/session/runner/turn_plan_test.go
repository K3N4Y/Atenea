package runner

import (
	"context"
	"encoding/json"
	"testing"

	"atenea/internal/llm"
	"atenea/internal/session"
	"atenea/internal/tool"
)

// plannerTool es un stub minimo (sin efectos) para anunciar una def "planner" en
// el set de permisos de plan-mode: las afirmaciones miran que su def viaje en el
// Request, no que se ejecute.
type plannerTool struct{}

func (plannerTool) Name() string { return "planner" }
func (plannerTool) Description() string {
	return "Stub tool para probar el set de permisos de plan-mode."
}
func (plannerTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (plannerTool) Execute(ctx context.Context, input json.RawMessage) (tool.Result, error) {
	return tool.Result{Output: "planned"}, nil
}

// hasToolDef informa si las defs del Request contienen una con ese nombre.
func hasToolDef(defs []llm.ToolDef, name string) bool {
	for _, d := range defs {
		if d.Name == name {
			return true
		}
	}
	return false
}

// newPlanModeRunner arma el runner comun de estos tests: siembra un usuario,
// registra echo (normal) y planner (plan), y construye el Runner con permisos
// NORMAL {"echo": true} y un recordingProvider que captura el Request.
func newPlanModeRunner(t *testing.T) (*Runner, *recordingProvider, session.Store) {
	t.Helper()
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
	reg := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{}, plannerTool{})
	r := NewRunner(store, session.NewMemoryInbox(), prov, reg, tool.Permissions{"echo": true}, func() string { return "a1" })
	return r, prov, store
}

// TestRunner_PlanModeUsesPlanSystemAndPerms es el RED: con el mode hook devolviendo
// ModePlan y un plan system/perms inyectados via SetPlanMode, el Request usa el
// system de plan ("PLAN[model]") y las tools materializadas con los permisos de plan
// (planner presente, echo ausente).
func TestRunner_PlanModeUsesPlanSystemAndPerms(t *testing.T) {
	ctx := context.Background()
	r, prov, store := newPlanModeRunner(t)

	r.SetMode(func(string) session.Mode { return session.ModePlan })
	r.SetPlanMode(func(model string) string { return "PLAN[" + model + "]" }, tool.Permissions{"planner": true})

	if _, err := r.runTurn(ctx, "s1"); err != nil {
		t.Fatalf("runTurn error inesperado: %v", err)
	}

	ep, err := store.Epoch(ctx, "s1")
	if err != nil {
		t.Fatalf("Epoch error inesperado: %v", err)
	}
	req := prov.captured()
	if got, want := req.System, "PLAN["+ep.Model+"]"; got != want {
		t.Errorf("Request.System = %q, quiero %q (plan system)", got, want)
	}
	if !hasToolDef(req.Tools, "planner") {
		t.Errorf("Request.Tools no contiene la def de planner; tools = %+v", req.Tools)
	}
	if hasToolDef(req.Tools, "echo") {
		t.Errorf("Request.Tools contiene la def de echo; en plan-mode no debe estar; tools = %+v", req.Tools)
	}
}

// TestRunner_NormalModeUnaffectedByPlanConfig: con SetPlanMode configurado pero el
// mode hook devolviendo ModeNormal, el Request usa el system normal ("NORM[model]")
// y las tools normales (echo presente, planner ausente). Prueba que la config de
// plan no se filtra al modo normal.
func TestRunner_NormalModeUnaffectedByPlanConfig(t *testing.T) {
	ctx := context.Background()
	r, prov, store := newPlanModeRunner(t)

	r.SetMode(func(string) session.Mode { return session.ModeNormal })
	r.SetSystemPrompt(func(m string) string { return "NORM[" + m + "]" })
	r.SetPlanMode(func(model string) string { return "PLAN[" + model + "]" }, tool.Permissions{"planner": true})

	if _, err := r.runTurn(ctx, "s1"); err != nil {
		t.Fatalf("runTurn error inesperado: %v", err)
	}

	ep, err := store.Epoch(ctx, "s1")
	if err != nil {
		t.Fatalf("Epoch error inesperado: %v", err)
	}
	req := prov.captured()
	if got, want := req.System, "NORM["+ep.Model+"]"; got != want {
		t.Errorf("Request.System = %q, quiero %q (system normal)", got, want)
	}
	if !hasToolDef(req.Tools, "echo") {
		t.Errorf("Request.Tools no contiene la def de echo; tools = %+v", req.Tools)
	}
	if hasToolDef(req.Tools, "planner") {
		t.Errorf("Request.Tools contiene planner; en modo normal no debe estar; tools = %+v", req.Tools)
	}
}

// TestRunner_NilModeHookDefaultsNormal: con SetPlanMode y SetSystemPrompt pero SIN
// llamar a SetMode, el comportamiento es el normal (system normal, echo presente,
// planner ausente). Prueba que el hook nil == modo normal.
func TestRunner_NilModeHookDefaultsNormal(t *testing.T) {
	ctx := context.Background()
	r, prov, store := newPlanModeRunner(t)

	r.SetSystemPrompt(func(m string) string { return "NORM[" + m + "]" })
	r.SetPlanMode(func(model string) string { return "PLAN[" + model + "]" }, tool.Permissions{"planner": true})

	if _, err := r.runTurn(ctx, "s1"); err != nil {
		t.Fatalf("runTurn error inesperado: %v", err)
	}

	ep, err := store.Epoch(ctx, "s1")
	if err != nil {
		t.Fatalf("Epoch error inesperado: %v", err)
	}
	req := prov.captured()
	if got, want := req.System, "NORM["+ep.Model+"]"; got != want {
		t.Errorf("Request.System = %q, quiero %q (system normal; hook nil)", got, want)
	}
	if !hasToolDef(req.Tools, "echo") {
		t.Errorf("Request.Tools no contiene la def de echo; tools = %+v", req.Tools)
	}
	if hasToolDef(req.Tools, "planner") {
		t.Errorf("Request.Tools contiene planner; con hook nil debe ser normal; tools = %+v", req.Tools)
	}
}

// TestRunner_PlanModeFallsBackToSystemWhenPlanSystemNil: en plan-mode con plan
// system nil, el system cae al normal ("NORM[model]") pero los permisos de plan SI
// se aplican (planner presente). Prueba el fallback independiente de system vs perms.
func TestRunner_PlanModeFallsBackToSystemWhenPlanSystemNil(t *testing.T) {
	ctx := context.Background()
	r, prov, store := newPlanModeRunner(t)

	r.SetMode(func(string) session.Mode { return session.ModePlan })
	r.SetSystemPrompt(func(m string) string { return "NORM[" + m + "]" })
	r.SetPlanMode(nil, tool.Permissions{"planner": true})

	if _, err := r.runTurn(ctx, "s1"); err != nil {
		t.Fatalf("runTurn error inesperado: %v", err)
	}

	ep, err := store.Epoch(ctx, "s1")
	if err != nil {
		t.Fatalf("Epoch error inesperado: %v", err)
	}
	req := prov.captured()
	if got, want := req.System, "NORM["+ep.Model+"]"; got != want {
		t.Errorf("Request.System = %q, quiero %q (fallback al system normal)", got, want)
	}
	if !hasToolDef(req.Tools, "planner") {
		t.Errorf("Request.Tools no contiene planner; los perms de plan SI deben aplicarse; tools = %+v", req.Tools)
	}
}

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"atenea/internal/llm"
	"atenea/internal/session"
)

// recordingEmit registra cada emision (canal, payload[0]) de forma segura ante
// concurrencia: el wiring de App emite desde la goroutine de Run mientras el test
// inspecciona. Es el emit fake que sustituye a runtime.EventsEmit en los tests.
type recordingEmit struct {
	mu       sync.Mutex
	channels []string
	payloads []interface{}
}

func (r *recordingEmit) emit(name string, data ...interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.channels = append(r.channels, name)
	if len(data) > 0 {
		r.payloads = append(r.payloads, data[0])
	} else {
		r.payloads = append(r.payloads, nil)
	}
}

func (r *recordingEmit) eventsOn(channel string) []session.SessionEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []session.SessionEvent
	for i, ch := range r.channels {
		if ch != channel {
			continue
		}
		if ev, ok := r.payloads[i].(session.SessionEvent); ok {
			out = append(out, ev)
		}
	}
	return out
}

func (r *recordingEmit) errorsOn(channel string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for i, ch := range r.channels {
		if ch != channel {
			continue
		}
		if s, ok := r.payloads[i].(string); ok {
			out = append(out, s)
		}
	}
	return out
}

type requestRecordingProvider struct {
	*llm.FakeProvider
	mu  sync.Mutex
	req llm.Request
}

func (p *requestRecordingProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	p.req = req
	p.mu.Unlock()
	return p.FakeProvider.Stream(ctx, req)
}

func (p *requestRecordingProvider) captured() llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.req
}

// TestApp_SendPromptStreamsTurnToBus: SendPrompt admite el prompt y arranca Run; el
// turno completo viaja por el bus al canal de la sesion. El prompt se promueve como
// Message{Role:user} y el texto del asistente se materializa coalescido en
// Step.Ended con su Message.
func TestApp_SendPromptStreamsTurnToBus(t *testing.T) {
	rec := &recordingEmit{}
	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "ok"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)
	app := newApp(fake, rec.emit)

	if err := app.SendPrompt("s1", "hola"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	evs := rec.eventsOn("session:s1")
	if len(evs) == 0 {
		t.Fatal("el bus no recibio ningun evento")
	}

	// Seq estrictamente crecientes.
	for i := 1; i < len(evs); i++ {
		if evs[i].Seq <= evs[i-1].Seq {
			t.Fatalf("Seq no estrictamente creciente: %d tras %d", evs[i].Seq, evs[i-1].Seq)
		}
	}

	hasUser := false
	hasStepEnded := false
	for _, ev := range evs {
		if ev.Message != nil && ev.Message.Role == session.RoleUser && ev.Message.Text == "hola" {
			hasUser = true
		}
		if ev.Kind == session.KindStepEnded && ev.Message != nil && ev.Message.Text == "ok" {
			hasStepEnded = true
		}
	}
	if !hasUser {
		t.Error("falta el evento con Message.Role==user y Text=='hola'")
	}
	if !hasStepEnded {
		t.Error("falta Step.Ended con Message.Text=='ok'")
	}
}

// TestApp_ListSessionsReturnsSentPrompts: tras enviar prompts en dos sesiones, el
// binding ListSessions las devuelve con su Title (el primer prompt del usuario),
// mas reciente primero. Es el wiring del historial de la sidebar.
func TestApp_ListSessionsReturnsSentPrompts(t *testing.T) {
	rec := &recordingEmit{}
	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "ok"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)
	app := newApp(fake, rec.emit)

	if err := app.SendPrompt("s1", "primera"); err != nil {
		t.Fatalf("SendPrompt(s1): %v", err)
	}
	app.wait()
	if err := app.SendPrompt("s2", "segunda"); err != nil {
		t.Fatalf("SendPrompt(s2): %v", err)
	}
	app.wait()

	got, err := app.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListSessions: got %d sessions, want 2 (%+v)", len(got), got)
	}
	// s2 fue la ultima con actividad: debe ir primero.
	if got[0].ID != "s2" || got[0].Title != "segunda" {
		t.Errorf("ListSessions[0] = %+v, want {s2 segunda}", got[0])
	}
	if got[1].ID != "s1" || got[1].Title != "primera" {
		t.Errorf("ListSessions[1] = %+v, want {s1 primera}", got[1])
	}
}

// TestApp_DeleteSessionRemovesItFromList: tras enviar un prompt para una sesion,
// DeleteSession la borra del store y ListSessions deja de listarla. Es el wiring
// del binding que borra una conversacion desde la sidebar.
func TestApp_DeleteSessionRemovesItFromList(t *testing.T) {
	rec := &recordingEmit{}
	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "ok"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)
	app := newApp(fake, rec.emit)

	if err := app.SendPrompt("chat-del", "hola"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	got, err := app.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if !containsSession(got, "chat-del") {
		t.Fatalf("ListSessions no contiene chat-del antes de borrar: %+v", got)
	}

	if err := app.DeleteSession("chat-del"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	got, err = app.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions tras borrar: %v", err)
	}
	if containsSession(got, "chat-del") {
		t.Fatalf("ListSessions sigue conteniendo chat-del tras borrar: %+v", got)
	}
}

func containsSession(summaries []session.SessionSummary, id string) bool {
	for _, s := range summaries {
		if s.ID == id {
			return true
		}
	}
	return false
}

// TestApp_SessionHistoryReplaysStoredLog: SessionHistory devuelve el log durable
// completo de la sesion (el mismo SessionEvent que viaja por el bus), para que el
// frontend lo reproduzca por applyEvent. Incluye el prompt del usuario y el
// Step.Ended con el texto coalescido del asistente.
func TestApp_SessionHistoryReplaysStoredLog(t *testing.T) {
	rec := &recordingEmit{}
	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "ok"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)
	app := newApp(fake, rec.emit)

	if err := app.SendPrompt("s1", "hola"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	got, err := app.SessionHistory("s1")
	if err != nil {
		t.Fatalf("SessionHistory: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("SessionHistory devolvio un log vacio")
	}
	// Seq estrictamente creciente: el log llega en orden.
	for i := 1; i < len(got); i++ {
		if got[i].Seq <= got[i-1].Seq {
			t.Fatalf("SessionHistory Seq no creciente: %d tras %d", got[i].Seq, got[i-1].Seq)
		}
	}
	var hasUser, hasStepEnded bool
	for _, ev := range got {
		if ev.Message != nil && ev.Message.Role == session.RoleUser && ev.Message.Text == "hola" {
			hasUser = true
		}
		if ev.Kind == session.KindStepEnded && ev.Message != nil && ev.Message.Text == "ok" {
			hasStepEnded = true
		}
	}
	if !hasUser {
		t.Error("SessionHistory no contiene el prompt del usuario")
	}
	if !hasStepEnded {
		t.Error("SessionHistory no contiene el Step.Ended con el texto del asistente")
	}
}

// TestApp_SessionHistoryUnknownSessionReturnsError: pedir el historial de una
// sesion inexistente propaga ErrSessionNotFound, no un log vacio silencioso.
func TestApp_SessionHistoryUnknownSessionReturnsError(t *testing.T) {
	app := newApp(demoProvider(), func(string, ...interface{}) {})

	if _, err := app.SessionHistory("ghost"); err == nil {
		t.Fatal("SessionHistory(ghost): got nil error, want ErrSessionNotFound")
	}
}

// TestApp_RequestAdvertisesGrepTool afirma el wiring de app.go: el registry de la
// app anuncia grep cuando arma el Request del proveedor, junto con las file tools
// existentes.
func TestApp_RequestAdvertisesGrepTool(t *testing.T) {
	rec := &recordingEmit{}
	prov := &requestRecordingProvider{FakeProvider: llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.StepEnded},
	)}
	app := newApp(prov, rec.emit)

	if err := app.SendPrompt("s1", "busca Execute"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	req := prov.captured()
	if !requestHasTool(req, "grep") {
		t.Fatalf("Request.Tools no contiene grep; tools = %+v", req.Tools)
	}
}

// TestApp_RequestAdvertisesGlobTool afirma el wiring de app.go para glob: el
// registry de la app anuncia la tool de busqueda de archivos cuando arma el
// Request del proveedor.
func TestApp_RequestAdvertisesGlobTool(t *testing.T) {
	rec := &recordingEmit{}
	prov := &requestRecordingProvider{FakeProvider: llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.StepEnded},
	)}
	app := newApp(prov, rec.emit)

	if err := app.SendPrompt("s1", "busca archivos go"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	req := prov.captured()
	if !requestHasTool(req, "glob") {
		t.Fatalf("Request.Tools no contiene glob; tools = %+v", req.Tools)
	}
}

// TestApp_RequestAdvertisesBashTool asserts that the app's registry advertises
// bash when building the provider Request (ask-before-run gates its execution,
// but the tool must be advertised for the model to request it).
func TestApp_RequestAdvertisesBashTool(t *testing.T) {
	rec := &recordingEmit{}
	prov := &requestRecordingProvider{FakeProvider: llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.StepEnded},
	)}
	app := newApp(prov, rec.emit)

	if err := app.SendPrompt("s1", "run a command"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	req := prov.captured()
	if !requestHasTool(req, "bash") {
		t.Fatalf("Request.Tools does not contain bash; tools = %+v", req.Tools)
	}
}

// TestApp_OffersTaskToolToModel afirma el wiring de app.go (S9 parte B): el
// registry de la app debe registrar la tool task para que el modelo pueda
// delegar en subagentes. Se captura el Request del proveedor y se busca un
// ToolDef con Name=="task" entre las tools anunciadas.
func TestApp_OffersTaskToolToModel(t *testing.T) {
	rec := &recordingEmit{}
	prov := &requestRecordingProvider{FakeProvider: llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "ok"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)}
	a := newApp(prov, rec.emit)

	if err := a.SendPrompt("s1", "hola"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	a.wait()

	// Busca la tool task entre las anunciadas; si no esta, lista los nombres
	// recibidos para que el mensaje muestre que ofrece hoy el wiring.
	req := prov.captured()
	var names []string
	for _, def := range req.Tools {
		names = append(names, def.Name)
		if def.Name == "task" {
			return
		}
	}
	t.Errorf("el modelo no recibe la tool 'task'; el wiring no la registra; tools = %v", names)
}

// TestApp_TaskToolDescriptionListsBuiltinSubagents afirma que el catalogo de
// subagentes fluye del wiring a la descripcion de la tool task: la Description del
// ToolDef task anunciado lista los built-in (explore read-only y general). Tumba
// un wiring que pasara defs vacias a NewTaskTool o que no conectara el catalogo
// de agent.Catalog, casos en que la tool se anunciaria pero sin subagentes.
func TestApp_TaskToolDescriptionListsBuiltinSubagents(t *testing.T) {
	rec := &recordingEmit{}
	prov := &requestRecordingProvider{FakeProvider: llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "ok"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)}
	a := newApp(prov, rec.emit)

	if err := a.SendPrompt("s1", "hola"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	a.wait()

	req := prov.captured()
	var taskDef *llm.ToolDef
	for i := range req.Tools {
		if req.Tools[i].Name == "task" {
			taskDef = &req.Tools[i]
			break
		}
	}
	if taskDef == nil {
		t.Fatalf("el modelo no recibe la tool 'task'; tools = %+v", req.Tools)
	}
	if !strings.Contains(taskDef.Description, "explore") {
		t.Errorf("la descripcion de task no lista el built-in 'explore'; description = %q", taskDef.Description)
	}
	if !strings.Contains(taskDef.Description, "general") {
		t.Errorf("la descripcion de task no lista el built-in 'general'; description = %q", taskDef.Description)
	}
}

// TestApp_PlanModeDoesNotOfferTaskTool afirma que plan-mode (solo lectura) NO
// anuncia la tool task: el agente investiga y presenta un plan, no delega. Tumba
// un wiring que hubiera metido task tambien en los Permissions de plan-mode. Como
// sanity de que de verdad estamos en plan-mode, verifica que SI esta present_plan.
func TestApp_PlanModeDoesNotOfferTaskTool(t *testing.T) {
	rec := &recordingEmit{}
	prov := &requestRecordingProvider{FakeProvider: llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "ok"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)}
	a := newApp(prov, rec.emit)

	if err := a.SendPlanPrompt("s1", "investiga"); err != nil {
		t.Fatalf("SendPlanPrompt: %v", err)
	}
	a.wait()

	req := prov.captured()
	if requestHasTool(req, "task") {
		t.Errorf("plan-mode anuncia la tool task (no debe delegar); tools = %+v", req.Tools)
	}
	if !requestHasTool(req, "present_plan") {
		t.Errorf("sanity: plan-mode no anuncia present_plan; tools = %+v", req.Tools)
	}
}

// TestApp_SendPlanPromptUsesPlanModeToolsAndPrompt: SendPlanPrompt arranca la
// sesion en plan-mode: el Request del proveedor anuncia present_plan (y las tools
// de investigacion read/glob/grep), NO anuncia las de escritura (bash/write), y el
// System lleva el contrato de plan-mode (contiene "present_plan").
func TestApp_SendPlanPromptUsesPlanModeToolsAndPrompt(t *testing.T) {
	rec := &recordingEmit{}
	prov := &requestRecordingProvider{FakeProvider: llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.StepEnded},
	)}
	app := newApp(prov, rec.emit)

	if err := app.SendPlanPrompt("s1", "planea la feature X"); err != nil {
		t.Fatalf("SendPlanPrompt: %v", err)
	}
	app.wait()

	req := prov.captured()
	if !requestHasTool(req, "present_plan") {
		t.Errorf("Request.Tools no contiene present_plan; tools = %+v", req.Tools)
	}
	if requestHasTool(req, "bash") {
		t.Errorf("Request.Tools contiene bash en plan-mode; tools = %+v", req.Tools)
	}
	if requestHasTool(req, "write") {
		t.Errorf("Request.Tools contiene write en plan-mode; tools = %+v", req.Tools)
	}
	if !strings.Contains(req.System, "present_plan") {
		t.Errorf("Request.System no lleva el contrato de plan-mode; system = %q", req.System)
	}
}

// TestApp_SystemPromptAdvertisesDiscoveredSkills: con un SKILL.md bajo
// <root>/.atenea/skills, el agente lo descubre al ensamblar; su bloque
// <available_skills> (name + description) viaja en el system prompt y la tool
// skill queda anunciada para cargarlo bajo demanda. Verificacion end-to-end del
// disclosure progresivo sin GUI.
func TestApp_SystemPromptAdvertisesDiscoveredSkills(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".atenea", "skills", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "---\nname: demo\ndescription: skill de prueba\n---\ninstrucciones demo\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	// newAppWithStore ancla el root en os.Getwd(): situarse en el tempdir hace que
	// el descubrimiento halle la skill demo. HOME a un tempdir vacio aisla el test de
	// las skills globales reales del home (skillDirs tambien escanea ~/.agents, etc.).
	t.Setenv("HOME", t.TempDir())
	t.Chdir(root)

	rec := &recordingEmit{}
	prov := &requestRecordingProvider{FakeProvider: llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.StepEnded},
	)}
	app := newApp(prov, rec.emit)

	if err := app.SendPrompt("s1", "hola"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	req := prov.captured()
	if !requestHasTool(req, "skill") {
		t.Errorf("Request.Tools no contiene la tool skill; tools = %+v", req.Tools)
	}
	if !strings.Contains(req.System, "<available_skills>") {
		t.Errorf("Request.System no lleva el bloque de skills; system = %q", req.System)
	}
	if !strings.Contains(req.System, "demo") || !strings.Contains(req.System, "skill de prueba") {
		t.Errorf("Request.System no lista la skill demo con su description; system = %q", req.System)
	}
}

// TestApp_DiscoversSkillsFromAgentsDir: el agente tambien descubre skills bajo el
// estandar <root>/.agents/skills, no solo .atenea/skills. Su bloque viaja en el
// system prompt igual que las propias.
func TestApp_DiscoversSkillsFromAgentsDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".agents", "skills", "estandar")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "---\nname: estandar\ndescription: skill bajo .agents\n---\ncuerpo\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	t.Setenv("HOME", t.TempDir()) // aisla de las skills globales reales del home
	t.Chdir(root)

	rec := &recordingEmit{}
	prov := &requestRecordingProvider{FakeProvider: llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.StepEnded},
	)}
	app := newApp(prov, rec.emit)

	if err := app.SendPrompt("s1", "hola"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	req := prov.captured()
	if !strings.Contains(req.System, "estandar") || !strings.Contains(req.System, "skill bajo .agents") {
		t.Errorf("Request.System no lista la skill de .agents/skills; system = %q", req.System)
	}
}

// TestApp_SendPromptAfterPlanResetsToNormalTools: tras un SendPlanPrompt, un
// SendPrompt en la misma sesion vuelve a modo normal: el ultimo Request anuncia
// bash y NO anuncia present_plan.
func TestApp_SendPromptAfterPlanResetsToNormalTools(t *testing.T) {
	rec := &recordingEmit{}
	prov := &requestRecordingProvider{FakeProvider: llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.StepEnded},
	)}
	app := newApp(prov, rec.emit)

	if err := app.SendPlanPrompt("s1", "plan"); err != nil {
		t.Fatalf("SendPlanPrompt: %v", err)
	}
	app.wait()
	if err := app.SendPrompt("s1", "ahora hazlo"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	req := prov.captured()
	if !requestHasTool(req, "bash") {
		t.Errorf("Request.Tools no contiene bash tras volver a normal; tools = %+v", req.Tools)
	}
	if requestHasTool(req, "present_plan") {
		t.Errorf("Request.Tools sigue con present_plan tras volver a normal; tools = %+v", req.Tools)
	}
}

// TestApp_AcceptPlanRunsNormalModeAndAdmitsImplementPrompt: AcceptPlan corre en
// modo normal (Request anuncia bash, no present_plan) y promueve el prompt fijo de
// implementacion como Message del usuario (acceptPlanPrompt) al log de la sesion.
func TestApp_AcceptPlanRunsNormalModeAndAdmitsImplementPrompt(t *testing.T) {
	rec := &recordingEmit{}
	prov := &requestRecordingProvider{FakeProvider: llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.StepEnded},
	)}
	app := newApp(prov, rec.emit)

	if err := app.AcceptPlan("s1"); err != nil {
		t.Fatalf("AcceptPlan: %v", err)
	}
	app.wait()

	req := prov.captured()
	if !requestHasTool(req, "bash") {
		t.Errorf("Request.Tools no contiene bash en AcceptPlan; tools = %+v", req.Tools)
	}
	if requestHasTool(req, "present_plan") {
		t.Errorf("Request.Tools contiene present_plan en AcceptPlan; tools = %+v", req.Tools)
	}

	got, err := app.SessionHistory("s1")
	if err != nil {
		t.Fatalf("SessionHistory: %v", err)
	}
	var sawImplementPrompt bool
	for _, ev := range got {
		if ev.Message != nil && ev.Message.Role == session.RoleUser && ev.Message.Text == acceptPlanPrompt {
			sawImplementPrompt = true
		}
	}
	if !sawImplementPrompt {
		t.Errorf("SessionHistory no contiene el prompt de implementacion (%q)", acceptPlanPrompt)
	}
}

// TestApp_RequestPlanChangeStaysInPlanMode: "Solicitar cambio" en el frontend es
// un segundo SendPlanPrompt con el feedback. La sesion debe SEGUIR en plan-mode:
// el ultimo Request anuncia present_plan y NO bash, y ambos prompts (el original y
// el feedback) quedan en el historial para que el agente reescriba el plan.
func TestApp_RequestPlanChangeStaysInPlanMode(t *testing.T) {
	rec := &recordingEmit{}
	prov := &requestRecordingProvider{FakeProvider: llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.StepEnded},
	)}
	app := newApp(prov, rec.emit)

	if err := app.SendPlanPrompt("s1", "planea la feature X"); err != nil {
		t.Fatalf("SendPlanPrompt: %v", err)
	}
	app.wait()
	if err := app.SendPlanPrompt("s1", "cambia el paso 2"); err != nil {
		t.Fatalf("SendPlanPrompt (cambio): %v", err)
	}
	app.wait()

	req := prov.captured()
	if !requestHasTool(req, "present_plan") {
		t.Errorf("Request.Tools no contiene present_plan tras solicitar cambio; tools = %+v", req.Tools)
	}
	if requestHasTool(req, "bash") {
		t.Errorf("Request.Tools contiene bash tras solicitar cambio (deberia seguir en plan); tools = %+v", req.Tools)
	}

	got, err := app.SessionHistory("s1")
	if err != nil {
		t.Fatalf("SessionHistory: %v", err)
	}
	var sawPlan, sawChange bool
	for _, ev := range got {
		if ev.Message == nil || ev.Message.Role != session.RoleUser {
			continue
		}
		switch ev.Message.Text {
		case "planea la feature X":
			sawPlan = true
		case "cambia el paso 2":
			sawChange = true
		}
	}
	if !sawPlan || !sawChange {
		t.Errorf("el historial debe tener el plan original y el feedback; plan=%v cambio=%v", sawPlan, sawChange)
	}
}

// TestApp_AcceptPlanFromPlanModeResetsToNormal: aceptar un plan presentado vuelve
// a modo normal aunque la sesion venia de plan-mode (el plan-mode no se "pega").
// El ultimo Request anuncia bash y NO present_plan.
func TestApp_AcceptPlanFromPlanModeResetsToNormal(t *testing.T) {
	rec := &recordingEmit{}
	prov := &requestRecordingProvider{FakeProvider: llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.StepEnded},
	)}
	app := newApp(prov, rec.emit)

	if err := app.SendPlanPrompt("s1", "planea la feature X"); err != nil {
		t.Fatalf("SendPlanPrompt: %v", err)
	}
	app.wait()
	if err := app.AcceptPlan("s1"); err != nil {
		t.Fatalf("AcceptPlan: %v", err)
	}
	app.wait()

	req := prov.captured()
	if !requestHasTool(req, "bash") {
		t.Errorf("Request.Tools no contiene bash tras aceptar desde plan-mode; tools = %+v", req.Tools)
	}
	if requestHasTool(req, "present_plan") {
		t.Errorf("Request.Tools sigue con present_plan tras aceptar; tools = %+v", req.Tools)
	}
}

// TestApp_ModelReportsConfiguredModel: el binding Model() expone a la UI el modelo
// activo resuelto desde OPENROUTER_MODEL, para que el frontend sepa que ventana de
// contexto usar al pintar el uso de tokens.
func TestApp_ModelReportsConfiguredModel(t *testing.T) {
	t.Setenv("OPENROUTER_MODEL", "anthropic/claude-opus-4.8")
	app := newApp(demoProvider(), func(string, ...interface{}) {})

	if got := app.Model(); got != "anthropic/claude-opus-4.8" {
		t.Errorf("Model() = %q, want %q", got, "anthropic/claude-opus-4.8")
	}
}

// TestApp_ModelFallsBackToDefault: sin OPENROUTER_MODEL, Model() cae al defaultModel,
// mismo fallback que chooseProvider para que la UI y el provider coincidan.
func TestApp_ModelFallsBackToDefault(t *testing.T) {
	t.Setenv("OPENROUTER_MODEL", "")
	app := newApp(demoProvider(), func(string, ...interface{}) {})

	if got := app.Model(); got != defaultModel {
		t.Errorf("Model() = %q, want %q", got, defaultModel)
	}
}

func requestHasTool(req llm.Request, name string) bool {
	for _, def := range req.Tools {
		if def.Name == name {
			return true
		}
	}
	return false
}

// blockingProvider entrega un StepStarted (cuando el runner lo consume), avisa por
// started y luego bloquea hasta que se cancele el ctx, simulando un turno en vuelo
// que solo termina por interrupcion. Cierra out al salir: ningun receptor cuelga.
type blockingProvider struct {
	started chan struct{}
	once    sync.Once
}

var _ llm.Provider = (*blockingProvider)(nil)

func (p *blockingProvider) Stream(ctx context.Context, _ llm.Request) (<-chan llm.Event, error) {
	out := make(chan llm.Event)
	go func() {
		defer close(out)
		select {
		case <-ctx.Done():
			return
		case out <- llm.Event{Kind: llm.StepStarted}:
		}
		p.once.Do(func() { close(p.started) })
		<-ctx.Done()
	}()
	return out, nil
}

// TestApp_StopInterruptsInflightTurn: Stop cancela la corrida en vuelo; la
// interrupcion viaja por el cableado como Step.Failed. La cancelacion es un cierre
// limpio: NO se publica error en el canal de error (eso lo cubre tambien
// TestApp_StopDoesNotPublishCancelError). Correr con -race.
func TestApp_StopInterruptsInflightTurn(t *testing.T) {
	rec := &recordingEmit{}
	prov := &blockingProvider{started: make(chan struct{})}
	app := newApp(prov, rec.emit)

	if err := app.SendPrompt("s1", "hola"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}

	select {
	case <-prov.started:
	case <-time.After(2 * time.Second):
		t.Fatal("el turno no arranco a tiempo")
	}

	app.Stop("s1")
	app.wait()

	evs := rec.eventsOn("session:s1")
	hasStepFailed := false
	for _, ev := range evs {
		if ev.Kind == session.KindStepFailed {
			hasStepFailed = true
		}
	}
	if !hasStepFailed {
		t.Error("falta Step.Failed: la interrupcion no viajo por el cableado")
	}

	if errs := rec.errorsOn("session:s1:error"); len(errs) != 0 {
		t.Errorf("una cancelacion no debe publicar error; se reenvio %v", errs)
	}
}

// TestApp_StopDoesNotPublishCancelError: una cancelacion deliberada (Stop) hace que
// Run retorne context.Canceled. Eso es un cierre limpio, NO un fallo: el frontend no
// debe pintar un error rojo. El canal de error NO debe recibir nada. Correr con -race.
func TestApp_StopDoesNotPublishCancelError(t *testing.T) {
	rec := &recordingEmit{}
	prov := &blockingProvider{started: make(chan struct{})}
	app := newApp(prov, rec.emit)

	if err := app.SendPrompt("s1", "hola"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}

	select {
	case <-prov.started:
	case <-time.After(2 * time.Second):
		t.Fatal("el turno no arranco a tiempo")
	}

	app.Stop("s1")
	app.wait()

	if errs := rec.errorsOn("session:s1:error"); len(errs) != 0 {
		t.Errorf("una cancelacion no debe publicar error; se reenvio %v", errs)
	}
}

// errProvider emite un StepFailed: el runner lo eleva como ProviderError, un error
// duro REAL (no una cancelacion). Sirve para triangular que los errores reales si se
// siguen reenviando por el canal de error.
type errProvider struct{}

var _ llm.Provider = errProvider{}

func (errProvider) Stream(_ context.Context, _ llm.Request) (<-chan llm.Event, error) {
	out := make(chan llm.Event, 2)
	out <- llm.Event{Kind: llm.StepStarted}
	out <- llm.Event{Kind: llm.StepFailed, Text: "boom del proveedor"}
	close(out)
	return out, nil
}

// TestApp_RealErrorIsPublished: un error duro REAL de Run (aqui un fallo del
// proveedor) si debe reenviarse por el canal de error para que la UI lo muestre. La
// excepcion del cierre limpio es SOLO para context.Canceled/DeadlineExceeded.
func TestApp_RealErrorIsPublished(t *testing.T) {
	rec := &recordingEmit{}
	app := newApp(errProvider{}, rec.emit)

	if err := app.SendPrompt("s1", "hola"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	if errs := rec.errorsOn("session:s1:error"); len(errs) == 0 {
		t.Error("un error real del proveedor debe reenviarse por session:s1:error")
	}
}

// scriptedProvider returns a DIFFERENT script per turn (the M2 FakeProvider
// always replays the same one): the i-th Stream reproduces turns[i], or an
// empty script if exhausted. It lets us chain "tool in turn 1, text in turn 2"
// without the loop asking permission forever.
type scriptedProvider struct {
	mu    sync.Mutex
	calls int
	turns [][]llm.Event
}

func (p *scriptedProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	var script []llm.Event
	if p.calls < len(p.turns) {
		script = p.turns[p.calls]
	}
	p.calls++
	p.mu.Unlock()
	return llm.NewFakeProvider(script...).Stream(ctx, req)
}

// waitForPermissionRequest waits (with a timeout) for the bus to receive the
// Tool.Permission.Requested for callID: the runner emits it before blocking on
// the gate, so the test knows when to resolve.
func waitForPermissionRequest(t *testing.T, rec *recordingEmit, channel, callID string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		for _, ev := range rec.eventsOn(channel) {
			if ev.Kind == session.KindToolPermissionRequested && ev.CallID == callID {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("Tool.Permission.Requested for %s did not arrive", callID)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

// TestApp_BashCallAsksPermissionAndDenyDoesNotRun is the end-to-end integration
// test for ask-before-run wiring: a bash tool call makes the runner emit
// Tool.Permission.Requested and BLOCK; when denied via the ResolveToolPermission
// binding the tool does not run and Tool.Failed is published. It covers
// SetPermissionGate + needsApproval==bash + the end-to-end binding, without
// actually running bash (it denies) or hanging (a text-only turn 2 closes
// activity). Run with -race.
func TestApp_BashCallAsksPermissionAndDenyDoesNotRun(t *testing.T) {
	rec := &recordingEmit{}
	prov := &scriptedProvider{turns: [][]llm.Event{
		{
			{Kind: llm.StepStarted},
			{Kind: llm.ToolCall, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"echo should-not-run"}`)},
			{Kind: llm.StepEnded},
		},
		{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: "done"},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		},
	}}
	app := newApp(prov, rec.emit)

	if err := app.SendPrompt("s1", "run echo"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}

	waitForPermissionRequest(t, rec, "session:s1", "c1")
	app.ResolveToolPermission("s1", "c1", false) // DENY: bash must not run
	app.wait()

	var sawRequest, sawFailed, sawSuccess bool
	for _, ev := range rec.eventsOn("session:s1") {
		switch {
		case ev.Kind == session.KindToolPermissionRequested && ev.CallID == "c1":
			sawRequest = true
		case ev.Kind == session.KindToolFailed && ev.CallID == "c1":
			sawFailed = true
		case ev.Kind == session.KindToolSuccess && ev.CallID == "c1":
			sawSuccess = true
		}
	}
	if !sawRequest {
		t.Error("missing Tool.Permission.Requested of c1: the gate is not wired for bash")
	}
	if !sawFailed {
		t.Error("missing Tool.Failed of c1: the denial did not propagate via the binding")
	}
	if sawSuccess {
		t.Error("Tool.Success of c1 happened despite denying the permission")
	}
}

// TestApp_ResolveToolPermissionWiredToGate verifies the binding in isolation:
// the decision arriving via ResolveToolPermission unblocks a pending Ask on the
// app's gate (proves the gate field exists and the binding invokes it).
func TestApp_ResolveToolPermissionWiredToGate(t *testing.T) {
	app := newApp(demoProvider(), func(string, ...interface{}) {})

	done := make(chan bool, 1)
	go func() {
		approved, err := app.gate.Ask(context.Background(), session.PermissionRequest{SessionID: "s1", CallID: "c1"})
		if err != nil {
			t.Errorf("Ask unexpected error: %v", err)
		}
		done <- approved
	}()

	deadline := time.After(2 * time.Second)
	for {
		app.ResolveToolPermission("s1", "c1", true)
		select {
		case got := <-done:
			if !got {
				t.Errorf("the decision did not arrive as approved=true via the binding")
			}
			return
		case <-deadline:
			t.Fatal("the binding did not deliver the decision to the gate")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

// taskAwareProvider devuelve un guion distinto segun el system prompt del Request:
// el turno PADRE (system del agente principal) pide la tool "task" para delegar en
// el subagente "general"; el turno HIJO (system del subagente "general", que en el
// wiring real es su Prompt fijo) pide una bash. Asi un mismo provider sirve al
// runner padre y al runner hijo del subagente, que comparten el provider en app.go.
// El segundo turno de cada uno cierra con texto para no agotar MaxSteps.
type taskAwareProvider struct {
	mu          sync.Mutex
	sentinel    string // ruta que el bash del hijo intentaria crear (touch)
	parentCalls int
	childCalls  int
}

func (p *taskAwareProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	isChild := strings.Contains(req.System, "subagente de proposito general")
	var script []llm.Event
	if isChild {
		if p.childCalls == 0 {
			input, _ := json.Marshal(map[string]string{"command": "touch " + p.sentinel})
			script = []llm.Event{
				{Kind: llm.StepStarted},
				{Kind: llm.ToolCall, CallID: "b1", ToolName: "bash", Input: json.RawMessage(input)},
				{Kind: llm.StepEnded},
			}
		} else {
			script = []llm.Event{
				{Kind: llm.StepStarted},
				{Kind: llm.TextStarted},
				{Kind: llm.TextDelta, Text: "informe del hijo"},
				{Kind: llm.TextEnded},
				{Kind: llm.StepEnded},
			}
		}
		p.childCalls++
	} else {
		if p.parentCalls == 0 {
			script = []llm.Event{
				{Kind: llm.StepStarted},
				{Kind: llm.ToolCall, CallID: "t1", ToolName: "task", Input: json.RawMessage(`{"subagent_type":"general","prompt":"haz algo"}`)},
				{Kind: llm.StepEnded},
			}
		} else {
			script = []llm.Event{
				{Kind: llm.StepStarted},
				{Kind: llm.TextStarted},
				{Kind: llm.TextDelta, Text: "listo"},
				{Kind: llm.TextEnded},
				{Kind: llm.StepEnded},
			}
		}
		p.parentCalls++
	}
	p.mu.Unlock()
	return llm.NewFakeProvider(script...).Stream(ctx, req)
}

// TestApp_SubagentBashIsGatedBySharedGate is the end-to-end security test for the
// subagent gate propagation: the production wiring (wire) must give the TaskTool
// the SAME app.gate and bash-needsApproval as the main chat, so a subagent that
// invokes bash blocks on the shared gate instead of running it ungated. The parent
// delegates to the "general" subagent, which calls bash; the test denies via the
// shared gate keyed by the child's session id (the TaskTool's fresh id generator
// yields msg-1) and asserts the bash command never ran (its sentinel file is
// absent). Without SetPermissionGate on the TaskTool the child runs bash directly
// and the sentinel appears: that is the security regression this guards. Run with
// -race.
func TestApp_SubagentBashIsGatedBySharedGate(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "pwned")

	prov := &taskAwareProvider{sentinel: sentinel}
	rec := &recordingEmit{}
	app := newApp(prov, rec.emit)
	if err := app.SetWorkspace(dir); err != nil {
		t.Fatalf("SetWorkspace: %v", err)
	}

	if err := app.SendPrompt("s1", "delega en el subagente"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}

	// The child gate Ask blocks keyed by (childSessionID=msg-1, callID=b1). Deny it
	// until the pending request is delivered, so the bash is never settled. The
	// child session id is msg-1: wire gives the TaskTool a fresh id generator.
	deadline := time.After(3 * time.Second)
	for {
		if app.gate.Resolve("msg-1", "b1", false) {
			break // the deny reached a pending Ask: the subagent bash WAS gated
		}
		select {
		case <-deadline:
			t.Fatal("the subagent bash never blocked on the shared gate: it is not gated")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	app.wait()

	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("the subagent bash ran despite the denial: the gate is not propagated to the child")
	}
}

// TestApp_SubagentPermissionRequestReachesBus is the end-to-end surfacing test:
// when a subagent invokes bash, its Tool.Permission.Requested must reach the bus on
// the PARENT channel (session:s1, the one the UI already listens on), carrying the
// child session id (msg-1) in the payload so the UI resolves with (childID, callID).
// The child session id is msg-1 (wire gives the TaskTool a fresh id generator).
// Without SetStoreDecorator the child uses an isolated store and the event never
// reaches the bus: the request stays invisible and the run hangs until Stop. Here a
// deny via the shared gate keyed by (msg-1, b1) lets the child close. Run with -race.
func TestApp_SubagentPermissionRequestReachesBus(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "pwned")

	prov := &taskAwareProvider{sentinel: sentinel}
	rec := &recordingEmit{}
	app := newApp(prov, rec.emit)
	if err := app.SetWorkspace(dir); err != nil {
		t.Fatalf("SetWorkspace: %v", err)
	}

	if err := app.SendPrompt("s1", "delega en el subagente"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}

	// The child permission request must surface on the parent channel; deny it so the
	// child closes instead of hanging. Poll until the gate has the pending Ask.
	deadline := time.After(3 * time.Second)
	for {
		if app.gate.Resolve("msg-1", "b1", false) {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the subagent bash never blocked on the shared gate")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	app.wait()

	// The request surfaces on the PARENT channel (session:s1) with the child id in the
	// payload's SessionID, so the UI shows Approve/Deny and resolves with (msg-1, b1).
	var sawChildRequest bool
	for _, ev := range rec.eventsOn("session:s1") {
		if ev.Kind == session.KindToolPermissionRequested && ev.CallID == "b1" && ev.SessionID == "msg-1" {
			sawChildRequest = true
		}
	}
	if !sawChildRequest {
		t.Fatal("missing Tool.Permission.Requested of the subagent on session:s1 with child SessionID: the UI cannot surface it")
	}
}

// TestTitleFromProvider_AccumulatesStreamText: el helper de titulado abre un turno
// aislado contra el provider y concatena los Text.Delta del stream en el titulo,
// recortando espacios. Es la pieza que NewApp cablea como titler real.
func TestTitleFromProvider_AccumulatesStreamText(t *testing.T) {
	prov := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "Configurar "},
		llm.Event{Kind: llm.TextDelta, Text: "el proxy"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)
	if got := titleFromProvider(prov, "modelo", "como configuro el proxy"); got != "Configurar el proxy" {
		t.Fatalf("titleFromProvider: got %q, want %q", got, "Configurar el proxy")
	}
}

// ctxRecorderProvider es un Provider doble que captura el ctx con el que el caller
// invoca Stream, para verificar que los turnos aislados de titulo/commit lo acotan
// con un deadline en vez de usar context.Background() crudo. Reproduce un guion fijo
// (como FakeProvider) para que el helper igual produzca su texto.
type ctxRecorderProvider struct {
	gotCtx context.Context
	script []llm.Event
}

func (p *ctxRecorderProvider) Stream(ctx context.Context, _ llm.Request) (<-chan llm.Event, error) {
	p.gotCtx = ctx
	out := make(chan llm.Event)
	go func() {
		defer close(out)
		for _, ev := range p.script {
			out <- ev
		}
	}()
	return out, nil
}

// TestTitleFromProvider_BoundsContextWithDeadline: el turno aislado de titulado no
// debe usar context.Background() crudo, sino acotarlo con un deadline para que un SSE
// colgado no deje la goroutine viva para siempre. Verifica que el ctx con el que el
// helper llama a Stream trae un Deadline configurado.
func TestTitleFromProvider_BoundsContextWithDeadline(t *testing.T) {
	prov := &ctxRecorderProvider{script: []llm.Event{
		{Kind: llm.TextStarted},
		{Kind: llm.TextDelta, Text: "Titulo"},
		{Kind: llm.TextEnded},
	}}
	titleFromProvider(prov, "modelo", "hola")
	if prov.gotCtx == nil {
		t.Fatal("titleFromProvider no llamo a Stream")
	}
	if _, ok := prov.gotCtx.Deadline(); !ok {
		t.Fatal("titleFromProvider debe acotar el ctx con un deadline, no usar context.Background()")
	}
}

// TestTitleFromProvider_EmptyWhenNoText: si el stream no produce texto, el titulo es
// "" para que el caller caiga al fallback (el primer mensaje).
func TestTitleFromProvider_EmptyWhenNoText(t *testing.T) {
	prov := llm.NewFakeProvider(llm.Event{Kind: llm.StepStarted}, llm.Event{Kind: llm.StepEnded})
	if got := titleFromProvider(prov, "modelo", "hola"); got != "" {
		t.Fatalf("titleFromProvider sin texto: got %q, want empty", got)
	}
}

// TestApp_AutoTitlesFirstMessage: con un titler cableado, el primer mensaje de una
// sesion nueva genera un Session.Title que la sidebar (ListSessions) muestra en vez
// del primer prompt.
func TestApp_AutoTitlesFirstMessage(t *testing.T) {
	app := newApp(demoProvider(), func(string, ...interface{}) {})
	app.titler = func(string) string { return "Titulo Generado" }

	if err := app.SendPrompt("s1", "hola, este es el primer mensaje"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	sessions, err := app.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Title != "Titulo Generado" {
		t.Fatalf("ListSessions: got %+v, want title %q", sessions, "Titulo Generado")
	}
}

// TestApp_AutoTitleFallsBackToFirstMessageWhenEmpty: si el titler no produce titulo,
// no se persiste nada y la sidebar cae al primer mensaje (el fallback pedido).
func TestApp_AutoTitleFallsBackToFirstMessageWhenEmpty(t *testing.T) {
	app := newApp(demoProvider(), func(string, ...interface{}) {})
	app.titler = func(string) string { return "" }

	if err := app.SendPrompt("s1", "mi primer mensaje"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	sessions, err := app.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Title != "mi primer mensaje" {
		t.Fatalf("ListSessions: got %+v, want fallback al primer mensaje", sessions)
	}
}

// TestApp_AutoTitleSkipsSecondMessage: el titulo se genera solo en el primer mensaje;
// los siguientes no vuelven a invocar al titler.
func TestApp_AutoTitleSkipsSecondMessage(t *testing.T) {
	app := newApp(demoProvider(), func(string, ...interface{}) {})
	var mu sync.Mutex
	calls := 0
	app.titler = func(string) string {
		mu.Lock()
		calls++
		mu.Unlock()
		return "T"
	}

	if err := app.SendPrompt("s1", "primero"); err != nil {
		t.Fatalf("SendPrompt #1: %v", err)
	}
	app.wait()
	if err := app.SendPrompt("s1", "segundo"); err != nil {
		t.Fatalf("SendPrompt #2: %v", err)
	}
	app.wait()

	mu.Lock()
	n := calls
	mu.Unlock()
	if n != 1 {
		t.Fatalf("titler llamado %d veces, want 1 (solo primer mensaje)", n)
	}
}

// TestApp_TitleGeneratedAfterTurnStreams: regresion. El titulado del primer mensaje
// corre DESPUES del turno, no en paralelo: al titular, el turno ya cerro (Step.Ended
// en el bus) y la respuesta ya viajo al frontend. Asi el titulado no le compite el
// proveedor al turno (lo que retrasaba el streaming en vivo).
func TestApp_TitleGeneratedAfterTurnStreams(t *testing.T) {
	rec := &recordingEmit{}
	app := newApp(llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "respuesta del agente"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	), rec.emit)

	turnDoneBeforeTitle := false
	app.titler = func(string) string {
		for _, ev := range rec.eventsOn("session:s1") {
			if ev.Kind == session.KindStepEnded {
				turnDoneBeforeTitle = true
			}
		}
		return "Titulo"
	}

	if err := app.SendPrompt("s1", "hola"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	if !turnDoneBeforeTitle {
		t.Fatal("el titulado corrio antes de cerrar el turno; debe correr despues para no competirle el proveedor")
	}
	sawText := false
	for _, ev := range rec.eventsOn("session:s1") {
		if ev.Kind == session.KindTextEnded && ev.Text == "respuesta del agente" {
			sawText = true
		}
	}
	if !sawText {
		t.Fatal("la respuesta del turno no llego al bus")
	}
}

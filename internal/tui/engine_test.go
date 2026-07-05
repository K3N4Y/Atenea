package tui

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"atenea/internal/llm"
	"atenea/internal/session"
)

// turnProvider implementa llm.Provider con un guion POR TURNO: la i-esima
// llamada a Stream reproduce el i-esimo guion. Contrasta con llm.FakeProvider,
// que repite el mismo guion en cada Stream (loop infinito si el guion pide
// tools). Si los guiones se acaban, emite un turno de solo StepEnded para que
// la corrida cierre limpia. Protegido con mutex: el runner llama Stream desde
// su propia goroutine.
type turnProvider struct {
	mu    sync.Mutex
	turns [][]llm.Event
	next  int
	// toolNames registra, por cada llamada a Stream, los nombres de las tools
	// anunciadas en el Request: la evidencia observable del modo del turno
	// (plan-mode anuncia present_plan y esconde bash/write).
	toolNames [][]string
	// messages registra, por cada llamada a Stream, el historial proyectado que
	// el runner envio al proveedor: la evidencia observable del orden en que los
	// eventos se materializaron como Messages.
	messages [][]llm.Message
	// delayStepEnded, si es > 0, duerme ese lapso entre un ToolCall del guion y
	// el StepEnded que lo sigue: espejo deterministico del ultimo chunk SSE que
	// llega tarde por la red mientras la tool ya se esta asentando localmente.
	delayStepEnded time.Duration
}

var _ llm.Provider = (*turnProvider)(nil)

func newTurnProvider(turns ...[]llm.Event) *turnProvider {
	return &turnProvider{turns: turns}
}

func (p *turnProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	names := make([]string, len(req.Tools))
	for i, def := range req.Tools {
		names[i] = def.Name
	}
	p.toolNames = append(p.toolNames, names)
	p.messages = append(p.messages, append([]llm.Message(nil), req.Messages...))
	script := []llm.Event{{Kind: llm.StepEnded}}
	if p.next < len(p.turns) {
		script = p.turns[p.next]
		p.next++
	}
	delay := p.delayStepEnded
	p.mu.Unlock()

	out := make(chan llm.Event)
	go func() {
		defer close(out)
		sawToolCall := false
		for _, ev := range script {
			if ev.Kind == llm.StepEnded && sawToolCall && delay > 0 {
				time.Sleep(delay)
			}
			select {
			case <-ctx.Done():
				return
			case out <- ev:
			}
			if ev.Kind == llm.ToolCall {
				sawToolCall = true
			}
		}
	}()
	return out, nil
}

// requestedTools devuelve una copia de los nombres de tools anunciados en cada
// llamada a Stream, en orden de llegada. Con mutex: el runner llama Stream
// desde su propia goroutine.
func (p *turnProvider) requestedTools() [][]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]string(nil), p.toolNames...)
}

// requestedMessages devuelve una copia del historial proyectado enviado en cada
// llamada a Stream, en orden de llegada. Con mutex: el runner llama Stream
// desde su propia goroutine.
func (p *turnProvider) requestedMessages() [][]llm.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]llm.Message(nil), p.messages...)
}

// nextMsg saca el siguiente mensaje del canal del engine, con timeout generoso
// para no ser flaky. Falla el test si el canal se cierra o vence el timeout.
func nextMsg(t *testing.T, ch <-chan tea.Msg, timeout time.Duration) tea.Msg {
	t.Helper()
	select {
	case <-time.After(timeout):
		t.Fatalf("timeout de %v esperando el siguiente mensaje del engine", timeout)
		return nil
	case msg, ok := <-ch:
		if !ok {
			t.Fatal("canal del engine cerrado antes de tiempo")
		}
		return msg
	}
}

// resolveUntilStopped entrega la decision del permiso via el API publico del
// engine, reintentando en segundo plano hasta que el test lo detenga. El
// reintento elimina una carrera real: el runner publica
// Tool.Permission.Requested ANTES de que gate.Ask registre la solicitud, asi
// que una entrega unica podria adelantarse al registro y perderse (el gate
// descarta decisiones sin Ask pendiente). Reintentar es inocuo: la entrega
// efectiva retira la solicitud del gate y los reintentos posteriores son no-op.
func resolveUntilStopped(e *Engine, sessionID, callID string, approved bool) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			e.ResolvePermission(sessionID, callID, approved)
			select {
			case <-done:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	return func() { close(done); wg.Wait() }
}

// gatedBashTurns arma el guion de dos turnos del escenario ask-before-run: el
// turno 1 pide la tool gateada bash con ese comando y el turno 2 responde texto.
func gatedBashTurns(command string) [][]llm.Event {
	input, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		panic(err) // un map[string]string siempre marshalea
	}
	return [][]llm.Event{
		{
			{Kind: llm.StepStarted},
			{Kind: llm.ToolCall, CallID: "c1", ToolName: "bash", Input: input},
			{Kind: llm.StepEnded},
		},
		{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: "listo"},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		},
	}
}

// collectUntilRunDone consume el canal del engine en el goroutine del test:
// acumula los EventMsg hasta ver el RunDoneMsg y los devuelve, tomando cada
// mensaje con nextMsg (que falla si el canal se cierra o vence el timeout).
// onEvent (opcional) se invoca con cada evento al llegar; los tests lo usan
// para reaccionar a mitad de corrida (resolver un permiso, detener la sesion).
func collectUntilRunDone(t *testing.T, ch <-chan tea.Msg, timeout time.Duration, onEvent func(session.SessionEvent)) ([]session.SessionEvent, RunDoneMsg) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var events []session.SessionEvent
	for {
		switch m := nextMsg(t, ch, time.Until(deadline)).(type) {
		case EventMsg:
			ev := session.SessionEvent(m)
			events = append(events, ev)
			if onEvent != nil {
				onEvent(ev)
			}
		case RunDoneMsg:
			return events, m
		default:
			t.Fatalf("mensaje inesperado en el canal del engine: %T", m)
		}
	}
}

// lastEvent devuelve el ultimo evento con ese Kind y CallID, o nil si no llego.
func lastEvent(events []session.SessionEvent, kind session.EventKind, callID string) *session.SessionEvent {
	var found *session.SessionEvent
	for i, ev := range events {
		if ev.Kind == kind && ev.CallID == callID {
			found = &events[i]
		}
	}
	return found
}

// writeSkill crea <root>/.atenea/skills/<name>/SKILL.md con el frontmatter
// name/description (mismo formato que los tests de internal/skill): la fuente
// de la que el wiring deriva los slash-commands del composer.
func writeSkill(t *testing.T, root, name, desc string) {
	t.Helper()
	dir := filepath.Join(root, ".atenea", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	front := "---\nname: " + name + "\ndescription: " + desc + "\n---\ncuerpo de " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(front), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

func TestEngine_ExposesCommandsFromSkills(t *testing.T) {
	// El Engine expone los slash-commands derivados de las skills descubiertas
	// (espejo de App.ListCommands): la TUI los cablea al menu "/" del composer.
	// Se asierta CONTENCION, no igualdad: el wiring tambien descubre las skills
	// globales del home del usuario.
	root := t.TempDir()
	writeSkill(t, root, "saluda", "saluda con estilo")

	e := NewEngine(EngineConfig{Root: root, Provider: llm.NewFakeProvider(), Store: session.NewMemoryStore()})

	cmds := e.Commands()
	for _, c := range cmds {
		if c.Name == "saluda" {
			if c.Description != "saluda con estilo" {
				t.Fatalf("Commands() dio saluda con Description = %q, quiero %q", c.Description, "saluda con estilo")
			}
			return
		}
	}
	t.Fatalf("Commands() = %v, debe contener el comando %q derivado de la skill del proyecto", cmds, "saluda")
}

func TestEngine_ProjectFilesListsWorkspace(t *testing.T) {
	// El Engine lista los archivos del workspace (rutas relativas a la raiz)
	// para el @-menu del composer (espejo de App.ListProjectFiles). El glob
	// real usa ripgrep: sin rg instalado el caso se salta.
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skipf("rg unavailable: %v", err)
	}
	root := t.TempDir()
	for _, f := range []string{"a.go", filepath.Join("sub", "b.txt")} {
		path := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte("contenido"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	e := NewEngine(EngineConfig{Root: root, Provider: llm.NewFakeProvider(), Store: session.NewMemoryStore()})

	files, err := e.ProjectFiles()
	if err != nil {
		t.Fatalf("ProjectFiles() = %v, se esperaba nil", err)
	}
	for _, want := range []string{"a.go", filepath.Join("sub", "b.txt")} {
		if !slices.Contains(files, want) {
			t.Fatalf("ProjectFiles() = %v, debe contener la ruta relativa %q", files, want)
		}
	}
}

func TestEngine_SendPromptExpandsSlashCommand(t *testing.T) {
	// SendPrompt expande un slash-command antes de encolarlo (espejo de
	// App.expandCommand): el Message user promovido lleva el prompt EXPANDIDO
	// de la plantilla de la skill, no el literal "/saluda ...". Un prompt que
	// no es comando pasa sin cambios. Cubre tambien SendPlanPrompt: ambos
	// comparten el camino comun de send.
	root := t.TempDir()
	writeSkill(t, root, "saluda", "saluda con estilo")
	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "hola"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)
	e := NewEngine(EngineConfig{Root: root, Provider: fake, Store: session.NewMemoryStore()})

	// lastUserPrompt corre una corrida completa y devuelve el ultimo Message
	// user promovido entre sus eventos.
	lastUserPrompt := func(sessionID, text string) string {
		t.Helper()
		if err := e.SendPrompt(sessionID, text); err != nil {
			t.Fatalf("SendPrompt(%s, %s) = %v, se esperaba nil", sessionID, text, err)
		}
		events, done := collectUntilRunDone(t, e.Events(), 10*time.Second, nil)
		if done.Err != "" {
			t.Fatalf("RunDoneMsg.Err = %q, se esperaba corrida limpia", done.Err)
		}
		prompt := ""
		for _, ev := range events {
			if ev.Message != nil && ev.Message.Role == session.RoleUser {
				prompt = ev.Message.Text
			}
		}
		return prompt
	}

	// La plantilla de FromSkills es `Usa la skill %q.\n\n$ARGUMENTS`.
	want := "Usa la skill \"saluda\".\n\nhola mundo"
	if got := lastUserPrompt("s1", "/saluda hola mundo"); got != want {
		t.Fatalf("Message user promovido = %q, quiero el prompt expandido %q, no el literal del comando", got, want)
	}

	// Un prompt que no es comando pasa sin transformar.
	if got := lastUserPrompt("s2", "hola normal"); got != "hola normal" {
		t.Fatalf("Message user promovido = %q, un prompt que no es comando debe pasar sin cambios (%q)", got, "hola normal")
	}
}

func TestEngine_StreamsSessionEventsAndSignalsRunDone(t *testing.T) {
	// Un turno de solo texto: el guion completo de una corrida limpia.
	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "Hola desde el engine"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)

	e := NewEngine(EngineConfig{Root: t.TempDir(), Provider: fake, Store: session.NewMemoryStore()})

	if err := e.SendPrompt("s1", "hola"); err != nil {
		t.Fatalf("SendPrompt(s1, hola) = %v, se esperaba nil", err)
	}

	events, done := collectUntilRunDone(t, e.Events(), 5*time.Second, nil)

	var sawUserPrompt bool // (a) el prompt promovido a mensaje user durable
	var sawTextDelta bool  // (b) al menos un Text.Delta
	var deltas strings.Builder
	var sawStepEnded bool // (c) el cierre del turno
	for _, ev := range events {
		if ev.Message != nil && ev.Message.Role == session.RoleUser && ev.Message.Text == "hola" {
			sawUserPrompt = true
		}
		if ev.Kind == session.KindTextDelta {
			sawTextDelta = true
			deltas.WriteString(ev.Text)
		}
		if ev.Kind == session.KindStepEnded {
			sawStepEnded = true
		}
	}

	if !sawUserPrompt {
		t.Errorf("no llego el mensaje user promovido con Text %q entre %d eventos", "hola", len(events))
	}
	if !sawTextDelta {
		t.Errorf("no llego ningun evento %s", session.KindTextDelta)
	} else if got := deltas.String(); !strings.Contains(got, "Hola desde el engine") {
		t.Errorf("texto acumulado de %s = %q, debe contener %q", session.KindTextDelta, got, "Hola desde el engine")
	}
	if !sawStepEnded {
		t.Errorf("no llego ningun evento %s", session.KindStepEnded)
	}
	if done.Err != "" {
		t.Errorf("RunDoneMsg.Err = %q, se esperaba corrida limpia (Err vacio)", done.Err)
	}
}

func TestEngine_GatedBashApprovedRunsAndSettles(t *testing.T) {
	provider := newTurnProvider(gatedBashTurns("echo hola-gate")...)
	e := NewEngine(EngineConfig{Root: t.TempDir(), Provider: provider, Store: session.NewMemoryStore()})

	if err := e.SendPrompt("s1", "corre el comando"); err != nil {
		t.Fatalf("SendPrompt(s1, corre el comando) = %v, se esperaba nil", err)
	}

	// Al ver la solicitud de permiso, el usuario APRUEBA la tool.
	events, done := collectUntilRunDone(t, e.Events(), 10*time.Second, func(ev session.SessionEvent) {
		if ev.Kind == session.KindToolPermissionRequested && ev.CallID == "c1" {
			t.Cleanup(resolveUntilStopped(e, ev.SessionID, "c1", true))
		}
	})

	success := lastEvent(events, session.KindToolSuccess, "c1")
	if success == nil {
		t.Fatalf("no llego ningun %s de c1 entre %d eventos: la tool aprobada debe ejecutarse y asentarse", session.KindToolSuccess, len(events))
	}
	if !strings.Contains(success.Text, "hola-gate") {
		t.Errorf("Tool.Success de c1 con Text = %q, debe contener %q (bash ejecuto de verdad)", success.Text, "hola-gate")
	}
	if done.Err != "" {
		t.Errorf("RunDoneMsg.Err = %q, se esperaba corrida limpia (Err vacio)", done.Err)
	}
}

func TestEngine_GatedBashDeniedFailsWithoutRunning(t *testing.T) {
	root := t.TempDir()
	forbidden := filepath.Join(root, "no-debe-existir")
	provider := newTurnProvider(gatedBashTurns("touch " + forbidden)...)
	e := NewEngine(EngineConfig{Root: root, Provider: provider, Store: session.NewMemoryStore()})

	if err := e.SendPrompt("s1", "corre el comando"); err != nil {
		t.Fatalf("SendPrompt(s1, corre el comando) = %v, se esperaba nil", err)
	}

	// Al ver la solicitud de permiso, el usuario DENIEGA la tool.
	events, done := collectUntilRunDone(t, e.Events(), 10*time.Second, func(ev session.SessionEvent) {
		if ev.Kind == session.KindToolPermissionRequested && ev.CallID == "c1" {
			t.Cleanup(resolveUntilStopped(e, ev.SessionID, "c1", false))
		}
	})

	if ev := lastEvent(events, session.KindToolSuccess, "c1"); ev != nil {
		t.Fatalf("llego %s de c1 con Text %q: una tool denegada NO debe ejecutarse", session.KindToolSuccess, ev.Text)
	}
	failed := lastEvent(events, session.KindToolFailed, "c1")
	if failed == nil {
		t.Fatalf("no llego ningun %s de c1 entre %d eventos: la denegacion debe asentar la tool como fallida", session.KindToolFailed, len(events))
	}
	if !strings.Contains(strings.ToLower(failed.Error), "deni") {
		t.Errorf("Tool.Failed de c1 con Error = %q, debe mencionar la denegacion", failed.Error)
	}
	if done.Err != "" {
		t.Errorf("RunDoneMsg.Err = %q, la denegacion no es un fallo de la corrida (Err vacio)", done.Err)
	}
	// La prueba dura de que bash NO corrio: el archivo que el comando tocaria
	// no debe existir tras el fin de la corrida.
	if _, err := os.Stat(forbidden); !os.IsNotExist(err) {
		t.Errorf("os.Stat(%q) = %v, el archivo no debe existir: la tool denegada no debe ejecutar el comando", forbidden, err)
	}
}

func TestEngine_StopUnblocksPendingPermission(t *testing.T) {
	// Un solo turno: la tool gateada queda esperando aprobacion para siempre;
	// Stop debe desbloquearla y cerrar la corrida limpia.
	provider := newTurnProvider([]llm.Event{
		{Kind: llm.StepStarted},
		{Kind: llm.ToolCall, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"echo bloqueado"}`)},
		{Kind: llm.StepEnded},
	})
	e := NewEngine(EngineConfig{Root: t.TempDir(), Provider: provider, Store: session.NewMemoryStore()})

	if err := e.SendPrompt("s1", "corre el comando"); err != nil {
		t.Fatalf("SendPrompt(s1, corre el comando) = %v, se esperaba nil", err)
	}

	// En vez de decidir, el usuario detiene la corrida.
	events, done := collectUntilRunDone(t, e.Events(), 10*time.Second, func(ev session.SessionEvent) {
		if ev.Kind == session.KindToolPermissionRequested && ev.CallID == "c1" {
			e.Stop("s1")
		}
	})

	if lastEvent(events, session.KindToolFailed, "c1") == nil {
		t.Errorf("no llego ningun %s de c1: Stop debe asentar la call pendiente como interrumpida", session.KindToolFailed)
	}
	if done.Err != "" {
		t.Errorf("RunDoneMsg.Err = %q, una cancelacion deliberada es cierre limpio (Err vacio)", done.Err)
	}
}

func TestEngine_AcceptPlanRunsImplementationInNormalMode(t *testing.T) {
	// TRIANGULATE: AcceptPlan debe volver la sesion a modo normal y promover el
	// prompt fijo de implementacion como prompt del usuario, arrancando la
	// corrida (espejo de App.AcceptPlan). Evidencia observable: el Request del
	// turno de AcceptPlan vuelve a anunciar bash (modo normal) y entre los
	// eventos llega el Message user con el texto del prompt fijo.
	textTurn := func(text string) []llm.Event {
		return []llm.Event{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: text},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		}
	}
	provider := newTurnProvider(textTurn("plan listo"), textTurn("implementado"))
	e := NewEngine(EngineConfig{Root: t.TempDir(), Provider: provider, Store: session.NewMemoryStore()})

	// Corrida de plan previa: deja la sesion en plan-mode con el plan presentado.
	if err := e.SendPlanPrompt("s1", "planea"); err != nil {
		t.Fatalf("SendPlanPrompt(s1, planea) = %v, se esperaba nil", err)
	}
	if _, done := collectUntilRunDone(t, e.Events(), 10*time.Second, nil); done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q, se esperaba corrida limpia en plan-mode", done.Err)
	}
	planCalls := len(provider.requestedTools())

	// El usuario acepta el plan: debe arrancar la corrida de implementacion.
	if err := e.AcceptPlan("s1"); err != nil {
		t.Fatalf("AcceptPlan(s1) = %v, se esperaba nil", err)
	}
	events, done := collectUntilRunDone(t, e.Events(), 10*time.Second, nil)
	if done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q, se esperaba corrida limpia al ejecutar el plan", done.Err)
	}

	calls := provider.requestedTools()
	if len(calls) <= planCalls {
		t.Fatalf("el provider registro %d llamadas a Stream tras AcceptPlan (habia %d): aceptar el plan debe arrancar una corrida nueva", len(calls), planCalls)
	}
	acceptTools := calls[len(calls)-1]
	if !slices.Contains(acceptTools, "bash") {
		t.Errorf("tools del turno de AcceptPlan = %v, debe incluir %q: aceptar el plan vuelve la sesion a modo normal", acceptTools, "bash")
	}

	var prompt *session.Message
	for _, ev := range events {
		if ev.Message != nil && ev.Message.Role == session.RoleUser {
			msg := *ev.Message
			prompt = &msg
		}
	}
	if prompt == nil {
		t.Fatalf("no llego ningun Message user entre %d eventos: AcceptPlan debe promover el prompt fijo de implementacion", len(events))
	}
	if !strings.Contains(prompt.Text, "aprobado") {
		t.Errorf("Message user promovido = %q, debe contener %q (el prompt fijo de implementacion)", prompt.Text, "aprobado")
	}
}

func TestEngine_SendPlanPromptRunsInPlanMode(t *testing.T) {
	// TRIANGULATE: SendPlanPrompt debe correr el turno en plan-mode REAL (como
	// en la app Wails), no delegar en SendPrompt. La evidencia observable son
	// las tools que el runner anuncia al modelo en el Request de cada turno:
	// plan-mode anuncia present_plan y esconde bash/write; el modo es por envio,
	// asi que un SendPrompt posterior en la MISMA sesion vuelve a anunciar bash.
	textTurn := func(text string) []llm.Event {
		return []llm.Event{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: text},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		}
	}
	provider := newTurnProvider(textTurn("plan listo"), textTurn("hecho"))
	e := NewEngine(EngineConfig{Root: t.TempDir(), Provider: provider, Store: session.NewMemoryStore()})

	// Envio en plan-mode: el turno debe anunciar las tools de planificacion.
	if err := e.SendPlanPrompt("s1", "planea x"); err != nil {
		t.Fatalf("SendPlanPrompt(s1, planea x) = %v, se esperaba nil", err)
	}
	if _, done := collectUntilRunDone(t, e.Events(), 10*time.Second, nil); done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q, se esperaba corrida limpia en plan-mode", done.Err)
	}
	calls := provider.requestedTools()
	if len(calls) == 0 {
		t.Fatalf("el provider no registro ninguna llamada a Stream tras la corrida de plan")
	}
	planTools := calls[len(calls)-1]
	if !slices.Contains(planTools, "present_plan") {
		t.Errorf("tools del turno de plan = %v, debe incluir %q: SendPlanPrompt debe correr en plan-mode real", planTools, "present_plan")
	}
	for _, forbidden := range []string{"bash", "write"} {
		if slices.Contains(planTools, forbidden) {
			t.Errorf("tools del turno de plan = %v, NO debe incluir %q: plan-mode es de solo lectura", planTools, forbidden)
		}
	}

	// Envio normal posterior en la MISMA sesion: el modo es por envio (espejo
	// de la app Wails) y el turno vuelve a anunciar las tools de build.
	if err := e.SendPrompt("s1", "hazlo"); err != nil {
		t.Fatalf("SendPrompt(s1, hazlo) = %v, se esperaba nil", err)
	}
	if _, done := collectUntilRunDone(t, e.Events(), 10*time.Second, nil); done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q, se esperaba corrida limpia en modo normal", done.Err)
	}
	calls = provider.requestedTools()
	if len(calls) < 2 {
		t.Fatalf("el provider registro %d llamadas a Stream, se esperaban al menos 2 (turno de plan + turno normal)", len(calls))
	}
	buildTools := calls[len(calls)-1]
	if !slices.Contains(buildTools, "bash") {
		t.Errorf("tools del turno normal = %v, debe incluir %q: el modo es por envio y SendPrompt vuelve a build", buildTools, "bash")
	}
}

func TestEngine_ToolResultNeverPrecedesAssistantMessageInHistory(t *testing.T) {
	// RED (bug real visto con OpenRouter/Cohere): cuando el modelo responde SOLO
	// con una tool call que falla al instante (read con ruta absoluta: muere en
	// la validacion de sandboxJoin, sin I/O), el Tool.Failed (que materializa el
	// Message role=tool) puede persistirse ANTES que el Step.Ended (que
	// materializa el Message assistant con los tool_calls), porque el runner
	// asienta la tool en una goroutine concurrente mientras el StepEnded aun
	// viaja por la red. El historial proyectado queda `user, tool, assistant` y
	// el siguiente request al provider devuelve 400: "tool call id not found in
	// previous tool calls". El delay del provider reproduce esa carrera de forma
	// deterministica: el ultimo chunk SSE (StepEnded) llega ~100ms tarde.
	provider := newTurnProvider(
		[]llm.Event{
			{Kind: llm.StepStarted},
			{Kind: llm.ToolCall, CallID: "c1", ToolName: "read", Input: json.RawMessage(`{"path":"/etc/fuera"}`)},
			{Kind: llm.StepEnded},
		},
		[]llm.Event{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: "no pude leerlo"},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		},
	)
	provider.delayStepEnded = 100 * time.Millisecond
	e := NewEngine(EngineConfig{Root: t.TempDir(), Provider: provider, Store: session.NewMemoryStore()})

	if err := e.SendPrompt("s1", "lee eso"); err != nil {
		t.Fatalf("SendPrompt(s1, lee eso) = %v, se esperaba nil", err)
	}
	if _, done := collectUntilRunDone(t, e.Events(), 10*time.Second, nil); done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q, se esperaba corrida limpia (la tool fallida no es fallo de la corrida)", done.Err)
	}

	calls := provider.requestedMessages()
	if len(calls) < 2 {
		t.Fatalf("el provider registro %d llamadas a Stream, se esperaban al menos 2 (turno de la tool + turno de cierre)", len(calls))
	}
	history := calls[1] // el historial proyectado que ve el provider en el turno 2

	// La secuencia de roles proyectada, para un mensaje de fallo legible.
	roles := make([]string, len(history))
	for i, m := range history {
		roles[i] = m.Role
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			roles[i] = "assistant(tool_calls)"
		}
		if m.Role == "tool" {
			roles[i] = "tool(" + m.ToolCallID + ")"
		}
	}

	assistantIdx, toolIdx := -1, -1
	for i, m := range history {
		if m.Role == "assistant" && assistantIdx < 0 {
			for _, tc := range m.ToolCalls {
				if tc.ID == "c1" {
					assistantIdx = i
				}
			}
		}
		if m.Role == "tool" && m.ToolCallID == "c1" && toolIdx < 0 {
			toolIdx = i
		}
	}

	if assistantIdx < 0 {
		t.Fatalf("el historial del turno 2 no tiene ningun Message assistant con la tool call c1; secuencia de roles proyectada: %v", roles)
	}
	if toolIdx < 0 {
		t.Fatalf("el historial del turno 2 no tiene ningun Message role=tool con ToolCallID c1; secuencia de roles proyectada: %v", roles)
	}
	if toolIdx < assistantIdx {
		t.Fatalf("el Message role=tool de c1 (indice %d) precede al Message assistant con sus tool_calls (indice %d); un provider real lo rechaza con 400 (tool call id not found in previous tool calls); secuencia de roles proyectada: %v", toolIdx, assistantIdx, roles)
	}
}

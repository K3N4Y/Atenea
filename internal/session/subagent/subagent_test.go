package subagent

// Tests del TaskTool: la tool "task" levanta un subagente hijo (un runner con
// MemoryStore en memoria) para una tarea delegada y devuelve su reporte final
// (el ultimo texto del asistente). Vive en internal/session/subagent porque
// internal/session/runner importa internal/tool, asi que la tool no puede vivir
// en internal/tool (ciclo); este paquete si puede importar ambos.

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"atenea/internal/agent"
	"atenea/internal/llm"
	"atenea/internal/session/runner"
	"atenea/internal/tool"
)

// scriptedProvider devuelve un guion DISTINTO por turno: el loop del runner llama
// Stream una vez por turno y el FakeProvider replayea SIEMPRE el mismo guion, asi
// que para encadenar turnos con respuestas distintas (tool en el turno 1, texto en
// el turno 2) hace falta un proveedor que cambie de guion turno a turno. Copia el
// patron de internal/session/runner/run_test.go. El i-esimo Stream reproduce
// turns[i] (o un guion vacio si se agotaron) delegando en un FakeProvider fresco.
type scriptedProvider struct {
	turns [][]llm.Event
	calls int
	mu    sync.Mutex
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
	p.mu.Unlock()
	return llm.NewFakeProvider(script...).Stream(ctx, req)
}

// idCounter devuelve un generador de IDs incremental ("m1","m2",...) identico al
// de run_test.go: el runner hijo lo usa para el ID del mensaje de usuario
// promovido y para el del asistente, asi no colisionan. Usa strconv.Itoa para
// soportar mas de nueve IDs.
func idCounter() func() string {
	n := 0
	return func() string {
		n++
		return "m" + strconv.Itoa(n)
	}
}

// TestTaskTool_RunsChildAndReturnsReport fija la API del TaskTool: NewTaskTool
// arma la tool "task" con las defs de subagente, el provider hijo, el registry
// hijo y un generador de IDs. Execute parsea {subagent_type, prompt}, busca la
// def por nombre, levanta el hijo (un runner con MemoryStore en memoria) y
// devuelve tool.Result{Output: <reporte>}, donde el reporte es el ultimo texto
// del asistente del hijo.
//
// El test referencia simbolos que aun NO existen (NewTaskTool, *TaskTool): en Go
// eso es un fallo de compilacion del paquete de test, y ese es el RED honesto de
// este milestone.
func TestTaskTool_RunsChildAndReturnsReport(t *testing.T) {
	ctx := context.Background()

	// Provider hijo: guion de SOLO TEXTO que emite el reporte del subagente.
	prov := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "informe del subagente"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)

	children := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	defs := []agent.Def{{Name: "reviewer", Description: "Revisa codigo", Prompt: "Eres un revisor."}}

	tt := NewTaskTool(defs, prov, children, idCounter())

	if tt.Name() != "task" {
		t.Errorf("tt.Name() = %q, quiero %q", tt.Name(), "task")
	}

	res, err := tt.Execute(ctx, json.RawMessage(`{"subagent_type":"reviewer","prompt":"revisa esto"}`))
	if err != nil {
		t.Fatalf("Execute error inesperado: %v", err)
	}

	// El hijo corrio y su texto final volvio como reporte en el Output.
	if !strings.Contains(res.Output, "informe del subagente") {
		t.Errorf("res.Output = %q, quiero que contenga %q (reporte del hijo)", res.Output, "informe del subagente")
	}
}

// TestTaskTool_UnknownSubagentType afirma que un subagent_type que no esta en el
// catalogo devuelve un error accionable (no nil) que enumera los disponibles, sin
// llegar a correr el hijo. El provider de solo-texto nunca deberia consumirse: si
// el TaskTool levantara el runner igual, este test seguiria pasando, pero la regla
// que tumba es que el error mencione el catalogo ("reviewer").
func TestTaskTool_UnknownSubagentType(t *testing.T) {
	ctx := context.Background()

	prov := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "no deberia correr"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)

	children := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	defs := []agent.Def{{Name: "reviewer", Description: "Revisa codigo", Prompt: "Eres un revisor."}}

	tt := NewTaskTool(defs, prov, children, idCounter())

	_, err := tt.Execute(ctx, json.RawMessage(`{"subagent_type":"noexiste","prompt":"x"}`))
	if err == nil {
		t.Fatalf("Execute devolvio nil para un subagent_type desconocido, quiero un error")
	}
	// El error enumera los disponibles: debe mencionar "reviewer".
	if !strings.Contains(err.Error(), "reviewer") {
		t.Errorf("error = %q, quiero que enumere los disponibles (contenga %q)", err.Error(), "reviewer")
	}
}

// TestTaskTool_InvalidJSON afirma que un input que no es JSON valido devuelve un
// error (no nil). Tumba una version que no valide el input antes de buscar la def.
func TestTaskTool_InvalidJSON(t *testing.T) {
	ctx := context.Background()

	prov := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.StepEnded},
	)
	children := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	defs := []agent.Def{{Name: "reviewer", Description: "Revisa codigo", Prompt: "Eres un revisor."}}

	tt := NewTaskTool(defs, prov, children, idCounter())

	_, err := tt.Execute(ctx, json.RawMessage("no-es-json"))
	if err == nil {
		t.Fatalf("Execute devolvio nil para un input no-JSON, quiero un error")
	}
}

// TestTaskTool_PropagatesRunnerError afirma que si el runner hijo falla, el TaskTool
// propaga ese error sin tragarselo. El provider replayable SIEMPRE pide una tool, asi
// que el hijo agota MaxSteps y devuelve *runner.StepLimitExceededError; errors.As debe
// reconocerlo a traves del Execute. Tumba una version que oculte el error del hijo.
func TestTaskTool_PropagatesRunnerError(t *testing.T) {
	ctx := context.Background()

	// Guion replayable que SIEMPRE pide una echo: cada turno continua -> step limit.
	prov := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.ToolCall, CallID: "c1", ToolName: "echo", Input: json.RawMessage(`{"text":"x"}`)},
		llm.Event{Kind: llm.StepEnded},
	)

	children := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	defs := []agent.Def{{Name: "looper", Tools: []string{"echo"}, Description: "loopea", Prompt: "loop"}}

	tt := NewTaskTool(defs, prov, children, idCounter())

	_, err := tt.Execute(ctx, json.RawMessage(`{"subagent_type":"looper","prompt":"loop"}`))
	if err == nil {
		t.Fatalf("Execute devolvio nil, quiero el error del runner hijo propagado")
	}
	var target *runner.StepLimitExceededError
	if !errors.As(err, &target) {
		t.Fatalf("Execute error = %v, quiero un *runner.StepLimitExceededError reconocible con errors.As", err)
	}
}

// TestTaskTool_DescriptionListsAvailable afirma que Description() lista el catalogo
// real (nombre y descripcion de cada subagente) en orden alfabetico, no un texto
// estatico. Con "reviewer" y "explorer", ambos nombres y ambas descripciones deben
// aparecer, y "explorer" antes que "reviewer" (orden alfabetico). Tumba una
// descripcion fija que no recorra el catalogo o que no lo ordene.
func TestTaskTool_DescriptionListsAvailable(t *testing.T) {
	prov := llm.NewFakeProvider()
	children := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	defs := []agent.Def{
		{Name: "reviewer", Description: "Revisa codigo"},
		{Name: "explorer", Description: "Explora"},
	}

	tt := NewTaskTool(defs, prov, children, idCounter())
	desc := tt.Description()

	for _, want := range []string{"reviewer", "Revisa codigo", "explorer", "Explora"} {
		if !strings.Contains(desc, want) {
			t.Errorf("Description() no contiene %q; desc = %q", want, desc)
		}
	}
	// Orden alfabetico: explorer antes que reviewer.
	if strings.Index(desc, "explorer") >= strings.Index(desc, "reviewer") {
		t.Errorf("Description() no esta en orden alfabetico: %q debe ir antes que %q; desc = %q", "explorer", "reviewer", desc)
	}
}

// TestTaskTool_ChildUsesToolThenReportsFinalText ejercita el LOOP real multi-turno
// del hijo: turno 1 pide una echo (el loop continua por la tool local), turno 2 es
// solo texto "informe final" y cierra. El reporte devuelto es el ULTIMO texto del
// asistente, asi que debe ser "informe final", no el del primer turno. Tumba una
// version que devuelva el primer turno, que no continue el loop, o que no extraiga
// el ultimo asistente.
func TestTaskTool_ChildUsesToolThenReportsFinalText(t *testing.T) {
	ctx := context.Background()

	prov := &scriptedProvider{turns: [][]llm.Event{
		{
			{Kind: llm.StepStarted},
			{Kind: llm.ToolCall, CallID: "c1", ToolName: "echo", Input: json.RawMessage(`{"text":"pong"}`)},
			{Kind: llm.StepEnded},
		},
		{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: "informe final"},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		},
	}}

	children := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	defs := []agent.Def{{Name: "worker", Tools: []string{"echo"}, Description: "trabaja", Prompt: "Eres un worker."}}

	tt := NewTaskTool(defs, prov, children, idCounter())

	res, err := tt.Execute(ctx, json.RawMessage(`{"subagent_type":"worker","prompt":"haz algo"}`))
	if err != nil {
		t.Fatalf("Execute error inesperado: %v", err)
	}
	if res.Output != "informe final" {
		t.Errorf("res.Output = %q, quiero %q (el reporte es el ultimo texto del asistente, tras usar la tool)", res.Output, "informe final")
	}
}

// spyProvider captura las tools materializadas que se le ofrecen al hijo (req.Tools)
// y la profundidad del ctx, y delega en un turno de solo texto para que Run cierre.
type spyProvider struct {
	toolNames []string
	depth     int
}

func (p *spyProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	for _, td := range req.Tools {
		p.toolNames = append(p.toolNames, td.Name)
	}
	p.depth = depthFrom(ctx)
	return llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "ok"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	).Stream(ctx, req)
}

// taskStub es una tool con nombre "task" para verificar si se le ofrece al hijo.
type taskStub struct{}

func (taskStub) Name() string            { return "task" }
func (taskStub) Description() string     { return "stub task" }
func (taskStub) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (taskStub) Execute(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.Result{}, nil
}

// TestTaskTool_StripsTaskToolAtMaxDepth fija el control de recursion (S3): cuando el
// TaskTool levanta un hijo que correra en una profundidad childDepth >= maxDepth, al
// hijo NO se le ofrece la tool "task" (se quita de sus permisos, asi no aparece en
// las tools materializadas que ve el modelo). La profundidad se propaga por el ctx.
//
// El test referencia simbolos que aun NO existen (SetMaxDepth, depthFrom): en Go eso
// es un fallo de compilacion del paquete de test, y ese es el RED honesto.
func TestTaskTool_StripsTaskToolAtMaxDepth(t *testing.T) {
	prov := &spyProvider{}

	children := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{}, taskStub{})
	defs := []agent.Def{{Name: "worker", Tools: []string{"task", "echo"}}}

	tt := NewTaskTool(defs, prov, children, idCounter())
	tt.SetMaxDepth(1) // childDepth del primer hijo = 1 >= 1 -> "task" se elimina

	_, err := tt.Execute(context.Background(), json.RawMessage(`{"subagent_type":"worker","prompt":"haz"}`))
	if err != nil {
		t.Fatalf("Execute error inesperado: %v", err)
	}

	// El hijo conserva "echo" pero "task" fue removida por estar en max depth.
	if !slices.Contains(prov.toolNames, "echo") {
		t.Errorf("toolNames = %v, quiero que contenga %q", prov.toolNames, "echo")
	}
	if slices.Contains(prov.toolNames, "task") {
		t.Errorf("toolNames = %v, NO quiero que contenga %q en max depth", prov.toolNames, "task")
	}
}

// TestTaskTool_KeepsTaskToolBelowMaxDepth es el contrapunto del strip: con
// maxDepth=2 y ctx sin profundidad (entrante 0 -> childDepth=1 < 2) el hijo aun
// puede anidar, asi que SI se le ofrece la tool "task" (ademas de "echo"). Tumba
// una version que siempre quite "task" o que use un cap mal orientado (>= con tope
// distinto) que la quite por debajo del limite.
func TestTaskTool_KeepsTaskToolBelowMaxDepth(t *testing.T) {
	prov := &spyProvider{}

	children := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{}, taskStub{})
	defs := []agent.Def{{Name: "worker", Tools: []string{"task", "echo"}}}

	tt := NewTaskTool(defs, prov, children, idCounter())
	tt.SetMaxDepth(2) // childDepth del primer hijo = 1 < 2 -> "task" se conserva

	_, err := tt.Execute(context.Background(), json.RawMessage(`{"subagent_type":"worker","prompt":"haz"}`))
	if err != nil {
		t.Fatalf("Execute error inesperado: %v", err)
	}

	// Por debajo del tope el hijo recibe ambas tools.
	if !slices.Contains(prov.toolNames, "echo") {
		t.Errorf("toolNames = %v, quiero que contenga %q", prov.toolNames, "echo")
	}
	if !slices.Contains(prov.toolNames, "task") {
		t.Errorf("toolNames = %v, quiero que contenga %q por debajo de max depth", prov.toolNames, "task")
	}
}

// TestTaskTool_DepthIncrementsFromContext fija que la profundidad entrante del ctx
// se respeta y se incrementa al correr el hijo: con ctx en profundidad 1 el hijo
// corre (y ve en su ctx) profundidad 2. maxDepth alto para no interferir con el
// strip. Tumba una version que ignore la profundidad entrante (siempre arrancaria
// en 0/1) o que no la incremente ni la propague al ctx del hijo.
func TestTaskTool_DepthIncrementsFromContext(t *testing.T) {
	prov := &spyProvider{}

	children := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	defs := []agent.Def{{Name: "worker", Tools: []string{"echo"}}}

	tt := NewTaskTool(defs, prov, children, idCounter())
	tt.SetMaxDepth(5) // alto: no nos interesa el strip aqui, solo la profundidad

	ctx := withDepth(context.Background(), 1)
	_, err := tt.Execute(ctx, json.RawMessage(`{"subagent_type":"worker","prompt":"haz"}`))
	if err != nil {
		t.Fatalf("Execute error inesperado: %v", err)
	}

	// Entrante 1 -> el hijo corre en profundidad 2 y la ve en su ctx.
	if prov.depth != 2 {
		t.Errorf("prov.depth = %d, quiero 2 (entrante 1 incrementada y propagada)", prov.depth)
	}
}

// TestTaskTool_DefaultIncomingDepthIsZero fija el caso del agente raiz: con un ctx
// sin profundidad, depthFrom devuelve 0, asi que el primer hijo corre (y ve) en
// profundidad 1. Tumba un depthFrom cuyo default no sea 0 (p.ej. 1) o un Execute
// que no arranque el primer hijo en 1.
func TestTaskTool_DefaultIncomingDepthIsZero(t *testing.T) {
	prov := &spyProvider{}

	children := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	defs := []agent.Def{{Name: "worker", Tools: []string{"echo"}}}

	tt := NewTaskTool(defs, prov, children, idCounter())
	tt.SetMaxDepth(5) // alto: aqui solo importa la profundidad observada

	_, err := tt.Execute(context.Background(), json.RawMessage(`{"subagent_type":"worker","prompt":"haz"}`))
	if err != nil {
		t.Fatalf("Execute error inesperado: %v", err)
	}

	// ctx sin valor -> depthFrom = 0 -> primer hijo en profundidad 1.
	if prov.depth != 1 {
		t.Errorf("prov.depth = %d, quiero 1 (ctx sin profundidad: default 0, primer hijo en 1)", prov.depth)
	}
}

// safeIDCounter es como idCounter pero seguro para uso concurrente (varios Execute
// en paralelo comparten el generador del TaskTool).
func safeIDCounter() func() string {
	var mu sync.Mutex
	n := 0
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		n++
		return "m" + strconv.Itoa(n)
	}
}

// concurrencyProbe cuenta cuantos subagentes corren a la vez (su Stream se llama
// una vez por hijo, ya pasado el semaforo). Registra el pico, avisa que entro por
// `entered` y se bloquea en `release` hasta que el test lo suelte. Asi el test
// puede forzar exactamente N hijos simultaneos y comprobar el cap.
type concurrencyProbe struct {
	cur, peak atomic.Int32
	entered   chan struct{}
	release   chan struct{}
}

func (p *concurrencyProbe) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	c := p.cur.Add(1)
	for {
		old := p.peak.Load()
		if c <= old || p.peak.CompareAndSwap(old, c) {
			break
		}
	}
	p.entered <- struct{}{}
	<-p.release
	p.cur.Add(-1)
	return llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "ok"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	).Stream(ctx, req)
}

// TestTaskTool_CapsConcurrentSubagents fija el tope de concurrencia (S6): el
// TaskTool comparte un semaforo de tamano maxConcurrency entre todos sus Execute,
// asi que nunca corren mas de maxConcurrency subagentes hijos a la vez. El test
// lanza m Execute en paralelo con un cap de n, comprueba que exactamente n entran,
// que el (n+1) espera en el cap, y que el pico de concurrencia nunca pasa de n.
//
// El test referencia un simbolo que aun NO existe (SetMaxConcurrency): en Go eso es
// un fallo de compilacion del paquete de test, y ese es el RED honesto.
func TestTaskTool_CapsConcurrentSubagents(t *testing.T) {
	const n, m = 2, 4 // cap de 2, lanzamos 4
	probe := &concurrencyProbe{entered: make(chan struct{}, m), release: make(chan struct{})}
	children := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	defs := []agent.Def{{Name: "worker", Prompt: "x"}}
	tt := NewTaskTool(defs, probe, children, safeIDCounter())
	tt.SetMaxConcurrency(n)

	var wg sync.WaitGroup
	for i := 0; i < m; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = tt.Execute(context.Background(), json.RawMessage(`{"subagent_type":"worker","prompt":"x"}`))
		}()
	}

	// Exactamente n hijos entran; el resto esperan en el cap.
	for i := 0; i < n; i++ {
		<-probe.entered
	}
	if got := probe.cur.Load(); got != int32(n) {
		t.Fatalf("subagentes dentro = %d, quiero %d", got, n)
	}
	// Ningun (n+1) entra mientras el cap esta lleno.
	select {
	case <-probe.entered:
		t.Fatal("entro un subagente de mas: el cap de concurrencia no se respeta")
	case <-time.After(100 * time.Millisecond):
	}
	close(probe.release)
	wg.Wait()
	if got := probe.peak.Load(); got != int32(n) {
		t.Errorf("pico de concurrencia = %d, quiero %d (nunca mas de n a la vez)", got, n)
	}
}

// TestTaskTool_UnlimitedConcurrencyWhenZero fija que SetMaxConcurrency(0) deja al
// TaskTool SIN tope: con sem nil, acquire no bloquea y todos los Execute corren a la
// vez. Lanzando m=4 en paralelo, los 4 entran simultaneamente (pico = 4). Tumba una
// version que aplique un cap aunque se pida ilimitado, o que ignore SetMaxConcurrency(0).
func TestTaskTool_UnlimitedConcurrencyWhenZero(t *testing.T) {
	const m = 4
	probe := &concurrencyProbe{entered: make(chan struct{}, m), release: make(chan struct{})}
	children := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	defs := []agent.Def{{Name: "worker", Prompt: "x"}}
	tt := NewTaskTool(defs, probe, children, safeIDCounter())
	tt.SetMaxConcurrency(0) // sin tope

	var wg sync.WaitGroup
	for i := 0; i < m; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = tt.Execute(context.Background(), json.RawMessage(`{"subagent_type":"worker","prompt":"x"}`))
		}()
	}

	// Sin tope los 4 entran a la vez.
	for i := 0; i < m; i++ {
		<-probe.entered
	}
	if got := probe.cur.Load(); got != int32(m) {
		t.Fatalf("subagentes dentro = %d, quiero %d (sin tope los 4 corren a la vez)", got, m)
	}
	close(probe.release)
	wg.Wait()
	if got := probe.peak.Load(); got != int32(m) {
		t.Errorf("pico de concurrencia = %d, quiero %d (ilimitado deja correr todos)", got, m)
	}
}

// TestTaskTool_AcquireRespectsContextCancel fija que acquire respeta la cancelacion
// del ctx: con cap=1 y el unico slot ya ocupado, un segundo Execute con el ctx ya
// cancelado devuelve context.Canceled SIN llegar a correr el hijo (nunca pasa el
// semaforo, asi que su Stream nunca se invoca). Tumba una version cuyo acquire ignore
// ctx.Done() (bloquearia para siempre con el slot lleno, o correria el hijo igual).
func TestTaskTool_AcquireRespectsContextCancel(t *testing.T) {
	probe := &concurrencyProbe{entered: make(chan struct{}, 2), release: make(chan struct{})}
	children := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	defs := []agent.Def{{Name: "worker", Prompt: "x"}}
	tt := NewTaskTool(defs, probe, children, safeIDCounter())
	tt.SetMaxConcurrency(1) // un solo slot

	// Primer Execute ocupa el unico slot y se queda bloqueado en release.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = tt.Execute(context.Background(), json.RawMessage(`{"subagent_type":"worker","prompt":"x"}`))
	}()
	<-probe.entered // ya tiene el slot y esta dentro del Stream

	// Segundo Execute con un ctx ya cancelado: acquire debe ver ctx.Done() y abortar.
	ctx2, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tt.Execute(ctx2, json.RawMessage(`{"subagent_type":"worker","prompt":"x"}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute err = %v, quiero context.Canceled (acquire respeta el ctx cancelado)", err)
	}

	// El 2do nunca paso el semaforo: su hijo no corrio (no entro un segundo Stream).
	select {
	case <-probe.entered:
		t.Fatal("el 2do Execute corrio el hijo pese al ctx cancelado")
	default:
	}

	close(probe.release)
	wg.Wait()
}

// TestTaskTool_DefaultConcurrencyCap fija que NewTaskTool arranca con un tope por
// defecto (4) sin necesidad de llamar a SetMaxConcurrency: lanzando m=6 Execute en
// paralelo nunca corren mas de 4 a la vez. Tumba una version donde NewTaskTool no fije
// un sem por defecto (seria ilimitado y entrarian los 6).
func TestTaskTool_DefaultConcurrencyCap(t *testing.T) {
	const m = 6
	probe := &concurrencyProbe{entered: make(chan struct{}, m), release: make(chan struct{})}
	children := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
	defs := []agent.Def{{Name: "worker", Prompt: "x"}}
	tt := NewTaskTool(defs, probe, children, safeIDCounter()) // SIN setter: usa el default

	var wg sync.WaitGroup
	for i := 0; i < m; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = tt.Execute(context.Background(), json.RawMessage(`{"subagent_type":"worker","prompt":"x"}`))
		}()
	}

	// Exactamente 4 (el default) entran; el resto esperan en el cap.
	for i := 0; i < defaultMaxConcurrency; i++ {
		<-probe.entered
	}
	if got := probe.cur.Load(); got != int32(defaultMaxConcurrency) {
		t.Fatalf("subagentes dentro = %d, quiero %d (default)", got, defaultMaxConcurrency)
	}
	// Un quinto no entra mientras el cap esta lleno: el default si topa.
	select {
	case <-probe.entered:
		t.Fatal("entro un 5to: el default no topa")
	case <-time.After(100 * time.Millisecond):
	}
	close(probe.release)
	wg.Wait()
	if got := probe.peak.Load(); got != int32(defaultMaxConcurrency) {
		t.Errorf("pico de concurrencia = %d, quiero %d (el default nunca deja mas de 4 a la vez)", got, defaultMaxConcurrency)
	}
}

// stubTool es una tool con nombre configurable para poblar el registry del hijo.
type stubTool struct{ name string }

func (s stubTool) Name() string          { return s.name }
func (s stubTool) Description() string   { return "stub " + s.name }
func (stubTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (stubTool) Execute(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.Result{}, nil
}

// TestTaskTool_ReadOnlyAgentNotOfferedMutatingTools prueba END-TO-END que el
// allowlist por def.Tools acota lo que ve el hijo: aunque el registry del hijo
// contenga las tools mutantes (edit/write/bash), un agente read-only como 'explore'
// solo debe recibir las que declara (read/grep/glob). Tumba una version que ofrezca
// al hijo TODO el registry en vez del allowlist de def.Tools.
func TestTaskTool_ReadOnlyAgentNotOfferedMutatingTools(t *testing.T) {
	prov := &spyProvider{}

	// El registry del hijo tiene de todo, incluidas las mutantes.
	children := tool.NewRegistry(tool.NewOutputStore(0),
		stubTool{"read"}, stubTool{"grep"}, stubTool{"glob"},
		stubTool{"edit"}, stubTool{"write"}, stubTool{"bash"})

	// La def 'explore' (read-only) sale de los built-ins reales.
	var explore agent.Def
	found := false
	for _, d := range agent.Builtins() {
		if d.Name == "explore" {
			explore = d
			found = true
			break
		}
	}
	if !found {
		t.Fatal("agent.Builtins no incluye 'explore'")
	}

	tt := NewTaskTool([]agent.Def{explore}, prov, children, idCounter())

	_, err := tt.Execute(context.Background(), json.RawMessage(`{"subagent_type":"explore","prompt":"investiga"}`))
	if err != nil {
		t.Fatalf("Execute error inesperado: %v", err)
	}

	// Read-only: recibe lo que declara su def.Tools.
	for _, want := range []string{"read", "grep", "glob"} {
		if !slices.Contains(prov.toolNames, want) {
			t.Errorf("toolNames = %v, quiero que contenga %q (declarada en def.Tools)", prov.toolNames, want)
		}
	}
	// Y NO las mutantes, aunque esten en el registry del hijo.
	for _, deny := range []string{"edit", "write", "bash"} {
		if slices.Contains(prov.toolNames, deny) {
			t.Errorf("toolNames = %v, NO quiero %q: el allowlist de def.Tools acota el registry", prov.toolNames, deny)
		}
	}
}

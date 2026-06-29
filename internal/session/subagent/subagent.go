package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"atenea/internal/agent"
	"atenea/internal/llm"
	"atenea/internal/session"
	"atenea/internal/session/runner"
	"atenea/internal/tool"
)

// defaultMaxDepth limita la recursion de subagentes: con 2 se permite
// padre->hijo->nieto y el nieto ya no puede anidar mas (estilo opencode).
const defaultMaxDepth = 2

// defaultMaxConcurrency topa cuantos subagentes corren a la vez: un default
// modesto evita una avalancha de runners hijos paralelos por recursos.
const defaultMaxConcurrency = 4

// depthKey es la key (de tipo propio para no colisionar con otras del ctx) que
// lleva la profundidad de anidamiento de subagentes en el context.
type depthKey struct{}

// withDepth devuelve un ctx que lleva la profundidad de anidamiento de subagentes.
func withDepth(ctx context.Context, d int) context.Context {
	return context.WithValue(ctx, depthKey{}, d)
}

// depthFrom lee la profundidad del ctx; 0 si no hay (el agente raiz).
func depthFrom(ctx context.Context) int { d, _ := ctx.Value(depthKey{}).(int); return d }

// TaskTool delega una tarea a un subagente. catalog indexa las defs por nombre;
// provider y children son las dependencias del runner hijo; nextID genera los IDs
// de mensaje del hijo (determinista en tests). maxDepth topa la recursion.
type TaskTool struct {
	catalog  map[string]agent.Def
	provider llm.Provider
	children *tool.Registry
	nextID   func() string
	maxDepth int
	// sem topa la concurrencia de subagentes (nil = sin tope).
	sem chan struct{}

	// gate y needsApproval propagan el ask-before-run del padre al runner hijo:
	// antes de asentar una tool call para la que needsApproval devuelve true, el
	// hijo pide aprobacion al gate (que bloquea hasta la decision del usuario).
	// Ambos nil (default) = el hijo no gatea nada (compatibilidad con los tests
	// que no cablean el gate). SE INYECTAN con SetPermissionGate desde app.go;
	// SIN ellos un subagente "general" correria bash sin la confirmacion que el
	// chat principal SI exige, evadiendo el gate. El gate es keyed por
	// (sessionID, callID): el sessionID del hijo es su childID.
	gate          session.PermissionGate
	needsApproval func(call tool.Call) bool

	// storeDecorator envuelve el store en memoria del runner hijo antes de correrlo.
	// Recibe el sessionID del PADRE (el que esta atendiendo la UI) para que la
	// decoracion pueda surfacing los eventos del hijo en su canal. nil (default en
	// tests) = el hijo usa su MemoryStore aislado. SE INYECTA con SetStoreDecorator
	// desde app.go con un EmittingStore que publica los eventos de permiso del hijo
	// en el canal del padre; asi la UI ve el Tool.Permission.Requested del hijo y
	// puede aprobar/denegar. Sin esto el hijo bloquea en gate.Ask pero la UI nunca
	// ve la solicitud (el evento muere en el store aislado).
	storeDecorator func(parentSessionID string, inner session.Store) session.Store
}

// NewTaskTool indexa las defs por nombre. Si dos comparten nombre gana la ultima
// (config del programa, no input del modelo). maxDepth arranca en el default; se
// ajusta con SetMaxDepth (config opcional, igual que el runner usa Set*). El cap
// de concurrencia arranca en el default; se ajusta con SetMaxConcurrency. nextID
// DEBE ser seguro para uso concurrente: varios Execute en paralelo comparten el
// generador.
func NewTaskTool(defs []agent.Def, provider llm.Provider, children *tool.Registry, nextID func() string) *TaskTool {
	m := make(map[string]agent.Def, len(defs))
	for _, d := range defs {
		m[d.Name] = d
	}
	return &TaskTool{catalog: m, provider: provider, children: children, nextID: nextID, maxDepth: defaultMaxDepth, sem: make(chan struct{}, defaultMaxConcurrency)}
}

// SetMaxDepth fija la profundidad maxima de anidamiento de subagentes.
func (t *TaskTool) SetMaxDepth(n int) { t.maxDepth = n }

// SetPermissionGate propaga el ask-before-run del padre al runner hijo: gate
// resuelve la aprobacion del usuario y needsApproval decide que tool calls la
// requieren (p.ej. solo "bash"). Si cualquiera es nil el hijo no gatea nada.
// Mismo patron que runner.SetPermissionGate y que SetMaxDepth/SetMaxConcurrency
// (config opcional via setter). Punto de entrada para app.go (paquete main); los
// tests lo llaman directo. Es la pieza de seguridad: sin esta propagacion el
// subagente correria bash sin la confirmacion que el chat principal exige.
func (t *TaskTool) SetPermissionGate(gate session.PermissionGate, needsApproval func(call tool.Call) bool) {
	t.gate = gate
	t.needsApproval = needsApproval
}

// SetStoreDecorator inyecta el decorador del store del runner hijo. El decorador
// recibe el sessionID del padre (capturado del ctx de Execute via tool.SessionIDFrom)
// e inner (el MemoryStore del hijo). app.go pasa un EmittingStore que publica los
// eventos de permiso del hijo en el canal del padre para que la UI los vea y pueda
// aprobar/denegar. nil (default) deja el MemoryStore aislado del hijo (compatibilidad
// con los tests). Mismo patron Set* que SetPermissionGate/SetMaxDepth.
func (t *TaskTool) SetStoreDecorator(dec func(parentSessionID string, inner session.Store) session.Store) {
	t.storeDecorator = dec
}

// SetMaxConcurrency fija el tope de subagentes simultaneos. n <= 0 deja sin tope.
func (t *TaskTool) SetMaxConcurrency(n int) {
	if n > 0 {
		t.sem = make(chan struct{}, n)
		return
	}
	t.sem = nil
}

// acquire toma un slot del cap de concurrencia de subagentes; bloquea hasta que
// haya uno libre y respeta la cancelacion del ctx. Sin semaforo (nil) no topa.
func (t *TaskTool) acquire(ctx context.Context) error {
	if t.sem == nil {
		return nil
	}
	select {
	case t.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// release devuelve el slot. No-op si no hay semaforo.
func (t *TaskTool) release() {
	if t.sem != nil {
		<-t.sem
	}
}

func (*TaskTool) Name() string { return "task" }

// Description arma el texto base seguido del catalogo de subagentes ordenado por
// nombre, una linea por subagente, al estilo opencode (la descripcion del task
// tool se genera desde los agentes disponibles).
func (t *TaskTool) Description() string {
	var b strings.Builder
	b.WriteString("Delega una tarea a un subagente. El campo subagent_type debe ser uno de los disponibles:\n")
	names := make([]string, 0, len(t.catalog))
	for n := range t.catalog {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		b.WriteString("- " + n + ": " + t.catalog[n].Description + "\n")
	}
	return b.String()
}

func (*TaskTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"subagent_type":{"type":"string"},"prompt":{"type":"string"}},"required":["subagent_type","prompt"]}`)
}

// Execute parsea {subagent_type, prompt}, busca la def, levanta el subagente hijo
// (un runner con MemoryStore en memoria) con la entrada como prompt en cola y
// devuelve su reporte final (el ultimo texto del asistente del hijo).
func (t *TaskTool) Execute(ctx context.Context, input json.RawMessage) (tool.Result, error) {
	var in struct {
		SubagentType string `json:"subagent_type"`
		Prompt       string `json:"prompt"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return tool.Result{}, fmt.Errorf("subagent: input invalido: %w", err)
	}

	def, ok := t.catalog[in.SubagentType]
	if !ok {
		return tool.Result{}, fmt.Errorf("subagent_type %q desconocido. Disponibles: %s", in.SubagentType, t.available())
	}

	// Toma un slot del cap de concurrencia. El slot se mantiene mientras el hijo
	// corre porque Execute es sincrono (Run es bloqueante). No hay deadlock por
	// recursion: los built-in no anidan, y un subagente que si anida ya tiene el
	// cap de recursion (maxDepth) que le quita la tool "task" en el tope.
	if err := t.acquire(ctx); err != nil {
		return tool.Result{}, err
	}
	defer t.release()

	var store session.Store = session.NewMemoryStore()
	// Decora el store del hijo (en produccion, el EmittingStore) para que sus eventos
	// de permiso lleguen al bus en el canal del padre; asi la UI ve el
	// Tool.Permission.Requested del hijo y puede aprobar/denegar. parentSessionID sale
	// del ctx de Execute (el runner padre lo anota via tool.WithSessionID). Sin
	// decorator queda el MemoryStore aislado.
	if t.storeDecorator != nil {
		store = t.storeDecorator(tool.SessionIDFrom(ctx), store)
	}
	inbox := session.NewMemoryInbox()
	childID := t.nextID()

	perms := tool.Permissions{}
	for _, name := range def.Tools {
		perms[name] = true
	}

	// Cap de recursion: el hijo correra en childDepth. Si esa profundidad ya
	// alcanza (o pasa) el tope, le quitamos la tool "task" de sus permisos para
	// que no pueda anidar mas subagentes.
	depth := depthFrom(ctx)
	childDepth := depth + 1
	if childDepth >= t.maxDepth {
		delete(perms, "task")
	}

	r := runner.NewRunner(store, inbox, t.provider, t.children, perms, t.nextID)
	r.SetSystemPrompt(func(string) string { return def.Prompt })
	// Propaga el ask-before-run del padre: si el subagente invoca una tool gateada
	// (bash), el runner hijo pide aprobacion igual que el chat principal en vez de
	// correrla a ciegas. Sin gate cableado queda nil y el hijo no gatea (default).
	if t.gate != nil && t.needsApproval != nil {
		r.SetPermissionGate(t.gate, t.needsApproval)
	}

	if err := inbox.Admit(ctx, childID, session.Prompt{Text: in.Prompt}, session.DeliveryQueue); err != nil {
		return tool.Result{}, err
	}
	// Propaga la profundidad al hijo: un nieto vera childDepth+1.
	if err := r.Run(withDepth(ctx, childDepth), childID, false); err != nil {
		return tool.Result{}, err
	}

	msgs, err := store.Messages(ctx, childID, 0)
	if err != nil {
		return tool.Result{}, err
	}
	report := ""
	for _, m := range msgs {
		if m.Role == session.RoleAssistant {
			report = m.Text
		}
	}
	return tool.Result{Output: report}, nil
}

// available devuelve los nombres del catalogo ordenados, para el mensaje de error
// cuando el modelo pide un subagent_type inexistente.
func (t *TaskTool) available() string {
	names := make([]string, 0, len(t.catalog))
	for n := range t.catalog {
		names = append(names, n)
	}
	if len(names) == 0 {
		return "ninguno"
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// var _ tool.Tool = (*TaskTool)(nil) asegura en compilacion que cumple la interface.
var _ tool.Tool = (*TaskTool)(nil)

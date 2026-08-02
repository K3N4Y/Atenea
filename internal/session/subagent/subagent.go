package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/K3N4Y/atenea/internal/agent"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/session/runner"
	"github.com/K3N4Y/atenea/internal/tool"
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

// BudgetError identifies which delegated execution limit was exhausted.
type BudgetError struct {
	Kind  string
	Limit int
}

func (e *BudgetError) Error() string {
	return fmt.Sprintf("subagent %s budget exhausted (limit %d)", e.Kind, e.Limit)
}

// ChildEnvironment is the injectable execution boundary used for isolated worktree jobs.
type ChildEnvironment struct {
	Store     session.Store
	Inbox     session.Inbox
	Registry  *tool.Registry
	Workspace string
	Cleanup   func() error
	// Discard removes an isolated environment whose run did not complete.
	Discard func() error
}
type EnvironmentResolver func(context.Context, agent.Def) (ChildEnvironment, error)
type ProviderResolver func(context.Context, agent.Def) (llm.Provider, error)

type taskInput struct {
	SubagentType  string          `json:"subagent_type"`
	Prompt        string          `json:"prompt"`
	OutputSchema  json.RawMessage `json:"output_schema,omitempty"`
	RequestBudget *int            `json:"request_budget,omitempty"`
	TimeoutMS     *int            `json:"timeout_ms,omitempty"`
	Detached      bool            `json:"detached,omitempty"`
	Worktree      bool            `json:"worktree,omitempty"`
}
type taskUsage struct {
	requests atomic.Int64
	tokens   atomic.Int64
}
type budgetProvider struct {
	provider      llm.Provider
	requestBudget int
	usage         *taskUsage
	cancel        context.CancelFunc
	exhausted     atomic.Pointer[BudgetError]
	onProgress    func()
}

func (p *budgetProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	if p.requestBudget > 0 && int(p.usage.requests.Load()) >= p.requestBudget {
		err := &BudgetError{Kind: "request", Limit: p.requestBudget}
		p.exhausted.CompareAndSwap(nil, err)
		p.cancel()
		return nil, err
	}
	p.usage.requests.Add(1)
	if p.onProgress != nil {
		p.onProgress()
	}
	in, err := p.provider.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	out := make(chan llm.Event)
	go func() {
		defer close(out)
		for ev := range in {
			if ev.Kind == llm.StepEnded && ev.Usage != nil {
				total := ev.Usage.InputTokens + ev.Usage.OutputTokens + ev.Usage.ReasoningTokens
				p.usage.tokens.Add(int64(total))
				if p.onProgress != nil {
					p.onProgress()
				}
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

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

	providerResolver    ProviderResolver
	environmentResolver EnvironmentResolver
	supervisor          *Supervisor
	// gate and policy propagate the parent's ask-before-run to the child
	// runner: the policy classifies each of the child's tool calls
	// (Allow/Ask/Deny) and the gate blocks the asking ones until the user's
	// decision. Both nil (default) = the child gates nothing (compatibility
	// with tests that do not wire the gate). INJECTED with SetPermissionGate
	// from the wiring; WITHOUT them a "general" subagent would run gated tools
	// (bash, write, edit, web_fetch) without the confirmation the main chat
	// enforces, evading the gate. The gate is keyed by (sessionID, callID):
	// the child's sessionID is its childID.
	gate   permission.Gate
	policy permission.Policy

	// storeDecorator envuelve el store en memoria del runner hijo antes de correrlo.
	// Recibe el sessionID del PADRE (el que esta atendiendo la UI) para que la
	// decoracion pueda surfacing los eventos del hijo en su canal. nil (default en
	// tests) = el hijo usa su MemoryStore aislado. SE INYECTA con SetStoreDecorator
	// desde app.go con un EmittingStore que publica los eventos de permiso del hijo
	// en el canal del padre; asi la UI ve el Tool.Permission.Requested del hijo y
	// puede aprobar/denegar. Sin esto el hijo bloquea en gate.Ask pero la UI nunca
	// ve la solicitud (el evento muere en el store aislado).
	storeDecorator func(parentSessionID, parentCallID string, inner session.Store) session.Store
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
	return &TaskTool{catalog: m, provider: provider, children: children, nextID: nextID, maxDepth: defaultMaxDepth, sem: make(chan struct{}, defaultMaxConcurrency), supervisor: NewSupervisor(nextID)}
}

// SetMaxDepth fija la profundidad maxima de anidamiento de subagentes.
func (t *TaskTool) SetMaxDepth(n int) { t.maxDepth = n }

func (t *TaskTool) SetProviderResolver(resolve ProviderResolver) { t.providerResolver = resolve }
func (t *TaskTool) SetEnvironmentResolver(resolve EnvironmentResolver) {
	t.environmentResolver = resolve
}

func (t *TaskTool) SetSupervisor(supervisor *Supervisor) {
	if supervisor != nil {
		t.supervisor = supervisor
	}
}

// Close cancels and joins jobs owned by this task tool's supervisor.
func (t *TaskTool) Close() { t.supervisor.Close() }

// SupervisionTools returns the status, wait, and cancel tools sharing this supervisor.
func (t *TaskTool) SupervisionTools() []tool.Tool { return t.supervisor.tools() }

// SetPermissionGate propagates the parent's ask-before-run to the child
// runner: policy classifies each tool call (Allow/Ask/Deny) and gate resolves
// the user's decision for the calls that ask. If either is nil the child
// gates nothing. Same pattern as runner.SetPermissionGate and
// SetMaxDepth/SetMaxConcurrency (optional config via setter). Entry point for
// the wiring; tests call it directly. This is the security piece: without
// this propagation the subagent would run gated tools without the
// confirmation the main chat enforces.
func (t *TaskTool) SetPermissionGate(gate permission.Gate, policy permission.Policy) {
	t.gate = gate
	t.policy = policy
}

// SetStoreDecorator injects the bus-only child activity decorator. Parent
// session and exact task call IDs come from the settlement context.
func (t *TaskTool) SetStoreDecorator(dec func(parentSessionID, parentCallID string, inner session.Store) session.Store) {
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
	return json.RawMessage(`{"type":"object","properties":{"subagent_type":{"type":"string"},"prompt":{"type":"string"},"output_schema":{"type":"object"},"request_budget":{"type":"integer","minimum":1},"timeout_ms":{"type":"integer","minimum":1},"detached":{"type":"boolean"},"worktree":{"type":"boolean"}},"required":["subagent_type","prompt"]}`)
}

// Effects: none of its own. A task runs no command and writes no file itself —
// it starts a child runner whose every tool call goes through the SAME gate and
// the SAME policy as the main chat (see SetPermissionGate), so the child's
// effects are asked about on the child's behalf, as they happen and with the
// real command on screen. Declaring the union here instead would ask one vague
// question up front and answer for calls nobody has seen yet.
func (*TaskTool) Effects() tool.Effects { return tool.NoEffects }

// Present: a task reads as the kind of subagent it started, since that is what
// distinguishes two delegations at a glance. The output is the child's report,
// written to be read, so it stays visible.
func (*TaskTool) Present(call tool.Call, _ tool.Result) tool.Presentation {
	var in struct {
		Type string `json:"subagent_type"`
	}
	json.Unmarshal(call.Input, &in)
	return tool.Presentation{Label: "SubAgent", Subject: in.Type}
}

// Execute validates the request and either runs synchronously or transfers it
// to the supervisor-owned detached context.
func (t *TaskTool) Execute(ctx context.Context, input json.RawMessage) (tool.Result, error) {
	var in taskInput
	dec := json.NewDecoder(strings.NewReader(string(input)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return tool.Result{}, fmt.Errorf("subagent: invalid input: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return tool.Result{}, errors.New("subagent: invalid input: trailing JSON value")
	}
	for name, value := range map[string]*int{"request_budget": in.RequestBudget, "timeout_ms": in.TimeoutMS} {
		if value != nil && *value <= 0 {
			return tool.Result{}, fmt.Errorf("subagent: %s must be greater than zero", name)
		}
	}
	def, ok := t.catalog[in.SubagentType]
	if !ok {
		return tool.Result{}, fmt.Errorf("subagent_type %q desconocido. Disponibles: %s", in.SubagentType, t.available())
	}
	if in.Worktree && t.environmentResolver == nil {
		return tool.Result{}, errors.New("subagent: worktree requested but no isolated environment resolver is configured")
	}
	if in.Detached {
		result, err := t.supervisor.start(func(jobCtx context.Context, progress *jobProgress) (string, error) {
			return t.run(jobCtx, ctx, def, in, progress)
		})
		if err != nil {
			return tool.Result{}, err
		}
		if recorder := tool.SettlementRecorderFrom(ctx); recorder != nil {
			recorder.SetTaskDetached()
		}
		return result, nil
	}
	report, err := t.run(ctx, ctx, def, in, nil)
	return tool.Result{Output: report}, err
}

func (t *TaskTool) run(ctx, metadataCtx context.Context, def agent.Def, in taskInput, progress *jobProgress) (report string, err error) {
	started := time.Now()
	runCtx, cancel := context.WithCancel(ctx)
	if in.TimeoutMS != nil {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(*in.TimeoutMS)*time.Millisecond)
	}
	defer cancel()
	if err := t.acquire(runCtx); err != nil {
		if in.TimeoutMS != nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return "", &BudgetError{Kind: "timeout_ms", Limit: *in.TimeoutMS}
		}
		return "", err
	}
	defer t.release()
	provider := t.provider
	if t.providerResolver != nil {
		var err error
		provider, err = t.providerResolver(runCtx, def)
		if err != nil {
			return "", fmt.Errorf("subagent provider: %w", err)
		}
		if provider == nil {
			provider = t.provider
		}
	}
	env := ChildEnvironment{Store: session.NewMemoryStore(), Inbox: session.NewMemoryInbox(), Registry: t.children}
	if in.Worktree {
		var err error
		env, err = t.environmentResolver(runCtx, def)
		if err != nil {
			return "", fmt.Errorf("subagent environment: %w", err)
		}
		if env.Cleanup != nil {
			defer env.Cleanup()
		}
	}
	completed := false
	if env.Discard != nil {
		defer func() {
			if !completed {
				err = errors.Join(err, env.Discard())
			}
		}()
	}
	if env.Store == nil || env.Inbox == nil || env.Registry == nil {
		return "", errors.New("subagent: incomplete child environment")
	}
	counting := &countingStore{Store: env.Store}
	var store session.Store = counting
	if t.storeDecorator != nil {
		store = t.storeDecorator(tool.SessionIDFrom(metadataCtx), tool.CallIDFrom(metadataCtx), store)
	}
	usage := &taskUsage{}
	bp := &budgetProvider{provider: provider, usage: usage, cancel: cancel}
	updateProgress := func() {
		if progress != nil {
			progress.set(tool.TaskSettlement{Requests: int(usage.requests.Load()), Tokens: int(usage.tokens.Load()), Duration: time.Since(started), ToolCalls: counting.count()})
		}
	}
	counting.onChange = func(int) { updateProgress() }
	bp.onProgress = updateProgress
	if progress != nil {
		progress.setWorkspace(env.Workspace)
	}
	if in.RequestBudget != nil {
		bp.requestBudget = *in.RequestBudget
	}
	perms := tool.Permissions{}
	for _, name := range def.Tools {
		perms[name] = true
	}
	childDepth := depthFrom(metadataCtx) + 1
	if childDepth >= t.maxDepth {
		delete(perms, "task")
	}
	childID := t.nextID()
	r := runner.NewRunner(store, env.Inbox, bp, env.Registry, perms, t.nextID)
	systemPrompt := def.Prompt
	if env.Workspace != "" {
		systemPrompt += "\n\nYou are working in an isolated Git worktree at " + env.Workspace + ". Include that path in your final report so the parent can inspect or integrate your changes."
	}
	if len(in.OutputSchema) > 0 {
		systemPrompt += "\n\nYour final response must be only JSON that validates against this output schema:\n" + string(in.OutputSchema)
	}
	r.SetSystemPrompt(func(string) string { return systemPrompt })
	if t.gate != nil && t.policy != nil {
		r.SetPermissionGate(t.gate, t.policy)
	}
	finish := func() {
		s := tool.TaskSettlement{Requests: int(usage.requests.Load()), Tokens: int(usage.tokens.Load()), Duration: time.Since(started), ToolCalls: counting.count(), Workspace: env.Workspace}
		if rec := tool.SettlementRecorderFrom(metadataCtx); rec != nil {
			rec.SetTaskSettlement(s)
		}
		if progress != nil {
			progress.set(s)
		}
	}
	defer finish()
	if err := env.Inbox.Admit(runCtx, childID, session.Prompt{Text: in.Prompt}, session.DeliveryQueue); err != nil {
		return "", err
	}
	err = r.Run(withDepth(runCtx, childDepth), childID, false, def.Steps)
	if exhausted := bp.exhausted.Load(); exhausted != nil {
		return "", exhausted
	}
	if err != nil {
		if in.TimeoutMS != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(runCtx.Err(), context.DeadlineExceeded)) {
			return "", &BudgetError{Kind: "timeout_ms", Limit: *in.TimeoutMS}
		}
		return "", err
	}
	msgs, err := store.Messages(runCtx, childID, 0)
	if err != nil {
		return "", err
	}
	report = ""
	for _, m := range msgs {
		if m.Role == session.RoleAssistant {
			report = m.Text
		}
	}
	if len(in.OutputSchema) > 0 {
		if err := validateReport(in.OutputSchema, report); err != nil {
			return "", err
		}
	}
	if env.Workspace != "" {
		if len(in.OutputSchema) > 0 {
			var result any
			_ = json.Unmarshal([]byte(report), &result)
			envelope, _ := json.Marshal(map[string]any{"result": result, "worktree": env.Workspace})
			report = string(envelope)
		} else {
			report += "\n\nWorktree: " + env.Workspace
		}
	}
	completed = true
	return report, nil
}

func validateReport(raw json.RawMessage, report string) error {
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return fmt.Errorf("subagent output schema invalid: %w", err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return fmt.Errorf("subagent output schema invalid: %w", err)
	}
	var value any
	if err := json.Unmarshal([]byte(report), &value); err != nil {
		return fmt.Errorf("subagent output schema violation: report is not JSON: %w", err)
	}
	if err := resolved.Validate(value); err != nil {
		return fmt.Errorf("subagent output schema violation: %w", err)
	}
	return nil
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

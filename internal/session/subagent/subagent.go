package subagent

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/K3N4Y/atenea/internal/agent"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/session/runner"
	"github.com/K3N4Y/atenea/internal/session/tasksettlement"
	"github.com/K3N4Y/atenea/internal/tool"
)

// defaultMaxDepth allows three delegated harness levels below the root. A child
// running at depth 3 can finish its assignment but cannot spawn another child.
const defaultMaxDepth = 3

// defaultMaxConcurrency caps runnable leaf subagents. An ancestor releases its
// slot while it invokes descendants, so a recursive tree cannot deadlock by
// filling every slot with agents waiting for their own children.
const defaultMaxConcurrency = 4

type depthKey struct{}
type leaseKey struct{}
type recursiveKey struct{}

type scheduler struct {
	sem chan struct{}
}

type lease struct {
	scheduler   *scheduler
	mu          sync.Mutex
	held        bool
	suspensions int
	closed      bool
}

func withDepth(ctx context.Context, d int) context.Context {
	return context.WithValue(ctx, depthKey{}, d)
}

func depthFrom(ctx context.Context) int { d, _ := ctx.Value(depthKey{}).(int); return d }

func withRecursive(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, recursiveKey{}, enabled)
}

func recursiveFrom(ctx context.Context) bool {
	enabled, _ := ctx.Value(recursiveKey{}).(bool)
	return enabled
}

func withLease(ctx context.Context, l *lease) context.Context {
	return context.WithValue(ctx, leaseKey{}, l)
}

func leaseFrom(ctx context.Context) *lease {
	l, _ := ctx.Value(leaseKey{}).(*lease)
	return l
}

func (s *scheduler) acquire(ctx context.Context) (*lease, error) {
	l := &lease{scheduler: s}
	if s == nil || s.sem == nil {
		l.held = true
		return l, nil
	}
	select {
	case s.sem <- struct{}{}:
		l.held = true
		return l, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (l *lease) close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	if l.held && l.scheduler != nil && l.scheduler.sem != nil {
		<-l.scheduler.sem
	}
	l.held = false
}

func (l *lease) suspend() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.suspensions++
	if l.suspensions == 1 && l.held {
		if l.scheduler != nil && l.scheduler.sem != nil {
			<-l.scheduler.sem
		}
		l.held = false
	}
}

func (l *lease) resume(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	if l.suspensions == 0 {
		return nil
	}
	l.suspensions--
	if l.suspensions > 0 || l.held {
		return nil
	}
	if l.scheduler != nil && l.scheduler.sem != nil {
		select {
		case l.scheduler.sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	l.held = true
	return nil
}

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
	// Context decorates the child runner context with environment-local
	// capabilities such as its default recursive registry.
	Context func(context.Context) context.Context
	Cleanup func() error
	// Discard removes an isolated environment whose run did not complete.
	Discard func() error
}
type EnvironmentResolver func(context.Context, agent.Def) (ChildEnvironment, error)
type ProviderResolver func(context.Context, agent.Def) (llm.Provider, error)
type StoreDecorator func(parentSessionID, parentCallID string, inner session.Store) session.Store

// ChildRegistryResolver selects the default registry for a delegated child from
// the caller's execution context. It keeps recursive worktree descendants rooted
// in the worktree that spawned them.
type ChildRegistryResolver func(context.Context) *tool.Registry
type PolicyResolver func() permission.Policy

// Limits controls delegated nesting and parallelism. A nil *Limits in Config
// applies the package defaults; within Limits, MaxConcurrency <= 0 explicitly
// requests unlimited concurrency.
type Limits struct {
	MaxDepth       int
	MaxConcurrency int
}

// Config is the complete startup configuration of a TaskTool. NewTaskTool
// applies it atomically so delegated execution cannot begin with only part of
// its provider, environment, supervision, or permission policy installed.
type Config struct {
	Definitions []agent.Def
	Provider    llm.Provider
	Children    *tool.Registry
	NextID      func() string

	ProviderResolver      ProviderResolver
	EnvironmentResolver   EnvironmentResolver
	ChildRegistryResolver ChildRegistryResolver
	// Recursive reports whether the parent session explicitly activated RAH.
	// Nil preserves the existing one-level task behavior.
	Recursive      func(sessionID string) bool
	Supervisor     *Supervisor
	Gate           permission.Gate
	Policy         PolicyResolver
	StoreDecorator StoreDecorator
	Limits         *Limits
}

type taskInput struct {
	SubagentType string          `json:"subagent_type"`
	Prompt       string          `json:"prompt"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
	TimeoutMS    *int            `json:"timeout_ms,omitempty"`
	Detached     bool            `json:"detached,omitempty"`
	Worktree     bool            `json:"worktree,omitempty"`
}
type taskUsage struct {
	requests atomic.Int64
	tokens   atomic.Int64
}
type budgetProvider struct {
	provider   llm.Provider
	usage      *taskUsage
	onProgress func()
}

func (p *budgetProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
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

// TaskTool delegates work to child harnesses. catalog indexes definitions by
// name; provider and children supply each runner; nextID must be concurrency-safe.
// maxDepth bounds recursive delegation.
type TaskTool struct {
	catalog         map[string]agent.Def
	provider        llm.Provider
	children        *tool.Registry
	resolveChildren ChildRegistryResolver
	recursive       func(sessionID string) bool
	nextID          func() string
	maxDepth        int
	scheduler       *scheduler

	providerResolver    ProviderResolver
	environmentResolver EnvironmentResolver
	supervisor          *Supervisor
	// gate and policy propagate the parent's ask-before-run to the child
	// runner: the policy classifies each of the child's tool calls
	// (Allow/Ask/Deny) and the gate blocks the asking ones until the user's
	// decision. Both nil means the child gates nothing. Config installs them
	// from wiring; without them a "general" subagent would run gated tools
	// (bash, write, edit, web_fetch) without the confirmation the main chat
	// enforces, evading the gate. The gate is keyed by (sessionID, callID):
	// the child's sessionID is its childID.
	gate   permission.Gate
	policy PolicyResolver

	// storeDecorator wraps the child runner's memory store before execution. It
	// receives the parent session and task call IDs so child activity can surface
	// on the parent's channel. Nil keeps the child store isolated.
	storeDecorator StoreDecorator
}

// NewTaskTool indexes definitions by name; the final duplicate wins. NextID
// must be safe for concurrent use because parallel Execute calls share it.
func NewTaskTool(cfg Config) *TaskTool {
	m := make(map[string]agent.Def, len(cfg.Definitions))
	for _, d := range cfg.Definitions {
		m[d.Name] = d
	}
	maxDepth, maxConcurrency := defaultMaxDepth, defaultMaxConcurrency
	if cfg.Limits != nil {
		maxDepth, maxConcurrency = cfg.Limits.MaxDepth, cfg.Limits.MaxConcurrency
	}
	supervisor := cfg.Supervisor
	if supervisor == nil {
		supervisor = NewSupervisor(cfg.NextID)
	}
	t := &TaskTool{
		catalog: m, provider: cfg.Provider, children: cfg.Children, nextID: cfg.NextID,
		maxDepth: maxDepth, providerResolver: cfg.ProviderResolver,
		environmentResolver: cfg.EnvironmentResolver, resolveChildren: cfg.ChildRegistryResolver,
		recursive: cfg.Recursive, supervisor: supervisor, gate: cfg.Gate, policy: cfg.Policy,
		storeDecorator: cfg.StoreDecorator, scheduler: &scheduler{},
	}
	if maxConcurrency > 0 {
		t.scheduler.sem = make(chan struct{}, maxConcurrency)
	}
	return t
}

func (t *TaskTool) setMaxDepth(n int) { t.maxDepth = n }
func (t *TaskTool) setProviderResolver(resolve ProviderResolver) {
	t.providerResolver = resolve
}

func (t *TaskTool) setEnvironmentResolver(resolve EnvironmentResolver) {
	t.SetEnvironmentResolver(resolve)
}

func (t *TaskTool) setSupervisor(supervisor *Supervisor) {
	if supervisor != nil {
		t.supervisor = supervisor
	}
}

// Close cancels and joins jobs owned by this task tool's supervisor.
func (t *TaskTool) Close() { t.supervisor.Close() }

// SupervisionTools returns the status, wait, and cancel tools sharing this supervisor.
func (t *TaskTool) SupervisionTools() []tool.Tool { return t.supervisor.tools() }

func (t *TaskTool) setPermissionGate(gate permission.Gate, policy permission.Policy) {
	t.gate = gate
	t.policy = func() permission.Policy { return policy }
}

func (t *TaskTool) setStoreDecorator(dec StoreDecorator) {
	t.storeDecorator = dec
}

func (t *TaskTool) setMaxConcurrency(n int) {
	t.scheduler = &scheduler{}
	if n > 0 {
		t.scheduler.sem = make(chan struct{}, n)
	}
}

// SetChildren installs the final recursive child registry during composition.
// NewTaskTool cannot receive that registry initially because the registry itself
// contains this task tool.
func (t *TaskTool) SetChildren(children *tool.Registry) {
	t.children = children
}

// SetChildRegistryResolver installs the context-aware default registry selector
// after recursive workspace registries have been assembled.
func (t *TaskTool) SetChildRegistryResolver(resolve ChildRegistryResolver) {
	t.resolveChildren = resolve
}

// SetEnvironmentResolver installs the final isolated-environment factory during
// composition. Worktree registries also contain this task tool, so wiring closes
// that cycle after construction.
func (t *TaskTool) SetEnvironmentResolver(resolve EnvironmentResolver) {
	t.environmentResolver = resolve
}

func (*TaskTool) Name() string { return "task" }

//go:embed task.txt
var taskDescription string

// Description combines a stable operating contract with the live subagent
// catalog so the model can choose both whether and where to delegate.
func (t *TaskTool) Description() string {
	var b strings.Builder
	b.WriteString(taskDescription)
	b.WriteString("\n## Available subagents\n")
	names := make([]string, 0, len(t.catalog))
	for name := range t.catalog {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		b.WriteString("- " + name + ": " + t.catalog[name].Description + "\n")
	}
	return b.String()
}

func (*TaskTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"subagent_type":{"type":"string"},"prompt":{"type":"string"},"output_schema":{"type":"object"},"timeout_ms":{"type":"integer","minimum":1},"detached":{"type":"boolean"},"worktree":{"type":"boolean"}},"required":["subagent_type","prompt"]}`)
}

// Effects: none of its own. A task runs no command and writes no file itself —
// it starts a child runner whose every tool call goes through the SAME gate and
// the SAME policy as the main chat (provided through Config), so the child's
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
	for name, value := range map[string]*int{"timeout_ms": in.TimeoutMS} {
		if value != nil && *value <= 0 {
			return tool.Result{}, fmt.Errorf("subagent: %s must be greater than zero", name)
		}
	}
	def, ok := t.catalog[in.SubagentType]
	if !ok {
		return tool.Result{}, fmt.Errorf("unknown subagent_type %q; available: %s", in.SubagentType, t.available())
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
		if recorder := tasksettlement.FromContext(ctx); recorder != nil {
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

	parentLease := leaseFrom(metadataCtx)
	if parentLease != nil {
		parentLease.suspend()
		defer func() {
			err = errors.Join(err, parentLease.resume(ctx))
		}()
	}
	childLease, acquireErr := t.scheduler.acquire(runCtx)
	if acquireErr != nil {
		if in.TimeoutMS != nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return "", &BudgetError{Kind: "timeout_ms", Limit: *in.TimeoutMS}
		}
		return "", acquireErr
	}
	defer childLease.close()
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
	children := t.children
	if t.resolveChildren != nil {
		if resolved := t.resolveChildren(metadataCtx); resolved != nil {
			children = resolved
		}
	}
	env := ChildEnvironment{Store: session.NewMemoryStore(), Inbox: session.NewMemoryInbox(), Registry: children}
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
	bp := &budgetProvider{provider: provider, usage: usage}
	updateProgress := func() {
		if progress != nil {
			progress.set(tasksettlement.Summary{Requests: int(usage.requests.Load()), Tokens: int(usage.tokens.Load()), Duration: time.Since(started), ToolCalls: counting.count()})
		}
	}
	counting.onChange = func(int) { updateProgress() }
	bp.onProgress = updateProgress
	if progress != nil {
		progress.setWorkspace(env.Workspace)
	}
	perms := tool.Permissions{}
	for _, name := range def.Tools {
		perms[name] = true
	}
	childDepth := depthFrom(metadataCtx) + 1
	parentSessionID := tool.SessionIDFrom(metadataCtx)
	recursive := recursiveFrom(metadataCtx) ||
		(t.recursive != nil && t.recursive(parentSessionID))
	if !recursive || childDepth >= t.maxDepth {
		delete(perms, "task")
	}
	if recursive && childDepth >= t.maxDepth {
		delete(perms, "bash")
	}
	childID := t.nextID()
	systemPrompt := def.Prompt
	if env.Workspace != "" {
		systemPrompt += "\n\nYou are working in an isolated Git worktree at " + env.Workspace + ". Include that path in your final report so the parent can inspect or integrate your changes."
	}
	if len(in.OutputSchema) > 0 {
		systemPrompt += "\n\nYour final response must be only JSON that validates against this output schema:\n" + string(in.OutputSchema)
	}
	var policy permission.Policy
	if t.policy != nil {
		policy = t.policy()
	}
	r := runner.New(runner.Config{
		Store: store, Inbox: env.Inbox, Provider: bp, Registry: env.Registry,
		Permissions: perms, NextID: t.nextID,
		System: func(string) string { return systemPrompt }, Gate: t.gate, Policy: policy,
	})
	finish := func() {
		s := tasksettlement.Summary{Requests: int(usage.requests.Load()), Tokens: int(usage.tokens.Load()), Duration: time.Since(started), ToolCalls: counting.count(), Workspace: env.Workspace}
		if rec := tasksettlement.FromContext(metadataCtx); rec != nil {
			rec.SetSummary(s)
		}
		if progress != nil {
			progress.set(s)
		}
	}
	defer finish()
	if err := env.Inbox.Admit(runCtx, childID, session.Prompt{Text: in.Prompt}, session.DeliveryQueue); err != nil {
		return "", err
	}
	childCtx := withLease(withDepth(runCtx, childDepth), childLease)
	childCtx = withRecursive(childCtx, recursive)
	childCtx = tool.WithSessionID(childCtx, parentSessionID)
	if env.Context != nil {
		childCtx = env.Context(childCtx)
	}
	err = r.Run(childCtx, childID, false, def.Steps)
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

// available returns catalog names in stable order for validation errors.
func (t *TaskTool) available() string {
	names := make([]string, 0, len(t.catalog))
	for n := range t.catalog {
		names = append(names, n)
	}
	if len(names) == 0 {
		return "none"
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// var _ tool.Tool = (*TaskTool)(nil) asegura en compilacion que cumple la interface.
var _ tool.Tool = (*TaskTool)(nil)

package tool

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/K3N4Y/atenea/agentcore/permission"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/tool/repair"
)

// SettleFunc settles one tool call. A materialized function is closed over the
// allowed tools and safe to call concurrently.
type SettleFunc func(ctx context.Context, call Call) (Result, error)

// PreviewFunc projects partial input through the frozen turn strategy selected
// during materialization. Unknown or non-previewable routes return false.
type PreviewFunc func(ctx context.Context, call Call) (Preview, bool)

// MatcherEntriesFunc exposes path/content matcher metadata from that same
// frozen strategy, without requiring a local matcher consumer.
type MatcherEntriesFunc func(call Call) []MatcherEntry

// Middleware decorates tool settlement. Registry.Materialize repairs input,
// applies the permission gate to exactly what will execute, caps output, then
// applies extensions in registration order.
type Middleware func(next SettleFunc) SettleFunc

// PermissionRequester publishes an ask-before-run request and waits for its
// verdict. The runner supplies one per settlement because publication belongs
// to the current durable turn.
type PermissionRequester func(context.Context, permission.Request) (bool, error)
type permissionRequestKey struct{}
type executionStateKey struct{}
type executionState struct{ repairNotes []string }

// WithPermissionRequester supplies the turn-owned publication and ask operation
// to the registry's permission middleware without coupling the tool module to
// the durable session implementation.
func WithPermissionRequester(ctx context.Context, request PermissionRequester) context.Context {
	return context.WithValue(ctx, permissionRequestKey{}, request)
}

// ErrPermissionDenied is returned when policy or the user refuses a call.
var ErrPermissionDenied = errors.New("tool denied by the user")

// ErrPermissionUnresolved means asking stopped before a verdict. The runner
// leaves the call unresolved so its existing turn-close path records the cause.
var ErrPermissionUnresolved = errors.New("tool permission was not resolved")

// SettlementAbortError carries an infrastructure failure that must abort the
// turn instead of becoming a tool result.
type SettlementAbortError struct{ Err error }

func (e *SettlementAbortError) Error() string { return e.Err.Error() }
func (e *SettlementAbortError) Unwrap() error { return e.Err }

// Permissions is the set of tools allowed by name. Materialize only announces
// and settles entries set to true; a missing or false entry is denied.
type Permissions map[string]bool

// Materialized contains the definitions announced to the model and a settlement
// function closed over exactly that set.
type Materialized struct {
	Definitions    []llm.ToolDef
	Settle         SettleFunc
	Preview        PreviewFunc
	MatcherEntries MatcherEntriesFunc
	Err            error
}

// UnknownToolError is returned when a call names a tool outside the materialized
// set, whether unregistered or excluded by permissions. Nothing is executed.
type UnknownToolError struct{ Name string }

func (e *UnknownToolError) Error() string {
	return fmt.Sprintf("tool %q is unknown or not allowed", e.Name)
}

// Registry is the agent's tool catalog and execution policy. Configuration is
// protected by mu and snapshotted by Materialize; OutputStore protects its own
// mutable state.
type Registry struct {
	tools               map[string]Tool
	presentationAliases map[string]Tool
	outputs             *OutputStore
	middlewares         []Middleware
	mu                  sync.RWMutex
	gate                permission.Gate
	policy              permission.Policy
}

// SetPermissionGate configures the built-in permission middleware. Configuration
// is snapshotted by Materialize, so already-materialized settlements do not
// change underneath an in-flight turn.
func (r *Registry) SetPermissionGate(gate permission.Gate, policy permission.Policy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gate, r.policy = gate, policy
}

// Use appends execution middleware after the three built-ins. Configure a
// registry before serving turns; Materialize snapshots the slice for each turn.
func (r *Registry) Use(middlewares ...Middleware) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.middlewares = append(r.middlewares, middlewares...)
}

// NewRegistry indexes tools by name and records immutable presentation aliases.
// If canonical names collide, the final tool wins; registration is host
// configuration rather than model input. Presentation aliases never participate
// in execution routing: Materialize builds that routing afresh for each turn.
func NewRegistry(outputs *OutputStore, tools ...Tool) *Registry {
	m := make(map[string]Tool, len(tools))
	for _, t := range tools {
		m[t.Name()] = t
	}
	presentationAliases := make(map[string]Tool)
	for _, t := range m {
		aliaser, ok := t.(PresentationAliaser)
		if !ok {
			continue
		}
		for alias, presenter := range aliaser.PresentationAliases() {
			if alias != "" && alias != t.Name() && presenter != nil {
				presentationAliases[alias] = presenter
			}
		}
	}
	return &Registry{tools: m, presentationAliases: presentationAliases, outputs: outputs}
}

// Permissions returns a permission set containing every registered tool.
// The returned map is independent from the Registry, so callers may narrow it
// without mutating the catalog. Assembly code should use this instead of
// repeating tool names next to NewRegistry: registration then remains the
// single source of truth for the default tool set.
func (r *Registry) Permissions() Permissions {
	permissions := make(Permissions, len(r.tools))
	for name := range r.tools {
		permissions[name] = true
	}
	return permissions
}

type turnFreezer interface {
	FreezeFor(model, sessionID string) (Tool, error)
}

// Materialize filters the catalog by permissions and returns definitions plus a
// SettleFunc closed over the allowed tools. Definitions are sorted by name for
// deterministic requests. A call outside the set returns UnknownToolError before
// execution, so denied and unknown tools cannot produce side effects.
func (r *Registry) Materialize(perms Permissions) Materialized { return r.materialize(perms, "", "") }

func (r *Registry) MaterializeFor(perms Permissions, model, sessionID string) Materialized {
	return r.materialize(perms, model, sessionID)
}

func (r *Registry) materialize(perms Permissions, model, sessionID string) Materialized {
	r.mu.RLock()
	gate, policy := r.gate, r.policy
	extensions := append([]Middleware(nil), r.middlewares...)
	r.mu.RUnlock()
	allowed := make(map[string]Tool, len(r.tools))
	routes := make(map[string]string, len(r.tools))
	defs := make([]llm.ToolDef, 0, len(r.tools))
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		registered := r.tools[name]
		if !perms[name] {
			continue
		}
		t := registered
		if freezer, ok := registered.(turnFreezer); ok {
			var err error
			t, err = freezer.FreezeFor(model, sessionID)
			if err != nil {
				return Materialized{Err: fmt.Errorf("materialize tool %q: %w", name, err)}
			}
		} else if freezer, ok := registered.(Freezer); ok {
			t = freezer.Freeze()
		}
		if previous, exists := routes[name]; exists && previous != name {
			return Materialized{Err: fmt.Errorf("tool route %q collides between %q and %q", name, previous, name)}
		}
		allowed[name] = t
		routes[name] = name
		def := llm.ToolDef{Name: t.Name(), Description: t.Description(), Schema: t.Schema()}
		if custom, ok := t.(Definer); ok {
			published := custom.Definition()
			def.Name, def.Description, def.Schema, def.WireName = published.Name, published.Description, published.Schema, published.WireName
			if published.Name != "" && published.Name != name {
				return Materialized{Err: fmt.Errorf("tool %q published conflicting stable name %q", name, published.Name)}
			}
			if published.CustomFormat != nil {
				def.CustomFormat = &llm.ToolCustomFormat{Syntax: published.CustomFormat.Syntax, Definition: published.CustomFormat.Definition}
			}
			if published.WireName != "" && published.WireName != name {
				if previous, exists := routes[published.WireName]; exists && previous != name {
					return Materialized{Err: fmt.Errorf("tool wire alias %q collides with %q", published.WireName, previous)}
				}
				allowed[published.WireName] = t
				routes[published.WireName] = name
			}
		}
		defs = append(defs, def)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })

	execute := func(ctx context.Context, call Call) (Result, error) {
		t, ok := allowed[call.Name]
		if !ok {
			return Result{}, &UnknownToolError{Name: call.Name}
		}
		call.Name = t.Name()
		return t.Execute(ctx, call.Input)
	}
	middlewares := []Middleware{
		repairMiddleware(allowed),
		permissionMiddleware(gate, policy),
		outputMiddleware(r.outputs),
	}
	middlewares = append(middlewares, extensions...)
	settle := execute
	for i := len(middlewares) - 1; i >= 0; i-- {
		settle = middlewares[i](settle)
	}
	settleWithCanonicalRoute := settle
	settle = func(ctx context.Context, call Call) (Result, error) {
		if canonical, ok := routes[call.Name]; ok {
			call.Name = canonical
		}
		return settleWithCanonicalRoute(ctx, call)
	}
	previewers := make([]Previewer, 0, 1)
	seenPreviewer := make(map[Tool]bool)
	for _, candidate := range allowed {
		if capability, ok := candidate.(Previewer); ok && !seenPreviewer[candidate] {
			seenPreviewer[candidate] = true
			previewers = append(previewers, capability)
		}
	}
	preview := func(ctx context.Context, call Call) (Preview, bool) {
		t, ok := allowed[call.Name]
		if !ok && call.Name == "" && len(previewers) == 1 {
			return previewers[0].Preview(ctx, call.Input), true
		}
		if !ok {
			return Preview{}, false
		}
		capability, ok := t.(Previewer)
		if !ok {
			return Preview{}, false
		}
		return capability.Preview(ctx, call.Input), true
	}
	matcherEntries := func(call Call) []MatcherEntry {
		t, ok := allowed[call.Name]
		if !ok {
			return nil
		}
		capability, ok := t.(Previewer)
		if !ok {
			return nil
		}
		return capability.MatcherEntries(call.Input)
	}
	return Materialized{Definitions: defs, Settle: settle, Preview: preview, MatcherEntries: matcherEntries}
}

func permissionMiddleware(gate permission.Gate, policy permission.Policy) Middleware {
	return func(next SettleFunc) SettleFunc {
		return func(ctx context.Context, call Call) (Result, error) {
			if gate == nil || policy == nil {
				return next(ctx, call)
			}
			sessionID := SessionIDFrom(ctx)
			switch policy.Decide(sessionID, call) {
			case permission.Deny:
				return Result{}, ErrPermissionDenied
			case permission.Ask:
				request, _ := ctx.Value(permissionRequestKey{}).(PermissionRequester)
				if request == nil {
					return Result{}, ErrPermissionUnresolved
				}
				approved, err := request(ctx, permission.Request{SessionID: sessionID, CallID: call.ID, ToolName: call.Name, Input: call.Input})
				if err != nil {
					var abort *SettlementAbortError
					if errors.As(err, &abort) {
						return Result{}, err
					}
					return Result{}, ErrPermissionUnresolved
				}
				if !approved {
					return Result{}, ErrPermissionDenied
				}
			}
			return next(ctx, call)
		}
	}
}

func repairMiddleware(tools map[string]Tool) Middleware {
	return func(next SettleFunc) SettleFunc {
		return func(ctx context.Context, call Call) (Result, error) {
			t, ok := tools[call.Name]
			if !ok || len(call.Input) == 0 {
				return next(ctx, call)
			}
			input, notes, err := repair.Repair(call.Name, t.Schema(), call.Input)
			if err != nil {
				return Result{}, err
			}
			call.Input = input
			return next(context.WithValue(ctx, executionStateKey{}, &executionState{repairNotes: notes}), call)
		}
	}
}

func outputMiddleware(outputs *OutputStore) Middleware {
	return func(next SettleFunc) SettleFunc {
		return func(ctx context.Context, call Call) (Result, error) {
			res, err := next(ctx, call)
			if state, ok := ctx.Value(executionStateKey{}).(*executionState); ok && res.Output != "" {
				res.Output = repair.WithNotes(state.repairNotes, res.Output)
			}
			// Structured partial results are settlement evidence even when execution
			// fails (for example, an earlier file was already committed).
			return outputs.CapResult(call.ID, res), err
		}
	}
}

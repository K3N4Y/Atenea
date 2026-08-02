package permission

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	"github.com/K3N4Y/atenea/internal/tool"
)

// The doubles below model the three states a tool can be in with respect to the
// optional capability interfaces. A Go type either has a method or does not, so
// silence cannot be a field: silentTool is what an MCP tool looks like, and each
// of the others adds exactly one capability on top of it.

type silentTool struct{ name string }

func (s silentTool) Name() string            { return s.name }
func (s silentTool) Description() string     { return s.name + " stub" }
func (s silentTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s silentTool) Execute(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.Result{}, nil
}

// declaringTool declares what its calls affect.
type declaringTool struct {
	silentTool
	effects tool.Effects
}

func (d declaringTool) Effects() tool.Effects { return d.effects }

type callDeclaringTool struct{ declaringTool }

func (d callDeclaringTool) CallEffects(call tool.Call) tool.Effects {
	if string(call.Input) == `{"write":true}` {
		return tool.WritesFiles
	}
	return tool.NoEffects
}

// grantableTool declares its effects and offers a grant. grantable false is the
// tool that refuses to summarize this particular input.
type grantableTool struct {
	declaringTool
	rule      Rule
	grantable bool
}

func (g grantableTool) GrantRule(tool.Call) (Rule, bool) { return g.rule, g.grantable }

// catalog is a tool.Catalog for the tests: the tools it was handed, by name.
type catalog map[string]tool.Tool

func (c catalog) Lookup(name string) (tool.Tool, bool) {
	t, ok := c[name]
	return t, ok
}

func (c catalog) Names() []string {
	names := make([]string, 0, len(c))
	for name := range c {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func declaring(name string, effects tool.Effects) declaringTool {
	return declaringTool{silentTool: silentTool{name: name}, effects: effects}
}

// shippedCatalog mirrors how atenea's own tools declare themselves, so the
// classification is asserted against the real vocabulary rather than an invented
// one.
func shippedCatalog() catalog {
	return catalog{
		"bash":         declaring("bash", tool.RunsCommands),
		"write":        declaring("write", tool.WritesFiles),
		"edit":         declaring("edit", tool.WritesFiles),
		"web_fetch":    declaring("web_fetch", tool.ReachesNetwork),
		"read":         declaring("read", tool.NoEffects),
		"glob":         declaring("glob", tool.NoEffects),
		"grep":         declaring("grep", tool.NoEffects),
		"skill":        declaring("skill", tool.NoEffects),
		"todo_write":   declaring("todo_write", tool.NoEffects),
		"present_plan": declaring("present_plan", tool.NoEffects),
		"task":         declaring("task", tool.NoEffects),
		// An MCP server's tool: registered, and silent about what it does.
		"mcp_github_create_issue": silentTool{name: "mcp_github_create_issue"},
	}
}

// TestDecision_ZeroValueIsAsk pins the fail-safe invariant: an unclassified
// (zero-valued) Decision asks instead of silently allowing.
func TestDecision_ZeroValueIsAsk(t *testing.T) {
	var d Decision
	if d != Ask {
		t.Fatalf("zero Decision = %v, want Ask", d)
	}
}

// TestEffectsPolicy_AsksForDeclaredEffects is the classification contract: a tool
// that declares an effect outside the conversation asks, one that declares none is
// allowed, and the decision comes from the tool rather than from a list of names.
func TestEffectsPolicy_AsksForDeclaredEffects(t *testing.T) {
	p := NewEffectsPolicy(shippedCatalog())

	cases := []struct {
		name string
		want Decision
	}{
		{"bash", Ask},
		{"write", Ask},
		{"edit", Ask},
		{"web_fetch", Ask},
		{"read", Allow},
		{"glob", Allow},
		{"grep", Allow},
		{"skill", Allow},
		{"todo_write", Allow},
		{"present_plan", Allow},
		{"task", Allow},
	}
	for _, tc := range cases {
		if got := p.Decide("s1", tool.Call{Name: tc.name}); got != tc.want {
			t.Errorf("Decide(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestEffectsPolicy_UsesPerCallEffects(t *testing.T) {
	p := NewEffectsPolicy(catalog{"deep": callDeclaringTool{declaring("deep", tool.NoEffects)}})
	if got := p.Decide("s1", tool.Call{Name: "deep", Input: json.RawMessage(`{"query":true}`)}); got != Allow {
		t.Fatalf("read-only call = %v, want Allow", got)
	}
	if got := p.Decide("s1", tool.Call{Name: "deep", Input: json.RawMessage(`{"write":true}`)}); got != Ask {
		t.Fatalf("mutating call = %v, want Ask", got)
	}
}

// TestEffectsPolicy_AsksForUndeclaredTools is the security default of the
// extension boundary: a registered tool that says nothing about what it affects —
// every tool an MCP server contributes — is asked about, not allowed. Before this
// policy any third-party server got unattended execution.
func TestEffectsPolicy_AsksForUndeclaredTools(t *testing.T) {
	p := NewEffectsPolicy(shippedCatalog())
	if got := p.Decide("s1", tool.Call{Name: "mcp_github_create_issue"}); got != Ask {
		t.Errorf("Decide(mcp tool) = %v, want Ask: an undeclared tool has not earned unattended execution", got)
	}
}

// TestEffectsPolicy_AnyDeclaredEffectAsks: the rule is "any effect", not a list of
// gated flags, so a flag added to the vocabulary later can only ever leave the
// host more careful.
func TestEffectsPolicy_AnyDeclaredEffectAsks(t *testing.T) {
	const future = tool.Effects(1 << 15) // a flag this policy has never heard of
	p := NewEffectsPolicy(catalog{"x": declaring("x", future)})
	if got := p.Decide("s1", tool.Call{Name: "x"}); got != Ask {
		t.Errorf("Decide(tool declaring an unknown effect) = %v, want Ask", got)
	}
}

// TestEffectsPolicy_UnregisteredToolIsAllowed: a name the registry does not know
// cannot run either way — Settle refuses it before executing anything — so the
// policy stays out of the way and lets the model see "unknown tool" instead of a
// permission prompt for a call that can never happen or a denial that misnames the
// problem.
func TestEffectsPolicy_UnregisteredToolIsAllowed(t *testing.T) {
	p := NewEffectsPolicy(shippedCatalog())
	for _, name := range []string{"nope", "Bash", ""} {
		if got := p.Decide("s1", tool.Call{Name: name}); got != Allow {
			t.Errorf("Decide(%q) = %v, want Allow: unregistered names are refused by Settle, not by the policy", name, got)
		}
	}
}

// TestEffectsPolicy_NilCatalogAsksForEverything: without a catalog nothing can be
// shown to be harmless, so nothing is.
func TestEffectsPolicy_NilCatalogAsksForEverything(t *testing.T) {
	p := NewEffectsPolicy(nil)
	if got := p.Decide("s1", tool.Call{Name: "read"}); got != Ask {
		t.Errorf("Decide(read) with a nil catalog = %v, want Ask", got)
	}
}

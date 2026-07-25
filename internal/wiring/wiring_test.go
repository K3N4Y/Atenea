package wiring

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/K3N4Y/atenea/internal/event"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool"
)

// TestSkillDirs_ProjectBeforeGlobalDeduped: skillDirs lista primero las rutas del
// proyecto (root) y luego las globales (home), en el orden .atenea/.agents/.claude,
// para que una skill del proyecto override a una global homonima. Rutas identicas
// (root == home) se deduplican.
func TestSkillDirs_ProjectBeforeGlobalDeduped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := skillDirs("/proj")
	want := []string{
		filepath.Join("/proj", ".atenea", "skills"),
		filepath.Join("/proj", ".agents", "skills"),
		filepath.Join("/proj", ".claude", "skills"),
		filepath.Join(home, ".atenea", "skills"),
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".claude", "skills"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("skillDirs orden = %v,\n want %v", got, want)
	}
	// root == home: las rutas coinciden, deben deduplicarse a las 3 del home.
	if d := skillDirs(home); len(d) != 3 {
		t.Fatalf("root==home debe deduplicar a 3 dirs, got %v", d)
	}
}

func TestBuild_InstallsContextCompactor(t *testing.T) {
	store := session.NewMemoryStore()
	ctx := context.Background()
	for _, message := range []session.Message{
		{ID: "u1", Role: session.RoleUser, Text: "old"},
		{ID: "a1", Role: session.RoleAssistant, Text: "answer"},
		{ID: "u2", Role: session.RoleUser, Text: "current"},
	} {
		message := message
		if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{Message: &message}); err != nil {
			t.Fatal(err)
		}
	}
	provider := llm.NewFakeProvider(
		llm.Event{Kind: llm.TextDelta, Text: `{"current_goal":"continue","constraints_and_instructions":[],"decisions":[],"completed_work":[],"files_and_changes":[],"relevant_tool_results":[],"failures_and_attempts":[],"pending_and_next_step":[],"facts_not_to_reinterpret":[]}`},
		llm.Event{Kind: llm.StepEnded},
	)
	built := Build(Config{
		Root:     t.TempDir(),
		Provider: provider,
		Store:    store,
		Inbox:    session.NewMemoryInbox(),
		Gate:     permission.NewMemoryGate(),
		Snaps:    tool.NewSessionSnapshots(),
		Bus:      event.NewBus(func(string, ...interface{}) {}),
		NextID:   func() string { return "id" },
	})

	if err := built.Runner.CompactNow(ctx, "s1"); err != nil {
		t.Fatalf("CompactNow() error = %v", err)
	}
	events, err := store.Events(ctx, "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if last := events[len(events)-1]; last.Kind != session.KindContextCompacted {
		t.Fatalf("last event = %+v, want Context.Compacted", last)
	}
}

// buildForTest assembles the real wiring over an empty workspace, with the MCP
// tools the case wants to see in the registry.
func buildForTest(t *testing.T, mcpTools ...tool.Tool) Built {
	t.Helper()
	return Build(Config{
		Root:     t.TempDir(),
		Provider: llm.NewFakeProvider(llm.Event{Kind: llm.StepEnded}),
		Store:    session.NewMemoryStore(),
		Inbox:    session.NewMemoryInbox(),
		Gate:     permission.NewMemoryGate(),
		Snaps:    tool.NewSessionSnapshots(),
		Bus:      event.NewBus(func(string, ...interface{}) {}),
		NextID:   func() string { return "id" },
		MCPTools: mcpTools,
	})
}

// TestBuild_EveryShippedToolDeclaresItsEffects is the invariant that keeps the
// classification honest as tools are added. A tool that forgets to declare gets
// gated — which is safe but wrong for a read-only tool, and the failure would show
// up as an unexplained permission prompt rather than as a test failure. So the
// registration itself is checked.
func TestBuild_EveryShippedToolDeclaresItsEffects(t *testing.T) {
	built := buildForTest(t)
	for _, name := range built.Tools.Names() {
		registered, ok := built.Tools.Lookup(name)
		if !ok {
			t.Fatalf("tool %q is announced but not registered", name)
		}
		if _, declared := tool.EffectsOf(registered); !declared {
			t.Errorf("tool %q does not implement tool.Declaring: add an Effects() method beside its Schema()", name)
		}
	}
}

// TestBuild_PolicyGatesShellFSAndNetwork pins the classification the assembled
// agent runs with. It is derived from what each tool declares rather than from a
// list kept in this package, so the assertion is over the real registry: shell
// (bash), local FS mutations (write, edit) and outbound network (web_fetch) ask;
// reads and the tools that never leave the session are allowed.
func TestBuild_PolicyGatesShellFSAndNetwork(t *testing.T) {
	policy := buildForTest(t).Policy
	cases := []struct {
		name string
		want permission.Decision
	}{
		{"bash", permission.Ask},
		{"write", permission.Ask},
		{"edit", permission.Ask},
		{"web_fetch", permission.Ask},
		{"read", permission.Allow},
		{"glob", permission.Allow},
		{"grep", permission.Allow},
		{"skill", permission.Allow},
		{"todo_write", permission.Allow},
		{"present_plan", permission.Allow},
		{"task", permission.Allow},
	}
	for _, tc := range cases {
		if got := policy.Decide("s1", tool.Call{Name: tc.name}); got != tc.want {
			t.Errorf("policy.Decide(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestBuild_PolicyAsksForMCPTools is the security default of the extension
// boundary, asserted end to end: an MCP server's tool says nothing about what it
// affects, so it is asked about instead of run unattended. Before the
// effects-derived policy any connected server got unattended execution.
func TestBuild_PolicyAsksForMCPTools(t *testing.T) {
	remote := undeclaredTool{name: "mcp_github_create_issue"}
	built := buildForTest(t, remote)
	if got := built.Policy.Decide("s1", tool.Call{Name: remote.name}); got != permission.Ask {
		t.Errorf("policy.Decide(%q) = %v, want Ask", remote.name, got)
	}
}

// undeclaredTool stands in for an MCP server's tool: registered, and silent about
// what its calls affect. It deliberately does not implement tool.Declaring.
type undeclaredTool struct{ name string }

func (u undeclaredTool) Name() string        { return u.name }
func (u undeclaredTool) Description() string { return u.name + " (remote)" }
func (u undeclaredTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (u undeclaredTool) Execute(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.Result{}, nil
}

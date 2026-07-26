package wiring

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/K3N4Y/atenea/internal/event"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool"
)

// TestDefaultSkillDirs_ProjectBeforeGlobalDeduped: the default list puts the
// project's paths (root) before the global ones (home), each in .atenea/.agents/
// .claude order, so a project skill overrides a global namesake. Identical paths
// (root == home) are deduplicated.
func TestDefaultSkillDirs_ProjectBeforeGlobalDeduped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := DefaultSkillDirs("/proj")
	want := []string{
		filepath.Join("/proj", ".atenea", "skills"),
		filepath.Join("/proj", ".agents", "skills"),
		filepath.Join("/proj", ".claude", "skills"),
		filepath.Join(home, ".atenea", "skills"),
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".claude", "skills"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultSkillDirs order = %v,\n want %v", got, want)
	}
	// root == home: the paths coincide and must collapse to the home's 3.
	if d := DefaultSkillDirs(home); len(d) != 3 {
		t.Fatalf("root==home must deduplicate to 3 dirs, got %v", d)
	}
}

// TestDefaultAgentDirs_ProjectOnly pins the asymmetry with the skills as it
// behaves today: subagents are searched for under the project root only, and
// .claude/agents is not read. The audit reads both as probably bugs; resolving
// them is R7's one ordered list, and this test is what makes that change visible
// when it happens instead of silent.
func TestDefaultAgentDirs_ProjectOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	got := DefaultAgentDirs("/proj")
	want := []string{
		filepath.Join("/proj", ".atenea", "agents"),
		filepath.Join("/proj", ".agents", "agents"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultAgentDirs = %v,\n want %v", got, want)
	}
}

// buildWith assembles the real wiring over the caller's Config: it fills in the
// dependency half every case needs and leaves the policy half exactly as the case
// wrote it, which is what most of these tests are about — what a caller that says
// nothing gets, and what one that says something does.
//
// HOME is a temp dir because the default skill directories include the user's own.
// Without it the developer's installed skills reach these assertions, and a test
// whose result depends on who runs it is worse than no test.
func buildWith(t *testing.T, cfg Config) Built {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if cfg.Root == "" {
		cfg.Root = t.TempDir()
	}
	if cfg.Provider == nil {
		cfg.Provider = llm.NewFakeProvider(llm.Event{Kind: llm.StepEnded})
	}
	if cfg.Store == nil {
		cfg.Store = session.NewMemoryStore()
	}
	if cfg.Inbox == nil {
		cfg.Inbox = session.NewMemoryInbox()
	}
	if cfg.Gate == nil {
		cfg.Gate = permission.NewMemoryGate()
	}
	if cfg.Snaps == nil {
		cfg.Snaps = tool.NewSessionSnapshots()
	}
	if cfg.Bus == nil {
		cfg.Bus = event.NewBus(func(string, ...interface{}) {})
	}
	if cfg.NextID == nil {
		cfg.NextID = func() string { return "id" }
	}
	return Build(cfg)
}

// buildForTest assembles the wiring over an empty workspace with the MCP tools the
// case wants to see in the registry, and nothing else varied.
func buildForTest(t *testing.T, mcpTools ...tool.Tool) Built {
	t.Helper()
	return buildWith(t, Config{MCPTools: mcpTools})
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
	built := buildWith(t, Config{Provider: provider, Store: store})

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

// TestBuild_PolicyGatesShellFSAndNetwork pins the classification a caller that
// leaves Config.Policy nil runs with. It is derived from what each tool declares
// rather than from a list kept in this package, so the assertion is over the real
// registry: shell (bash), local FS mutations (write, edit) and outbound network
// (web_fetch) ask; reads and the tools that never leave the session are allowed.
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

// TestBuild_PolicyFieldClassifiesOverThisAssemblysCatalog is why the field is a
// function of the catalog and not a policy value: the classification a caller
// installs has to see the registry of the moment. Here that registry contains a
// tool an MCP server contributed after the process started, which a policy built
// before Build could not have known about.
func TestBuild_PolicyFieldClassifiesOverThisAssemblysCatalog(t *testing.T) {
	remote := undeclaredTool{name: "mcp_github_create_issue"}
	var seen []string
	built := buildWith(t, Config{
		MCPTools: []tool.Tool{remote},
		Policy: func(catalog tool.Catalog) permission.Policy {
			seen = catalog.Names()
			return denyAll{}
		},
	})

	if !slices.Contains(seen, remote.name) {
		t.Errorf("the policy was built over a catalog without %q; names = %v", remote.name, seen)
	}
	// The caller's classification is the one in force, including for tools it would
	// have been allowed by the default.
	if got := built.Policy.Decide("s1", tool.Call{Name: "read"}); got != permission.Deny {
		t.Errorf("policy.Decide(\"read\") = %v, want Deny from the installed policy", got)
	}
}

// TestBuild_GrantsLayerOverTheInstalledPolicy: Build layers the session grants
// over whatever Config.Policy returns, in one place. So a caller's own
// classification inherits "allow for the rest of the session" without knowing the
// grant store exists — and cannot apply it a second time.
func TestBuild_GrantsLayerOverTheInstalledPolicy(t *testing.T) {
	grants := permission.NewSessionGrants()
	built := buildWith(t, Config{
		Grants: grants,
		Policy: func(tool.Catalog) permission.Policy { return askAll{} },
	})
	call := tool.Call{Name: "bash", Input: json.RawMessage(`{"command":"go test ./..."}`)}

	if got := built.Policy.Decide("s1", call); got != permission.Ask {
		t.Fatalf("policy.Decide(bash) = %v before the grant, want Ask", got)
	}
	rule, ok := permission.RuleFor(built.Tools, call)
	if !ok {
		t.Fatal("permission.RuleFor(bash) is not grantable; the case cannot test the layering")
	}
	grants.Grant("s1", rule)

	if got := built.Policy.Decide("s1", call); got != permission.Allow {
		t.Errorf("policy.Decide(bash) = %v after granting %+v, want Allow", got, rule)
	}
	if got := built.Policy.Decide("s2", call); got != permission.Ask {
		t.Errorf("policy.Decide(bash) in another session = %v, want Ask", got)
	}
}

// TestBuild_ZeroOutputLimitCapsAtTheDefault is the field whose zero value would be
// dangerous if it were passed through: tool.OutputStore reads a zero as no limit,
// so a caller that left the field alone would get uncapped tool output in the
// model's context. Zero means the default instead.
func TestBuild_ZeroOutputLimitCapsAtTheDefault(t *testing.T) {
	result := settleBigOutput(t, Config{})
	if !result.Truncated {
		t.Errorf("output of %d bytes was not truncated with OutputLimit left at zero", len(result.Output))
	}
	if len(result.Output) != DefaultOutputLimit {
		t.Errorf("capped output = %d bytes, want DefaultOutputLimit (%d)", len(result.Output), DefaultOutputLimit)
	}
}

// TestBuild_OutputLimitFieldCapsWhereItSays: a caller that sets the field gets
// exactly that cap, and a negative value is how "no cap at all" is said out loud —
// the answer zero deliberately does not stand for.
func TestBuild_OutputLimitFieldCapsWhereItSays(t *testing.T) {
	if result := settleBigOutput(t, Config{OutputLimit: 128}); len(result.Output) != 128 || !result.Truncated {
		t.Errorf("with OutputLimit 128: output = %d bytes, truncated = %v; want 128 and true",
			len(result.Output), result.Truncated)
	}
	if result := settleBigOutput(t, Config{OutputLimit: -1}); len(result.Output) != bigOutputSize || result.Truncated {
		t.Errorf("with OutputLimit -1: output = %d bytes, truncated = %v; want %d and false",
			len(result.Output), result.Truncated, bigOutputSize)
	}
}

// settleBigOutput assembles the wiring with one tool that returns more output than
// any default would allow, settles a call to it through the registry the runner
// uses, and returns what the model would have seen.
func settleBigOutput(t *testing.T, cfg Config) tool.Result {
	t.Helper()
	big := undeclaredTool{name: "big", output: strings.Repeat("x", bigOutputSize)}
	cfg.MCPTools = append(cfg.MCPTools, big)
	built := buildWith(t, cfg)
	registry, ok := built.Tools.(*tool.Registry)
	if !ok {
		t.Fatalf("Built.Tools is %T, want *tool.Registry to settle a call through it", built.Tools)
	}
	result, err := registry.Materialize(tool.Permissions{big.name: true}).
		Settle(context.Background(), tool.Call{ID: "c1", Name: big.name})
	if err != nil {
		t.Fatalf("Settle(%q) error = %v", big.name, err)
	}
	return result
}

const bigOutputSize = 64 * 1024

// TestBuild_ZeroSkillDirsDiscoversTheProjectSkills: a caller that says nothing
// gets today's discovery, which includes the project's own .atenea/skills and
// derives a slash-command from every skill found there.
func TestBuild_ZeroSkillDirsDiscoversTheProjectSkills(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, ".atenea", "skills", "deploy"), "deploy")

	built := buildWith(t, Config{Root: root})

	if _, ok := built.Commands.Resolve("/deploy"); !ok {
		t.Errorf("the project skill produced no /deploy command; commands = %+v", built.Commands.List())
	}
}

// TestBuild_SkillDirsFieldReplacesDiscovery: the field is the whole list, not an
// addition to it. What the caller names is searched and what it does not name is
// not, so an embedder that ships its own skills does not inherit the user's.
func TestBuild_SkillDirsFieldReplacesDiscovery(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, ".atenea", "skills", "deploy"), "deploy")
	elsewhere := t.TempDir()
	writeSkill(t, filepath.Join(elsewhere, "audit"), "audit")

	built := buildWith(t, Config{Root: root, SkillDirs: []string{elsewhere}})

	if _, ok := built.Commands.Resolve("/audit"); !ok {
		t.Errorf("the skill in the configured directory produced no /audit command; commands = %+v", built.Commands.List())
	}
	if _, ok := built.Commands.Resolve("/deploy"); ok {
		t.Error("the project's default directory was still scanned; SkillDirs must replace the list, not extend it")
	}
}

// TestBuild_EmptySkillDirsDiscoversNothing is the "said nothing" / "declared
// nothing" distinction, which a length check would flatten: nil asks for the
// defaults, an empty non-nil slice asks for no discovery at all.
func TestBuild_EmptySkillDirsDiscoversNothing(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, ".atenea", "skills", "deploy"), "deploy")

	built := buildWith(t, Config{Root: root, SkillDirs: []string{}})

	if commands := built.Commands.List(); len(commands) != 0 {
		t.Errorf("commands = %+v, want none: an empty SkillDirs scans nothing", commands)
	}
}

// writeSkill materializes a discoverable skill: a SKILL.md with the frontmatter
// discovery requires, in its own directory.
func writeSkill(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: the " + name + " skill\n---\n\nBody.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestBuild_ZeroAgentDirsDiscoversTheProjectSubagents: a caller that says nothing
// gets today's discovery, so a definition in the project's .atenea/agents is
// offered to the model by the task tool.
func TestBuild_ZeroAgentDirsDiscoversTheProjectSubagents(t *testing.T) {
	root := t.TempDir()
	writeAgent(t, filepath.Join(root, ".atenea", "agents"), "reviewer")

	if agents := subagentCatalog(t, buildWith(t, Config{Root: root})); !strings.Contains(agents, "reviewer") {
		t.Errorf("the project subagent is not in the task tool's catalog:\n%s", agents)
	}
}

// TestBuild_AgentDirsFieldReplacesDiscovery: as with the skills, the field is the
// whole list. The built-in subagents are not part of it — they are merged in after
// discovery — so they survive an empty one.
func TestBuild_AgentDirsFieldReplacesDiscovery(t *testing.T) {
	root := t.TempDir()
	writeAgent(t, filepath.Join(root, ".atenea", "agents"), "reviewer")
	elsewhere := t.TempDir()
	writeAgent(t, elsewhere, "auditor")

	agents := subagentCatalog(t, buildWith(t, Config{Root: root, AgentDirs: []string{elsewhere}}))

	if !strings.Contains(agents, "auditor") {
		t.Errorf("the subagent in the configured directory is not in the catalog:\n%s", agents)
	}
	if strings.Contains(agents, "reviewer") {
		t.Errorf("the project's default directory was still scanned:\n%s", agents)
	}
	if !strings.Contains(agents, "general") {
		t.Errorf("the built-in subagents are gone; they are merged in after discovery:\n%s", agents)
	}
}

// writeAgent materializes a discoverable subagent definition.
func writeAgent(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: the " + name + " subagent\ntools: read\n---\n\nPrompt.\n"
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// subagentCatalog returns the task tool's description, which is where the
// discovered subagents reach the model: one line per available subagent_type.
func subagentCatalog(t *testing.T, built Built) string {
	t.Helper()
	task, ok := built.Tools.Lookup("task")
	if !ok {
		t.Fatal("the task tool is not registered")
	}
	return task.Description()
}

// TestBuild_ZeroPlanModeAnnouncesTheDefaultSurface pins both halves of the field's
// zero value at once, because they are one decision: plan mode announces read-only
// investigation plus present_plan, and normal mode announces everything registered
// except the present_plan plan mode claimed.
func TestBuild_ZeroPlanModeAnnouncesTheDefaultSurface(t *testing.T) {
	plan := announcedTools(t, Config{}, session.ModePlan)
	want := []string{"glob", "grep", "present_plan", "read", "skill"}
	if !reflect.DeepEqual(plan, want) {
		t.Errorf("plan mode announced %v, want %v", plan, want)
	}

	normal := announcedTools(t, Config{}, session.ModeNormal)
	if slices.Contains(normal, "present_plan") {
		t.Errorf("normal mode announced present_plan; it is plan mode's alone. tools = %v", normal)
	}
	for _, name := range []string{"read", "write", "edit", "bash", "glob", "grep", "skill", "task", "todo_write", "web_fetch"} {
		if !slices.Contains(normal, name) {
			t.Errorf("normal mode did not announce %q; registration is what puts a tool there. tools = %v", name, normal)
		}
	}
}

// TestBuild_PlanModeFieldReplacesBothHalves is the coupling the struct exists for.
// One field decides what plan mode announces and what normal mode hides, so a
// caller that moves bash into plan mode's exclusive set gets it out of normal mode
// in the same breath — and present_plan, no longer claimed by anyone, becomes an
// ordinary registered tool.
func TestBuild_PlanModeFieldReplacesBothHalves(t *testing.T) {
	cfg := Config{PlanMode: &PlanMode{Tools: []string{"read"}, Exclusive: []string{"bash"}}}

	plan := announcedTools(t, cfg, session.ModePlan)
	if want := []string{"bash", "read"}; !reflect.DeepEqual(plan, want) {
		t.Errorf("plan mode announced %v, want %v", plan, want)
	}

	normal := announcedTools(t, cfg, session.ModeNormal)
	if slices.Contains(normal, "bash") {
		t.Errorf("normal mode announced bash, which plan mode now claims. tools = %v", normal)
	}
	if !slices.Contains(normal, "present_plan") {
		t.Errorf("normal mode did not announce present_plan, which nothing claims any more. tools = %v", normal)
	}
}

// announcedTools runs one turn of the assembled agent in the given mode and
// returns the tools that reached the model, sorted. The announced set is not
// readable off the runner, and the assertion belongs where the answer actually
// lands anyway: in the request the provider is handed.
func announcedTools(t *testing.T, cfg Config, mode session.Mode) []string {
	t.Helper()
	ctx := context.Background()
	store := session.NewMemoryStore()
	prompt := session.Message{ID: "u1", Role: session.RoleUser, Text: "hello"}
	if _, err := store.AppendEvent(ctx, "s1", session.SessionEvent{Message: &prompt}); err != nil {
		t.Fatal(err)
	}
	provider := &recordingProvider{FakeProvider: llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.StepEnded},
	)}
	cfg.Store = store
	cfg.Provider = provider
	cfg.Mode = func(string) session.Mode { return mode }

	built := buildWith(t, cfg)
	if err := built.Runner.Run(ctx, "s1", true); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	return provider.announced()
}

// recordingProvider captures the request the runner puts on the wire and then
// replays the fake's script, which is how a test reads the tool set a mode
// announces.
type recordingProvider struct {
	*llm.FakeProvider
	mu   sync.Mutex
	last llm.Request
}

func (p *recordingProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	p.last = req
	p.mu.Unlock()
	return p.FakeProvider.Stream(ctx, req)
}

// announced lists the names of the tool definitions of the last request, sorted.
func (p *recordingProvider) announced() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	names := make([]string, 0, len(p.last.Tools))
	for _, def := range p.last.Tools {
		names = append(names, def.Name)
	}
	slices.Sort(names)
	return names
}

// undeclaredTool stands in for an MCP server's tool: registered, and silent about
// what its calls affect. It deliberately does not implement tool.Declaring.
type undeclaredTool struct {
	name   string
	output string
}

func (u undeclaredTool) Name() string        { return u.name }
func (u undeclaredTool) Description() string { return u.name + " (remote)" }
func (u undeclaredTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (u undeclaredTool) Execute(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.Result{Output: u.output}, nil
}

// denyAll and askAll are classifications a caller installs through Config.Policy:
// one to prove the installed policy is the one in force, the other to prove Build
// layers the session grants over it.
type denyAll struct{}

func (denyAll) Decide(string, tool.Call) permission.Decision { return permission.Deny }

type askAll struct{}

func (askAll) Decide(string, tool.Call) permission.Decision { return permission.Ask }

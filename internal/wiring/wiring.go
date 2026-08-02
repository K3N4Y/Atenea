// Package wiring assembles the shared agent (tools, skills, subagents, runner)
// anchored to a workspace root. It is the inner composition root: the Wails app
// (through internal/wailsworkspace) and the TUI's headless engine build the same
// agent by calling Build with their own dependencies.
//
// The split with internal/host is by lifetime. The host owns what is built once
// per process; this package owns what is rebuilt whenever the workspace root, the
// set of connected MCP tools or the active model changes. See
// .okf/architecture/composition-root.md.
package wiring

import (
	"log"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/K3N4Y/atenea/internal/agent"
	"github.com/K3N4Y/atenea/internal/command"
	"github.com/K3N4Y/atenea/internal/event"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/paths"
	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/session/prompt"
	"github.com/K3N4Y/atenea/internal/session/runner"
	"github.com/K3N4Y/atenea/internal/session/subagent"
	"github.com/K3N4Y/atenea/internal/skill"
	"github.com/K3N4Y/atenea/internal/tool"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

// Config is what the caller contributes to the assembly, in two halves.
//
// The first half is the dependencies: the workspace root, the provider, the
// durable store and the pieces of the sitting that have to survive a rewire.
// Nothing here has a useful default and the caller always passes it.
//
// The second half — from OutputLimit down — is the assembly's policy: how much of
// a tool's output the model sees, how a call is classified before it runs, where
// skills and subagents are discovered, and what plan mode announces. Each of
// those was a literal in this file until an embedder needed to vary it. Every one
// of them has a documented zero value that is atenea's own answer, pinned by a
// test, so the two hosts that vary none of them keep passing nothing rather than
// each spelling out the same default.
type Config struct {
	// Root is the workspace root: it anchors the file and exec tools, the skills,
	// the subagents and the system prompt.
	Root string
	// Provider is the model, shared by the runner, the subagents (task) and the
	// web_fetch tool.
	Provider llm.Provider
	// Store is the durable session log the runner writes to. It arrives already
	// decorated by the caller (EmittingStore over that host's bus); Build does not
	// wrap it again.
	Store session.Store
	// Inbox is the prompt queue the runner drains per session.
	Inbox session.Inbox
	// Gate is the ask-before-run broker shared by the main runner and the
	// subagents; the caller delivers the user's decision through it.
	Gate permission.Gate
	// Grants is the caller-owned store of session-scoped approvals layered over
	// the classification (see permission.GrantedPolicy). It outlives the build so
	// a rewire does not drop the user's grants; nil = no session grants, every
	// gated call asks.
	Grants *permission.SessionGrants
	// AutoAccept is sitting-owned and therefore survives rewires.
	AutoAccept *permission.AutoAcceptModes
	// Yolo is process-local interactive authorization and activation state.
	Yolo *permission.YoloMode
	// Snaps is the per-session read state that read, write and edit share. The
	// caller creates it once so it survives a re-assembly.
	Snaps *tool.SessionSnapshots
	// Bus publishes the subagents' permission events on the parent's channel
	// (ChildPermissionStore), the same one the frontend already listens to.
	Bus *event.Bus
	// LocalPrompt answers, once per turn, whether the provider that will serve it
	// runs local models (LM Studio, Ollama): then the base system prompt switches
	// to the explicit function-calling protocol instead of routing by model
	// family, because a local model id says nothing about its family. It is a
	// question rather than a flag so a live provider switch takes effect on the
	// next turn without re-assembling anything. nil means never local.
	// Reasoning is read for every turn so both hosts share the same preference
	// without rebuilding the workspace.
	Reasoning   func() *llm.ReasoningPreference
	LocalPrompt func() bool
	// NextID generates the runner's assistantMessageIDs (see NewIDGen).
	NextID func() string
	// Mode is the per-session mode hook (normal/plan) the runner consults every
	// turn; nil = always normal mode. PlanMode below is what the plan half of that
	// answer is allowed to do.
	Mode func(sessionID string) session.Mode
	// MCPTools are the tools discovered from already-connected MCP servers.
	MCPTools []tool.Tool
	// PersistentGrants are approvals loaded from durable configuration. They are
	// composed before session grants, and can only turn Ask into Allow.
	PersistentGrants []permission.Rule

	// OutputLimit caps, in bytes, how much of a tool call's output reaches the
	// model. The whole output is always kept for the UI to expand
	// (tool.OutputStore), so this is a context-window budget rather than data
	// loss.
	//
	// Zero is DefaultOutputLimit and deliberately not "no limit", even though a
	// zero is exactly what tool.NewOutputStore reads as no limit: one runaway
	// command quietly evicting a conversation from the context window is not
	// something a caller can mean by leaving a field alone. Saying it out loud is
	// a negative value, which the store reads as no cap at all.
	OutputLimit int
	// Policy builds the ask-before-run classification over the catalog of this
	// assembly. It is a function of the catalog rather than a permission.Policy
	// the caller constructs once because a classification is only as good as the
	// catalog it reads, and that catalog changes with every rewire: an MCP server
	// that just connected contributes tools the policy has to be able to see, and
	// a value built before Build could only ever answer for the tools of the
	// previous assembly.
	//
	// It returns the *base* classification, not the final one: Build layers Grants
	// over whatever comes back. So a caller's own classification inherits "allow
	// for the rest of the session" without knowing the grant store exists, and
	// there is exactly one place that can apply grants — the two fields cannot end
	// up layering them twice, or disagreeing about the order.
	//
	// nil is DefaultPolicy.
	Policy func(catalog tool.Catalog) permission.Policy
	// SkillDirs is the ordered list of directories skills are discovered in. The
	// first occurrence of a name wins, so an earlier directory overrides a later
	// one.
	//
	// nil is DefaultSkillDirs(Root). An empty non-nil slice is a different answer
	// and is honored as one: nothing is scanned and the agent runs with no skills,
	// which is what an embedder that ships its own set says. Collapsing the two
	// would be the same mistake every optional capability in this tree avoids —
	// "said nothing" is not "declared nothing".
	SkillDirs []string
	// AgentDirs is the ordered list of directories subagent definitions are
	// discovered in, first occurrence of a name winning. The built-in subagents
	// are merged in after them, so a definition may override a built-in by name.
	//
	// nil is DefaultAgentDirs(Root); an empty non-nil slice means the built-ins
	// alone, on the same reading as SkillDirs.
	AgentDirs []string
	// PlanMode is plan mode's tool surface: what the runner announces while Mode
	// reports session.ModePlan and, through PlanMode.Exclusive, what normal mode
	// therefore hides. nil is DefaultPlanMode().
	//
	// It is one field, and a struct rather than the plain permission set, because
	// the plan set and the normal-mode exclusion are one decision seen from two
	// sides. As two fields they could contradict each other — a tool excluded from
	// normal mode but missing from the plan set would be registered, executable
	// and announced nowhere — and a configuration that can be set to an incoherent
	// state is worse than the constant it replaced.
	PlanMode *PlanMode
}

// PlanMode describes plan mode's tool surface. Plan mode announces Tools and
// Exclusive together; normal mode announces every registered tool except
// Exclusive.
//
// The split is by who else may see the tool, and it is what keeps registration
// the source of truth for normal mode. Every ordinary tool, and every tool an MCP
// server contributes, reaches normal mode by being registered — no list names it
// — and Exclusive is the single declared deviation from that. present_plan is its
// only member today: the shared registry has to hold it for plan mode to execute
// it, while announcing it in normal mode would invite the model to present a plan
// nobody asked for.
//
// The set is not derivable from what a tool declares about itself, which is why it
// is configuration and not a capability: todo_write declares tool.NoEffects and is
// deliberately outside plan mode, so effects cannot tell the two apart. What plan
// mode means is a product decision about investigation.
type PlanMode struct {
	// Tools are announced in plan mode and left alone in normal mode, where they
	// arrive by being registered like any other tool.
	Tools []string
	// Exclusive are announced in plan mode and hidden from normal mode.
	Exclusive []string
}

// permissions is the set plan mode announces: Tools and Exclusive together, so a
// tool claimed by plan mode is always announced by it.
func (p PlanMode) permissions() tool.Permissions {
	perms := make(tool.Permissions, len(p.Tools)+len(p.Exclusive))
	for _, name := range p.Tools {
		perms[name] = true
	}
	for _, name := range p.Exclusive {
		perms[name] = true
	}
	return perms
}

// DefaultOutputLimit is the cap Build applies when Config leaves OutputLimit at
// zero. 32 KiB is a few hundred lines of a file or of test output: enough for the
// model to work from, small enough that one call cannot spend the whole context
// window.
const DefaultOutputLimit = 32 * 1024

// DefaultPolicy is the classification Build applies when Config leaves Policy
// nil: every tool judged by the effects it declares about itself, and a tool that
// declares nothing asked about rather than assumed harmless. See
// permission.EffectsPolicy for why silence is gated.
func DefaultPolicy(catalog tool.Catalog) permission.Policy {
	return permission.NewEffectsPolicy(catalog)
}

// DefaultSkillDirs is retained for compatibility. New callers use
// paths.SkillDirs, which owns the discovery layout.
func DefaultSkillDirs(root string) []string {
	return paths.SkillDirs(root)
}

// DefaultAgentDirs is retained for compatibility. New callers use
// paths.AgentDirs, which owns the discovery layout.
func DefaultAgentDirs(root string) []string {
	return paths.AgentDirs(root)
}

// DefaultPlanMode is the plan surface Build applies when Config leaves PlanMode
// nil: read-only investigation (read, glob, grep, skill) plus present_plan, which
// belongs to plan mode alone. No write, no edit, no bash — a plan is proposed, not
// carried out.
//
// Fresh value per call, for the reason DefaultSkillDirs states.
func DefaultPlanMode() PlanMode {
	return PlanMode{
		Tools:     []string{"read", "glob", "grep", "skill"},
		Exclusive: []string{"present_plan"},
	}
}

// resolve fills in atenea's own answers for the policy fields the caller left
// alone, on the copy Build works from. It is the only place that reads a zero
// value as a default, so the behavior documented on each field and the behavior
// Build applies cannot drift apart.
func (c Config) resolve() Config {
	if c.OutputLimit == 0 {
		c.OutputLimit = DefaultOutputLimit
	}
	if c.Policy == nil {
		c.Policy = DefaultPolicy
	}
	if c.SkillDirs == nil {
		c.SkillDirs = paths.SkillDirs(c.Root)
	}
	if c.AgentDirs == nil {
		c.AgentDirs = paths.AgentDirs(c.Root)
	}
	if c.PlanMode == nil {
		plan := DefaultPlanMode()
		c.PlanMode = &plan
	}
	return c
}

// Built are the mutable pieces the caller publishes after the assembly: the
// runner ready to run, the glob of the @-menu and the composer's slash-commands.
type Built struct {
	Runner   *runner.Runner
	Glob     *tool.GlobTool
	Commands *command.Set
	// Tools is the catalog the UI asks about a tool it only knows by name: what a
	// finished call may have changed, what granting one would authorize, how one
	// should be presented. It is the same registry the runner settles against, so
	// the answers a user sees are the ones the agent acted on. Rebuilt with every
	// Build, hence published here for the caller to swap alongside the runner.
	Tools tool.Catalog
	// Policy is the ask-before-run classification the assembled agent enforces,
	// derived from Tools and shared by the main runner and the subagents. Published
	// so what the agent gates stays answerable from outside the assembly instead of
	// only from a turn.
	Policy permission.Policy
}

// Build assembles the whole wiring anchored at cfg.Root: the file and exec tools,
// the @-menu's glob, the skills and their slash-commands, the subagent catalog
// and a fresh runner with the system prompt pointing at the root. It mutates no
// global state: it returns the pieces so the caller performs its own swap.
func Build(cfg Config) Built {
	cfg = cfg.resolve()
	root := cfg.Root
	// The composer's file @-menu lists the workspace through this glob. It shares
	// the root with the file tools and reuses the already-tested ripgrep searcher
	// (honors .gitignore, excludes .git).
	glob := tool.NewGlobTool(root)
	// Skills in the opencode style (progressive disclosure): discovered under the
	// configured directories, the project's and the home's global ones by default
	// (DefaultSkillDirs). Their metadata goes in the system prompt (skill.Format), the
	// skill tool loads the body on demand, and each one derives a slash-command. A
	// discovery failure is not fatal: no skills.
	skills, err := skill.Discover(cfg.SkillDirs...)
	if err != nil {
		log.Printf("atenea: could not discover the skills: %v", err)
	}
	skillsBlock := skill.Format(skills)
	// The composer's slash-commands, derived from the skills (one "/<name>" per skill).
	commands := command.New(command.FromSkills(skills))
	// Subagents: agent.Catalog merges the manifests packaged from agents/*.md with
	// definitions discovered from the configured user directories. Discovered
	// definitions win by name, so users can override any built-in without
	// modifying Atenea's installation.
	agentDefs, err := agent.Catalog(cfg.AgentDirs...)
	if err != nil {
		log.Printf("atenea: could not discover the subagents: %v", err)
	}
	// The subagents' registry: the same file, search, exec and connected MCP tools,
	// narrowed by each agent's def.Tools. Rebuilding it from cfg.MCPTools on every
	// assembly makes connect and disconnect visible to new child runs, while the
	// definition remains the authority over which tools a child may use.
	childTools := []tool.Tool{
		tool.NewReadToolWithSnapshotProvider(root, cfg.Snaps), tool.NewWriteToolWithSnapshotProvider(root, cfg.Snaps),
		tool.NewEditToolWithSnapshotProvider(root, hashline.OSFilesystem{}, cfg.Snaps),
		tool.NewGlobTool(root), tool.NewGrepToolWithSnapshotProvider(root, cfg.Snaps),
		tool.NewBashTool(root),
	}
	childTools = append(childTools, cfg.MCPTools...)
	childRegistry := tool.NewRegistry(tool.NewOutputStore(cfg.OutputLimit), childTools...)
	// A def that names a tool the child registry does not have used to lose it
	// silently: the name never became a permission and the subagent ran with fewer
	// tools than its author wrote down. Now it is reported, once, against the
	// registry that actually bounds a subagent.
	for _, problem := range agent.Validate(agentDefs, childRegistry) {
		log.Printf("atenea: %v", problem)
	}
	// The task tool starts child subagents. Its own nextID (thread-safe) because
	// several subagents can run in parallel (internal concurrency cap).
	taskTool := subagent.NewTaskTool(agentDefs, cfg.Provider, childRegistry, NewIDGen())
	// Surfacing a subagent's permission in the UI: decorate the child runner's store
	// with ChildPermissionStore over the SAME bus, so the child's permission events
	// (Tool.Permission.Requested and its resolution) are published on the PARENT's
	// channel (the one the frontend already listens to), keeping the child's SessionID
	// in the payload. The frontend shows Approve/Deny and resolves with (childID,
	// callID) through the shared gate, which already keys by that pair. Without this the
	// child blocks in gate.Ask but the UI never sees the request.
	taskTool.SetStoreDecorator(func(parentSessionID string, inner session.Store) session.Store {
		return event.NewChildPermissionStore(parentSessionID, inner, cfg.Bus)
	})
	// present_plan is registered so the runner can execute it, but it does not enter
	// the normal permission set: cfg.PlanMode claims it, and only plan mode announces
	// it (SetPlanMode below).
	registryTools := []tool.Tool{
		tool.NewReadToolWithSnapshotProvider(root, cfg.Snaps), tool.NewWriteToolWithSnapshotProvider(root, cfg.Snaps),
		tool.NewEditToolWithSnapshotProvider(root, hashline.OSFilesystem{}, cfg.Snaps),
		tool.NewGlobTool(root), tool.NewGrepToolWithSnapshotProvider(root, cfg.Snaps),
		tool.NewBashTool(root), tool.NewPresentPlanTool(root), tool.NewSkillTool(skills), taskTool,
		tool.NewWebFetchTool(cfg.Provider), tool.TodoWriteTool{},
	}
	registryTools = append(registryTools, cfg.MCPTools...)
	registry := tool.NewRegistry(tool.NewOutputStore(cfg.OutputLimit), registryTools...)
	// The effective ask-before-run policy: cfg.Policy classifies over this
	// assembly's registry — by default from what each tool declares about itself
	// (permission.EffectsPolicy) rather than from a list of names kept here — with
	// the user's session grants layered on top when the caller keeps a store for
	// them. It is built here, after the registry, because a classification is only
	// as good as the catalog it reads and that catalog changes with every rewire —
	// an MCP server that just connected contributes tools this policy has to be
	// able to see.
	policy := permission.NewRulePolicy(cfg.Policy(registry), cfg.PersistentGrants, registry)
	policy = permission.NewGrantedPolicy(policy, cfg.Grants, registry)
	policy = permission.NewAutoAcceptPolicy(policy, cfg.AutoAccept, registry)
	home, _ := os.UserHomeDir()
	policy = permission.NewYoloPolicy(policy, cfg.Yolo, root, home)
	// Security: propagate ask-before-run to the child runner with the SAME gate
	// and the SAME policy as the main chat. Without this a "general" subagent
	// would run gated tools (bash, write, edit, web_fetch) without the
	// confirmation the main chat enforces, evading the gate. The policy reads the
	// main registry, whose tool names are a superset of the child's, so parent and
	// child classify the same call identically. The gate is keyed by (sessionID,
	// callID): the child's sessionID is its childID, so the child's permission
	// resolution finds it. Session grants are keyed by session too, so a subagent
	// does not inherit the chat's grants: it asks on its own behalf, and a grant
	// answered on its prompt covers only that child.
	taskTool.SetPermissionGate(cfg.Gate, policy)
	permissions := registry.Permissions()
	// What normal mode announces is what is registered, minus the tools plan mode
	// claims for itself. Every ordinary and dynamic MCP tool arrives from the
	// registry above; this is the one explicit exclusion, and it is the same field
	// that puts those tools in plan mode, so the two cannot disagree.
	for _, name := range cfg.PlanMode.Exclusive {
		delete(permissions, name)
	}
	r := runner.NewRunner(cfg.Store, cfg.Inbox, cfg.Provider, registry,
		permissions,
		cfg.NextID)
	r.SetCompactor(runner.NewContextCompactor(cfg.Store, cfg.Provider))
	r.SetSystemPrompt(systemPromptBuilder(root, skillsBlock, cfg.LocalPrompt))
	r.SetPermissionGate(cfg.Gate, policy)
	r.SetReasoning(cfg.Reasoning)
	// Plan mode: read-only investigation plus present_plan (no write/edit/bash). The
	// mode hook decides per session; SetMode/SetPlanMode only take effect when
	// cfg.Mode reports ModePlan (nil = always normal, the runner's default).
	r.SetMode(cfg.Mode)
	r.SetPlanMode(planSystemPromptBuilder(root, skillsBlock, cfg.LocalPrompt),
		cfg.PlanMode.permissions())

	return Built{Runner: r, Glob: glob, Commands: commands, Tools: registry, Policy: policy}
}

// promptSetup anchors at root the shared preparation of the system prompt: it
// detects whether root is a git repo and loads the repo instructions
// (AGENTS.md/CLAUDE.md) exactly once, then returns a factory of Env per call (the
// date is computed on every call so it does not go stale in a long session) plus
// the loaded instructions. The normal builder and the plan-mode one reuse it; they
// differ only in which pure prompt function they call (prompt.Build vs
// prompt.BuildPlan).
func promptSetup(root string) (env func() prompt.Env, instructions string) {
	_, gitErr := os.Stat(filepath.Join(root, ".git"))
	isGit := gitErr == nil
	instructions, err := prompt.LoadInstructions(root, root)
	if err != nil {
		log.Printf("atenea: could not load the repo instructions: %v", err)
	}
	env = func() prompt.Env {
		return prompt.Env{
			WorkingDir:   root,
			WorktreeRoot: root,
			IsGitRepo:    isGit,
			Platform:     goruntime.GOOS,
			Date:         time.Now().Format("2006-01-02"),
		}
	}
	return env, instructions
}

// systemPromptBuilder builds the normal-mode system prompt builder anchored at
// root: per turn it composes the base prompt + the <env> block with the day's date
// + the skills block (discovered once during the assembly and passed in already
// formatted), over the shared promptSetup. The base comes from the local prompt
// (function-calling protocol) when local reports so on that turn (LM Studio,
// Ollama); otherwise it is chosen by model family.
func systemPromptBuilder(root, skills string, local func() bool) func(model string) string {
	env, instructions := promptSetup(root)
	return func(model string) string {
		if local != nil && local() {
			return prompt.BuildLocal(env(), instructions, skills)
		}
		return prompt.Build(model, env(), instructions, skills)
	}
}

// planSystemPromptBuilder builds the plan-mode system prompt builder: the same
// shape as systemPromptBuilder, but adding the plan-mode contract (present_plan)
// on top of the base prompt, through BuildLocalPlan with local or BuildPlan
// without it.
func planSystemPromptBuilder(root, skills string, local func() bool) func(model string) string {
	env, instructions := promptSetup(root)
	return func(model string) string {
		if local != nil && local() {
			return prompt.BuildLocalPlan(env(), instructions, skills)
		}
		return prompt.BuildPlan(model, env(), instructions, skills)
	}
}

// NewIDGen returns a real generator of assistantMessageIDs: an atomic counter
// with a prefix. The counter starts over with the process, so an id is unique
// within a run rather than across runs. It is injected rather than fixed here so
// tests can stay deterministic.
func NewIDGen() func() string {
	var n uint64
	return func() string {
		return "msg-" + strconv.FormatUint(atomic.AddUint64(&n, 1), 10)
	}
}

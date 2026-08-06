package runner

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool"
)

// Runner assembles a turn: it reads Store history, materializes the Registry
// with the agent's permissions, calls the Provider, and publishes events.
type Runner struct {
	store     session.Store
	inbox     session.Inbox
	provider  llm.Provider
	registry  *tool.Registry
	perms     tool.Permissions
	nextID    func() string
	compactor Compactor // optional; nil disables compaction

	// system builds the turn baseline prompt from the epoch's model. nil (default)
	// = no system prompt. SetSystemPrompt wires it from the real assembly
	// (app.go); tests inject it directly or via the setter. It receives the model
	// so internal/session/prompt picks the base prompt by family.
	system func(model string) string

	// mode looks up the session's Mode per turn. nil (default) => always
	// ModeNormal: behavior is identical to today. In ModePlan the runner builds the
	// Request with planSystem/planPerms instead of system/perms.
	mode func(sessionID string) session.Mode

	// planSystem and planPerms are the plan-mode counterparts of system/perms,
	// used when mode reports ModePlan. planSystem nil => fall back to system;
	// planPerms nil => fall back to perms. SetPlanMode wires them from app.go;
	// tests inject them via the setter.
	planSystem func(model string) string
	planPerms  tool.Permissions

	// gate resolves user decisions after the runner has durably published the
	// permission request. Classification belongs exclusively to the registry.
	gate permission.Gate

	// logf writes tool failures to stderr for development visibility. It defaults
	// to log.Printf; tests replace it to capture output without touching stderr.
	logf      func(format string, args ...any)
	reasoning func() *llm.ReasoningPreference
	preview   func(tool.PreviewEvent)

	// transientRetryDelays paces the automatic retries of a turn whose stream
	// died on a transient transport interruption; its length is the retry
	// budget, and exhausting it surfaces the failure as Step.Failed. Empty (the
	// zero value) disables the retries. Tests shorten it to keep the suite fast.
	transientRetryDelays []time.Duration
}

// defaultTransientRetryDelays gives a failing endpoint three more chances over
// about seven seconds before its turn fails for the user.
var defaultTransientRetryDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// Compactor decides whether a Request exceeds the model context and compacts
// durable session history so the next attempt fits. A nil Compactor disables it.
type Compactor interface {
	// NeedsCompaction reports whether req must be compacted before calling the provider.
	NeedsCompaction(req llm.Request) bool
	// Compact reduces durable session history so the next request fits. It must
	// make progress: NeedsCompaction must eventually become false.
	Compact(ctx context.Context, sessionID string) error
}

func (r *Runner) SetCompactor(compactor Compactor) {
	r.compactor = compactor
}

func (r *Runner) CompactNow(ctx context.Context, sessionID string) error {
	if r.compactor == nil {
		return errors.New("context compaction is unavailable")
	}
	return r.compactor.Compact(ctx, sessionID)
}

// NewRunner constructs a Runner. nextID is injected to keep tests deterministic
// without introducing a UUID or clock dependency.
func NewRunner(store session.Store, inbox session.Inbox, provider llm.Provider,
	registry *tool.Registry, perms tool.Permissions, nextID func() string) *Runner {
	return &Runner{
		store: store, inbox: inbox, provider: provider,
		registry: registry, perms: perms, nextID: nextID,
		logf:                 log.Printf,
		transientRetryDelays: defaultTransientRetryDelays,
	}
}

// SetSystemPrompt injects the turn system prompt builder. It receives the epoch's
// model and returns the baseline prompt that travels in Request.System. nil
// (default) = no system prompt. This is the exported entry point for the real
// assembly (app.go, in package main, cannot touch the unexported field).
func (r *Runner) SetSystemPrompt(build func(model string) string) {
	r.system = build
}

// SetReasoning wires the per-turn reasoning preference. A nil callback keeps
// provider defaults, and the callback is evaluated when each request is built.
func (r *Runner) SetReasoning(reasoning func() *llm.ReasoningPreference) {
	r.reasoning = reasoning
}

// SetPreviewSink receives ephemeral edit projections. The sink must return
// promptly; nil disables previews without changing durable stream behavior.
func (r *Runner) SetPreviewSink(sink func(tool.PreviewEvent)) {
	r.preview = sink
}

// SetPermissionGate configures ask-before-run on the tool registry: policy
// classifies each call and gate resolves calls that ask. The registry owns and
// snapshots this execution policy when a turn materializes its tools; the
// runner deliberately keeps no second copy.
func (r *Runner) SetPermissionGate(gate permission.Gate, policy permission.Policy) {
	r.gate = gate
	r.registry.SetPermissionGate(gate, policy)
}

// SetMode injects the per-session Mode lookup. It receives the session id and
// returns its Mode; the runner consults it each turn to pick the normal or
// plan-mode system prompt and permissions. nil (default) = always ModeNormal,
// so behavior is identical to today. Exported entry point for app.go (package
// main); tests inject the field via this setter.
func (r *Runner) SetMode(mode func(sessionID string) session.Mode) {
	r.mode = mode
}

// SetPlanMode wires the plan-mode turn baseline: system builds the plan-mode
// system prompt and perms is the plan-mode permission set (read-only +
// present_plan). They take effect only when SetMode reports ModePlan. A nil
// system falls back to the normal SetSystemPrompt builder; nil perms fall back
// to the normal permissions. Exported entry point for app.go (package main);
// tests inject the fields via this setter.
func (r *Runner) SetPlanMode(system func(model string) string, perms tool.Permissions) {
	r.planSystem = system
	r.planPerms = perms
}

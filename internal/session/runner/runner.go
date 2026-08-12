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

	// system builds the turn baseline prompt from the epoch's model. nil means no
	// system prompt. It receives the model so the prompt module can select the
	// base prompt by family.
	system func(model string) string

	// mode selects the turn surface by session. Normal preserves the existing
	// agent; plan and RAH use their own prompt/permission pairs.
	mode func(sessionID string) session.Mode

	planSystem func(model string) string
	planPerms  tool.Permissions
	rahSystem  func(model string) string
	rahPerms   tool.Permissions

	lessonSection func(sessionID, latestPrompt string) string

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
	// NeedsCompaction reports whether req must be compacted before calling the
	// provider. observed carries the estimated/reported token pair of the last
	// completed turn, which anchors the decision on the provider's own count
	// instead of the estimator's bias; its zero value means no turn has completed
	// yet and the estimate is all there is to go on.
	NeedsCompaction(req llm.Request, observed llm.TokenObservation) bool
	// Compact reduces durable session history so the next request fits. It must
	// make progress: NeedsCompaction must eventually become false. The reason is
	// recorded on the checkpoint: preventive when the estimate crossed the
	// threshold, overflow when the provider already rejected the turn.
	Compact(ctx context.Context, sessionID string, reason session.CompactionReason) error
}

// Config is the complete startup configuration of a Runner. New applies it
// atomically, so callers never observe or accidentally run a partially wired
// runner. Optional behavior is represented by nil fields.
type Config struct {
	Store       session.Store
	Inbox       session.Inbox
	Provider    llm.Provider
	Registry    *tool.Registry
	Permissions tool.Permissions
	NextID      func() string

	Compactor     Compactor
	System        func(model string) string
	Reasoning     func() *llm.ReasoningPreference
	Preview       func(tool.PreviewEvent)
	Gate          permission.Gate
	Policy        permission.Policy
	Mode          func(sessionID string) session.Mode
	PlanSystem    func(model string) string
	PlanPerms     tool.Permissions
	RAHSystem     func(model string) string
	RAHPerms      tool.Permissions
	LessonSection func(sessionID, latestPrompt string) string
}

func (r *Runner) setCompactor(compactor Compactor) {
	r.compactor = compactor
}

// CompactNow compacts on demand, which is always ahead of a provider rejection:
// the user asked before the window ran out.
func (r *Runner) CompactNow(ctx context.Context, sessionID string) error {
	if r.compactor == nil {
		return errors.New("context compaction is unavailable")
	}
	return r.compactor.Compact(ctx, sessionID, session.CompactionPreventive)
}

// New constructs a fully configured Runner.
func New(cfg Config) *Runner {
	r := &Runner{
		store: cfg.Store, inbox: cfg.Inbox, provider: cfg.Provider,
		registry: cfg.Registry, perms: cfg.Permissions, nextID: cfg.NextID,
		compactor: cfg.Compactor, system: cfg.System, reasoning: cfg.Reasoning,
		preview: cfg.Preview, gate: cfg.Gate, mode: cfg.Mode,
		planSystem: cfg.PlanSystem, planPerms: cfg.PlanPerms,
		rahSystem: cfg.RAHSystem, rahPerms: cfg.RAHPerms,
		lessonSection:        cfg.LessonSection,
		logf:                 log.Printf,
		transientRetryDelays: defaultTransientRetryDelays,
	}
	if cfg.Registry != nil {
		cfg.Registry.SetPermissionGate(cfg.Gate, cfg.Policy)
	}
	return r
}

func (r *Runner) setSystemPrompt(build func(model string) string) {
	r.system = build
}

func (r *Runner) setReasoning(reasoning func() *llm.ReasoningPreference) {
	r.reasoning = reasoning
}

func (r *Runner) setPreviewSink(sink func(tool.PreviewEvent)) {
	r.preview = sink
}

func (r *Runner) setPermissionGate(gate permission.Gate, policy permission.Policy) {
	r.gate = gate
	r.registry.SetPermissionGate(gate, policy)
}

func (r *Runner) setMode(mode func(sessionID string) session.Mode) {
	r.mode = mode
}

func (r *Runner) setPlanMode(system func(model string) string, perms tool.Permissions) {
	r.planSystem = system
	r.planPerms = perms
}

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/K3N4Y/atenea/internal/agent"
	"github.com/K3N4Y/atenea/internal/checkpoint"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/providerconfig"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

type promptHistoryStore struct {
	session.Store
	failComposerPrompt bool
	blockedSession     string
}

type sessionModeFailingStore struct {
	session.Store
	err error
}

type blockingSessionsStore struct {
	session.Store
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

type resumeBlockingProvider struct {
	started chan struct{}
}

// releasableProvider blocks its first turn until release closes (or the run is
// canceled) and then streams a short text answer: a deterministic stand-in for
// a run that is still streaming while the user does something else.
type releasableProvider struct {
	started chan struct{}
	release chan struct{}
}

func (p *releasableProvider) Stream(ctx context.Context, _ llm.Request) (<-chan llm.Event, error) {
	out := make(chan llm.Event)
	go func() {
		defer close(out)
		close(p.started)
		select {
		case <-ctx.Done():
			return
		case <-p.release:
		}
		for _, event := range []llm.Event{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: "old conversation answer"},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		} {
			select {
			case <-ctx.Done():
				return
			case out <- event:
			}
		}
	}()
	return out, nil
}

type failingCheckpointStore struct{ err error }

type fixedCheckpointStore struct{ tree checkpoint.Tree }

func (s failingCheckpointStore) Capture(context.Context, string) (checkpoint.Tree, error) {
	return "", s.err
}

func (s failingCheckpointStore) Restore(context.Context, string, checkpoint.Tree) error {
	return nil
}

func (s fixedCheckpointStore) Capture(context.Context, string) (checkpoint.Tree, error) {
	return s.tree, nil
}

func (s fixedCheckpointStore) Restore(context.Context, string, checkpoint.Tree) error {
	return nil
}

func (s *sessionModeFailingStore) AppendEvent(ctx context.Context, sessionID string, event session.SessionEvent) (session.Seq, error) {
	if event.Kind == session.KindSessionMode {
		return 0, s.err
	}
	return s.Store.AppendEvent(ctx, sessionID, event)
}

func (s *blockingSessionsStore) Sessions(ctx context.Context) ([]session.SessionSummary, error) {
	s.once.Do(func() { close(s.entered) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.release:
		return s.Store.Sessions(ctx)
	}
}

func (s *promptHistoryStore) AppendEvent(ctx context.Context, sessionID string, ev session.SessionEvent) (session.Seq, error) {
	if s.failComposerPrompt && ev.Kind == session.KindComposerPrompt {
		return 0, errors.New("composer history unavailable")
	}
	return s.Store.AppendEvent(ctx, sessionID, ev)
}

func (s *promptHistoryStore) Events(ctx context.Context, sessionID string, sinceSeq session.Seq) ([]session.SessionEvent, error) {
	if sessionID == s.blockedSession {
		return nil, errors.New("older session should not be read")
	}
	return s.Store.Events(ctx, sessionID, sinceSeq)
}

func (p *resumeBlockingProvider) Stream(ctx context.Context, _ llm.Request) (<-chan llm.Event, error) {
	out := make(chan llm.Event)
	go func() {
		defer close(out)
		close(p.started)
		<-ctx.Done()
	}()
	return out, nil
}

// turnProvider implements llm.Provider with a script PER TURN: the ith call to Stream plays the ith script. Contrast with llm.FakeProvider, which repeats the same script in each Stream (infinite loop if the script asks for tools). If the scripts run out, issue a StepEnded-only turn so that the run ends cleanly. Protected with mutex: the runner calls Stream from its own goroutine.
type turnProvider struct {
	mu    sync.Mutex
	turns [][]llm.Event
	next  int
	// toolNames records, for each call to Stream, the names of the tools announced in the Request: the observable evidence of the shift mode (plan-mode announces present_plan and hides bash/write).
	toolNames [][]string
	// messages records, for each call to Stream, the projected history that the runner sent to the provider: the observable evidence of the order in which events materialized as Messages.
	messages [][]llm.Message
	// delayStepEnded, if > 0, sleeps that period between a ToolCall of the script and the StepEnded that follows it: deterministic mirror of the last SSE chunk that arrives late over the network while the tool is already settling locally.
	delayStepEnded time.Duration
}

type blockingAfterToolProvider struct {
	started  chan struct{}
	canceled chan struct{}
	mu       sync.Mutex
	next     int
}

type compactQueueProvider struct {
	started chan struct{}
	release chan struct{}

	mu       sync.Mutex
	requests []llm.Request
}

type blockingSummaryProvider struct {
	started chan struct{}
	release chan struct{}

	mu       sync.Mutex
	requests []llm.Request
}

type replacementRunCompactionProvider struct {
	mu      sync.Mutex
	next    int
	started [3]chan struct{}
}

type delayedCancellationProvider struct {
	mu            sync.Mutex
	next          int
	firstStarted  chan struct{}
	cancelSeen    chan struct{}
	releaseFirst  chan struct{}
	secondStarted chan struct{}
}

func newDelayedCancellationProvider() *delayedCancellationProvider {
	return &delayedCancellationProvider{
		firstStarted:  make(chan struct{}),
		cancelSeen:    make(chan struct{}),
		releaseFirst:  make(chan struct{}),
		secondStarted: make(chan struct{}),
	}
}

func (p *delayedCancellationProvider) Stream(ctx context.Context, _ llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	call := p.next
	p.next++
	p.mu.Unlock()
	out := make(chan llm.Event)
	go func() {
		defer close(out)
		if call == 0 {
			close(p.firstStarted)
			<-ctx.Done()
			close(p.cancelSeen)
			<-p.releaseFirst
			return
		}
		close(p.secondStarted)
		select {
		case <-ctx.Done():
		case out <- llm.Event{Kind: llm.StepEnded}:
		}
	}()
	return out, nil
}

func newReplacementRunCompactionProvider() *replacementRunCompactionProvider {
	return &replacementRunCompactionProvider{started: [3]chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}}
}

func (p *replacementRunCompactionProvider) Stream(ctx context.Context, _ llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	call := p.next
	p.next++
	p.mu.Unlock()
	out := make(chan llm.Event)
	go func() {
		defer close(out)
		close(p.started[call])
		if call < 2 {
			<-ctx.Done()
			return
		}
		out <- llm.Event{Kind: llm.TextDelta, Text: `{"current_goal":"continue","constraints_and_instructions":[],"decisions":[],"completed_work":[],"files_and_changes":[],"relevant_tool_results":[],"failures_and_attempts":[],"pending_and_next_step":[],"facts_not_to_reinterpret":[]}`}
		out <- llm.Event{Kind: llm.StepEnded}
	}()
	return out, nil
}

func newBlockingSummaryProvider() *blockingSummaryProvider {
	return &blockingSummaryProvider{started: make(chan struct{}), release: make(chan struct{})}
}

func (p *blockingSummaryProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	call := len(p.requests)
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	out := make(chan llm.Event)
	go func() {
		defer close(out)
		if call == 0 {
			close(p.started)
			select {
			case <-ctx.Done():
				return
			case <-p.release:
			}
			out <- llm.Event{Kind: llm.TextDelta, Text: `{"current_goal":"continue","constraints_and_instructions":[],"decisions":[],"completed_work":[],"files_and_changes":[],"relevant_tool_results":[],"failures_and_attempts":[],"pending_and_next_step":[],"facts_not_to_reinterpret":[]}`}
		}
		out <- llm.Event{Kind: llm.StepEnded}
	}()
	return out, nil
}

func (p *blockingSummaryProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

func newCompactQueueProvider() *compactQueueProvider {
	return &compactQueueProvider{started: make(chan struct{}), release: make(chan struct{})}
}

func (p *compactQueueProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	call := len(p.requests)
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	out := make(chan llm.Event)
	go func() {
		defer close(out)
		if call == 0 {
			close(p.started)
			select {
			case <-ctx.Done():
				return
			case <-p.release:
			}
			out <- llm.Event{Kind: llm.StepEnded}
			return
		}
		out <- llm.Event{Kind: llm.TextDelta, Text: `{"current_goal":"continue","constraints_and_instructions":[],"decisions":[],"completed_work":[],"files_and_changes":[],"relevant_tool_results":[],"failures_and_attempts":[],"pending_and_next_step":[],"facts_not_to_reinterpret":[]}`}
		out <- llm.Event{Kind: llm.StepEnded}
	}()
	return out, nil
}

func (p *compactQueueProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

func (p *blockingAfterToolProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	turn := p.next
	p.next++
	p.mu.Unlock()
	out := make(chan llm.Event)
	go func() {
		defer close(out)
		if turn == 0 {
			for _, event := range []llm.Event{{Kind: llm.StepStarted}, {Kind: llm.ToolCall, CallID: "write-1", ToolName: "write", Input: json.RawMessage(`{"path":"created.txt","content":"created\n"}`)}, {Kind: llm.StepEnded}} {
				select {
				case <-ctx.Done():
					return
				case out <- event:
				}
			}
			return
		}
		close(p.started)
		<-ctx.Done()
		close(p.canceled)
	}()
	return out, nil
}

var _ llm.Provider = (*turnProvider)(nil)

func newTurnProvider(turns ...[]llm.Event) *turnProvider {
	return &turnProvider{turns: turns}
}

func (p *turnProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	names := make([]string, len(req.Tools))
	for i, def := range req.Tools {
		names[i] = def.Name
	}
	p.toolNames = append(p.toolNames, names)
	p.messages = append(p.messages, append([]llm.Message(nil), req.Messages...))
	script := []llm.Event{{Kind: llm.StepEnded}}
	if p.next < len(p.turns) {
		script = p.turns[p.next]
		p.next++
	}
	delay := p.delayStepEnded
	p.mu.Unlock()

	out := make(chan llm.Event)
	go func() {
		defer close(out)
		sawToolCall := false
		for _, ev := range script {
			if ev.Kind == llm.StepEnded && sawToolCall && delay > 0 {
				time.Sleep(delay)
			}
			select {
			case <-ctx.Done():
				return
			case out <- ev:
			}
			if ev.Kind == llm.ToolCall {
				sawToolCall = true
			}
		}
	}()
	return out, nil
}

// requestedTools returns a copy of the tool names announced in each call to Stream, in order of arrival. With mutex: the runner calls Stream from its own goroutine.
func (p *turnProvider) requestedTools() [][]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]string(nil), p.toolNames...)
}

// RequestedMessages returns a copy of the projected history sent on each call to Stream, in order of arrival. With mutex: the runner calls Stream from its own goroutine.
func (p *turnProvider) requestedMessages() [][]llm.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]llm.Message(nil), p.messages...)
}

// nextMsg takes the next message from the engine channel, with a generous timeout so as not to be flaky. The test fails if the channel closes or expires.
func nextMsg(t *testing.T, ch <-chan tea.Msg, timeout time.Duration) tea.Msg {
	t.Helper()
	select {
	case <-time.After(timeout):
		t.Fatalf("timeout after %v waiting for the next engine message", timeout)
		return nil
	case msg, ok := <-ch:
		if !ok {
			t.Fatal("engine channel closed too early")
		}
		return msg
	}
}

// resolveUntilStopped delivers the permission decision via the engine's public API, retrying in the background until the test stops it. Retry eliminates a real race: the runner publishes Tool.Permission.Requested BEFORE gate.Ask registers the request, so a single delivery could preempt registration and be lost (the gate discards decisions without pending Ask). Retry is harmless: effective delivery removes the request from the gate and subsequent retries are no-op.
func resolveUntilStopped(e *Engine, sessionID, callID string, verdict permission.Verdict) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			e.ResolvePermission(sessionID, callID, verdict)
			select {
			case <-done:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	return func() { close(done); wg.Wait() }
}

// approveAllPermissions returns a collectUntilRunDone hook that approves every
// ask-before-run request the run emits. The fixed policy gates bash, write,
// edit and web_fetch; the tests using this hook exercise undo/checkpoint
// semantics, not the gate, so the user's approval is assumed.
func approveAllPermissions(t *testing.T, engine *Engine) func(session.SessionEvent) {
	t.Helper()
	return func(ev session.SessionEvent) {
		if ev.Kind == session.KindToolPermissionRequested {
			t.Cleanup(resolveUntilStopped(engine, ev.SessionID, ev.CallID, permission.AllowedOnce))
		}
	}
}

func appendSessionEvent(t *testing.T, store session.Store, sessionID string, event session.SessionEvent) {
	t.Helper()
	if _, err := store.AppendEvent(context.Background(), sessionID, event); err != nil {
		t.Fatal(err)
	}
}

func TestEngine_NewSessionIDReservesFreshTUISessions(t *testing.T) {
	root := t.TempDir()
	store := session.NewMemoryStore()
	appendSessionEvent(t, store, "tui-older", session.SessionEvent{Kind: session.KindSessionCwd, Text: root})

	engine := New(Config{Root: root, Provider: llm.NewFakeProvider(), Store: store})
	sessionID := engine.NewSessionID()
	if !strings.HasPrefix(sessionID, "tui-") || sessionID == "tui-older" {
		t.Fatalf("NewSessionID = %q, want a fresh tui- session", sessionID)
	}
	if _, err := store.Events(context.Background(), sessionID, 0); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("Events(%q) error = %v, want ErrSessionNotFound (no durable session until the first prompt)", sessionID, err)
	}
}

func TestEngine_ListResumeSessionsFiltersWorkspaceAndPreservesStoreOrder(t *testing.T) {
	root := t.TempDir()
	store := session.NewMemoryStore()
	appendSessionEvent(t, store, "tui-current", session.SessionEvent{Kind: session.KindSessionCwd, Text: root})
	appendSessionEvent(t, store, "app-newest", session.SessionEvent{Kind: session.KindSessionCwd, Text: root})
	appendSessionEvent(t, store, "tui-other-root", session.SessionEvent{Kind: session.KindSessionCwd, Text: t.TempDir()})
	appendSessionEvent(t, store, "tui-newer", session.SessionEvent{Kind: session.KindSessionCwd, Text: filepath.Join(root, ".")})

	all, err := store.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []session.SessionSummary{all[0], all[3]}
	engine := New(Config{Root: root, Provider: llm.NewFakeProvider(), Store: store})
	got, err := engine.ListResumeSessions("tui-current")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ListResumeSessions = %+v, want store-ordered summaries %+v", got, want)
	}
}

func TestEngine_ListResumeSessionsAcceptsSymlinkToWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	aliasParent := t.TempDir()
	alias := filepath.Join(aliasParent, "workspace-link")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	store := session.NewMemoryStore()
	appendSessionEvent(t, store, "tui-linked", session.SessionEvent{Kind: session.KindSessionCwd, Text: alias})

	engine := New(Config{Root: root, Provider: llm.NewFakeProvider(), Store: store})
	got, err := engine.ListResumeSessions("tui-current")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "tui-linked" {
		t.Fatalf("ListResumeSessions = %+v, want symlinked workspace session", got)
	}
}

func TestEngine_ListResumeSessionsUsesKernelSemanticsForSymlinkFollowedByDotDot(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	childLink := filepath.Join(t.TempDir(), "child-link")
	if err := os.Symlink(child, childLink); err != nil {
		t.Fatal(err)
	}
	store := session.NewMemoryStore()
	appendSessionEvent(t, store, "tui-linked-parent", session.SessionEvent{
		Kind: session.KindSessionCwd,
		Text: childLink + string(os.PathSeparator) + "..",
	})

	engine := New(Config{Root: root, Provider: llm.NewFakeProvider(), Store: store})
	got, err := engine.ListResumeSessions("tui-current")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "tui-linked-parent" {
		t.Fatalf("ListResumeSessions = %+v, want kernel-resolved symlink/.. workspace", got)
	}
}

func TestEngine_ListResumeSessionsRejectsNonDirectoryRoot(t *testing.T) {
	rootFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(rootFile, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := session.NewMemoryStore()
	appendSessionEvent(t, store, "tui-file-root", session.SessionEvent{Kind: session.KindSessionCwd, Text: rootFile})

	engine := New(Config{Root: rootFile, Provider: llm.NewFakeProvider(), Store: store})
	if got, err := engine.ListResumeSessions("tui-current"); err == nil {
		t.Fatalf("ListResumeSessions = %+v, want non-directory root error", got)
	}
}

func TestEngine_ListResumeSessionsRejectsSymlinkToOtherWorkspace(t *testing.T) {
	root := t.TempDir()
	otherRoot := t.TempDir()
	alias := filepath.Join(t.TempDir(), "other-workspace-link")
	if err := os.Symlink(otherRoot, alias); err != nil {
		t.Fatal(err)
	}
	store := session.NewMemoryStore()
	appendSessionEvent(t, store, "tui-other", session.SessionEvent{Kind: session.KindSessionCwd, Text: alias})

	engine := New(Config{Root: root, Provider: llm.NewFakeProvider(), Store: store})
	got, err := engine.ListResumeSessions("tui-current")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("ListResumeSessions = %+v, want cross-workspace symlink rejected", got)
	}
}

func TestEngine_ListResumeSessionsRejectsEmptyUnresolvableAndNonDirectoryCwd(t *testing.T) {
	root := t.TempDir()
	brokenLink := filepath.Join(t.TempDir(), "broken-workspace-link")
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), brokenLink); err != nil {
		t.Fatal(err)
	}
	fileCwd := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(fileCwd, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := session.NewMemoryStore()
	appendSessionEvent(t, store, "tui-empty", session.SessionEvent{Kind: session.KindSessionCwd})
	appendSessionEvent(t, store, "tui-broken", session.SessionEvent{Kind: session.KindSessionCwd, Text: brokenLink})
	appendSessionEvent(t, store, "tui-file", session.SessionEvent{Kind: session.KindSessionCwd, Text: fileCwd})

	engine := New(Config{Root: root, Provider: llm.NewFakeProvider(), Store: store})
	got, err := engine.ListResumeSessions("tui-current")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("ListResumeSessions = %+v, want unsafe Cwd values rejected", got)
	}
}

func TestEngine_ResumeSessionByIDLoadsExactTargetAndRestoresPlanMode(t *testing.T) {
	root := t.TempDir()
	store := session.NewMemoryStore()
	appendSessionEvent(t, store, "tui-current", session.SessionEvent{Kind: session.KindSessionCwd, Text: root})
	appendSessionEvent(t, store, "tui-target", session.SessionEvent{Kind: session.KindSessionCwd, Text: root})
	appendSessionEvent(t, store, "tui-target", session.SessionEvent{Kind: session.KindTextDelta, Text: "target marker"})
	appendSessionEvent(t, store, "tui-target", session.SessionEvent{Kind: session.KindSessionMode, Text: string(session.ModePlan)})
	appendSessionEvent(t, store, "tui-other", session.SessionEvent{Kind: session.KindSessionCwd, Text: root})
	appendSessionEvent(t, store, "tui-other", session.SessionEvent{Kind: session.KindTextDelta, Text: "other marker"})

	engine := New(Config{Root: root, Provider: llm.NewFakeProvider(), Store: store})
	result, err := engine.ResumeSessionByID("tui-current", "tui-target")
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID != "tui-target" || result.Mode != session.ModePlan {
		t.Fatalf("ResumeSessionByID = %+v, want exact target in plan mode", result)
	}
	if len(result.Events) != 3 || result.Events[1].Text != "target marker" {
		t.Fatalf("ResumeSessionByID events = %+v, want target events before resume marker", result.Events)
	}
	persisted, err := store.Events(context.Background(), "tui-target", 0)
	if err != nil {
		t.Fatal(err)
	}
	last := persisted[len(persisted)-1]
	if len(persisted) != 4 || last.Kind != session.KindSessionMode || last.Text != string(session.ModePlan) {
		t.Fatalf("persisted target events = %+v, want current plan mode appended", persisted)
	}
}

func TestEngine_ResumeSessionByIDRestoresOnlyTargetComposerHistory(t *testing.T) {
	root := t.TempDir()
	store := session.NewMemoryStore()
	appendSessionEvent(t, store, "tui-current", session.SessionEvent{Kind: session.KindSessionCwd, Text: root})
	appendSessionEvent(t, store, "tui-current", session.SessionEvent{Kind: session.KindComposerPrompt, Text: "current prompt"})
	appendSessionEvent(t, store, "tui-target", session.SessionEvent{Kind: session.KindSessionCwd, Text: root})
	appendSessionEvent(t, store, "tui-target", session.SessionEvent{Kind: session.KindComposerPrompt, Text: "target first"})
	appendSessionEvent(t, store, "tui-target", session.SessionEvent{Kind: session.KindComposerPrompt, Text: "target latest"})
	appendSessionEvent(t, store, "tui-other", session.SessionEvent{Kind: session.KindSessionCwd, Text: root})
	appendSessionEvent(t, store, "tui-other", session.SessionEvent{Kind: session.KindComposerPrompt, Text: "other prompt"})

	engine := New(Config{Root: root, Provider: llm.NewFakeProvider(), Store: store})
	result, err := engine.ResumeSessionByID("tui-current", "tui-target")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"target first", "target latest"}
	if !slices.Equal(result.History, want) {
		t.Fatalf("ResumeSessionByID history = %q, want target-only %q", result.History, want)
	}
}

func TestEngine_ResumeSessionByIDFallsBackToCappedTargetUserHistory(t *testing.T) {
	root := t.TempDir()
	store := session.NewMemoryStore()
	appendSessionEvent(t, store, "tui-current", session.SessionEvent{Kind: session.KindSessionCwd, Text: root})
	appendSessionEvent(t, store, "tui-target", session.SessionEvent{Kind: session.KindSessionCwd, Text: root})
	appendSessionEvent(t, store, "tui-target", session.SessionEvent{Message: &session.Message{ID: "internal", Role: session.RoleUser, Text: agent.AcceptPlanPrompt}})
	for i := 1; i <= HistoryLimit+2; i++ {
		appendSessionEvent(t, store, "tui-target", session.SessionEvent{Message: &session.Message{
			ID: "u-" + strconv.Itoa(i), Role: session.RoleUser, Text: "legacy-" + strconv.Itoa(i),
		}})
	}

	engine := New(Config{Root: root, Provider: llm.NewFakeProvider(), Store: store})
	result, err := engine.ResumeSessionByID("tui-current", "tui-target")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.History) != HistoryLimit || result.History[0] != "legacy-3" || result.History[len(result.History)-1] != "legacy-102" {
		t.Fatalf("ResumeSessionByID fallback history = [%q ... %q] (%d), want capped target legacy prompts", result.History[0], result.History[len(result.History)-1], len(result.History))
	}
}

func TestEngine_ResumeSessionByIDRestoresMixedLegacyAndComposerHistoryWithoutDuplicates(t *testing.T) {
	root := t.TempDir()
	store := session.NewMemoryStore()
	appendSessionEvent(t, store, "tui-current", session.SessionEvent{Kind: session.KindSessionCwd, Text: root})
	appendSessionEvent(t, store, "tui-target", session.SessionEvent{Kind: session.KindSessionCwd, Text: root})
	appendSessionEvent(t, store, "tui-target", session.SessionEvent{Message: &session.Message{ID: "legacy-1", Role: session.RoleUser, Text: "legacy first"}})
	appendSessionEvent(t, store, "tui-target", session.SessionEvent{Message: &session.Message{ID: "accept", Role: session.RoleUser, Text: agent.AcceptPlanPrompt}})
	appendSessionEvent(t, store, "tui-target", session.SessionEvent{Message: &session.Message{ID: "legacy-2", Role: session.RoleUser, Text: "legacy second"}})
	appendSessionEvent(t, store, "tui-target", session.SessionEvent{Kind: session.KindComposerPrompt, Text: "modern first"})
	appendSessionEvent(t, store, "tui-target", session.SessionEvent{Message: &session.Message{ID: "modern-user-1", Role: session.RoleUser, Text: "modern first"}})
	appendSessionEvent(t, store, "tui-target", session.SessionEvent{Kind: session.KindComposerPrompt, Text: "modern second"})
	appendSessionEvent(t, store, "tui-target", session.SessionEvent{Message: &session.Message{ID: "modern-user-2", Role: session.RoleUser, Text: "modern second"}})

	engine := New(Config{Root: root, Provider: llm.NewFakeProvider(), Store: store})
	result, err := engine.ResumeSessionByID("tui-current", "tui-target")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"legacy first", "legacy second", "modern first", "modern second"}
	if !slices.Equal(result.History, want) {
		t.Fatalf("ResumeSessionByID mixed history = %q, want %q", result.History, want)
	}
}

func TestResumeHistory_PreservesUserPromptWhenLaterComposerMarkerIsMissing(t *testing.T) {
	events := []session.SessionEvent{
		{Kind: session.KindComposerPrompt, Text: "marked prompt"},
		{Message: &session.Message{ID: "marked-user", Role: session.RoleUser, Text: "marked prompt"}},
		{Message: &session.Message{ID: "missing-marker-user", Role: session.RoleUser, Text: "marker write failed prompt"}},
	}

	want := []string{"marked prompt", "marker write failed prompt"}
	if got := resumeHistory(events); !slices.Equal(got, want) {
		t.Fatalf("resumeHistory = %q, want %q", got, want)
	}
}

func TestResumeHistory_ConsumesRepeatedIdenticalMarkersByCount(t *testing.T) {
	events := []session.SessionEvent{
		{Kind: session.KindComposerPrompt, Text: "same prompt"},
		{Kind: session.KindComposerPrompt, Text: "same prompt"},
		{Message: &session.Message{ID: "marked-user-1", Role: session.RoleUser, Text: "same prompt"}},
		{Message: &session.Message{ID: "marked-user-2", Role: session.RoleUser, Text: "same prompt"}},
		{Message: &session.Message{ID: "missing-marker-user", Role: session.RoleUser, Text: "same prompt"}},
	}

	want := []string{"same prompt", "same prompt", "same prompt"}
	if got := resumeHistory(events); !slices.Equal(got, want) {
		t.Fatalf("resumeHistory = %q, want counted marker suppression %q", got, want)
	}
}

func TestResumeHistory_PreservesMarkerOrderAroundFailedMiddleMarker(t *testing.T) {
	events := []session.SessionEvent{
		{Kind: session.KindComposerPrompt, Text: "A"},
		{Kind: session.KindComposerPrompt, Text: "C"},
		{Message: &session.Message{ID: "user-a", Role: session.RoleUser, Text: "A"}},
		{Message: &session.Message{ID: "user-b", Role: session.RoleUser, Text: "B"}},
		{Message: &session.Message{ID: "user-c", Role: session.RoleUser, Text: "C"}},
	}

	want := []string{"A", "B", "C"}
	if got := resumeHistory(events); !slices.Equal(got, want) {
		t.Fatalf("resumeHistory = %q, want ordered marker reconstruction %q", got, want)
	}
}

func TestEngine_ResumeOperationsRejectActiveRun(t *testing.T) {
	root := t.TempDir()
	store := session.NewMemoryStore()
	appendSessionEvent(t, store, "tui-current", session.SessionEvent{Kind: session.KindSessionCwd, Text: root})
	appendSessionEvent(t, store, "tui-target", session.SessionEvent{Kind: session.KindSessionCwd, Text: root})
	provider := &resumeBlockingProvider{started: make(chan struct{})}
	engine := New(Config{Root: root, Provider: provider, Store: store})
	if _, err := engine.SendPrompt("tui-current", session.Prompt{Text: "keep running"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("active run did not start")
	}
	t.Cleanup(func() {
		engine.Stop("tui-current")
		_ = engine.Shutdown(context.Background())
	})

	const want = "stop the active run before resuming another session"
	if _, err := engine.ListResumeSessions("tui-current"); !errors.Is(err, ErrResumeActiveRun) || err.Error() != want {
		t.Fatalf("ListResumeSessions error = %v, want %q", err, want)
	}
	if _, err := engine.ResumeSessionByID("tui-current", "tui-target"); !errors.Is(err, ErrResumeActiveRun) || err.Error() != want {
		t.Fatalf("ResumeSessionByID error = %v, want %q", err, want)
	}
}

func TestEngine_ResumeSessionByIDRejectsActiveTargetRun(t *testing.T) {
	root := t.TempDir()
	store := session.NewMemoryStore()
	appendSessionEvent(t, store, "tui-current", session.SessionEvent{Kind: session.KindSessionCwd, Text: root})
	appendSessionEvent(t, store, "tui-target", session.SessionEvent{Kind: session.KindSessionCwd, Text: root})
	provider := &resumeBlockingProvider{started: make(chan struct{})}
	engine := New(Config{Root: root, Provider: provider, Store: store})
	if _, err := engine.SendPrompt("tui-target", session.Prompt{Text: "keep target running"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("target run did not start")
	}
	t.Cleanup(func() {
		engine.Stop("tui-target")
		_ = engine.Shutdown(context.Background())
	})

	_, err := engine.ResumeSessionByID("tui-current", "tui-target")
	if !errors.Is(err, ErrResumeActiveRun) || err.Error() != ErrResumeActiveRun.Error() {
		t.Fatalf("ResumeSessionByID error = %v, want active-run sentinel", err)
	}
}

func TestEngine_ResumeSessionByIDRejectsUnavailableTargets(t *testing.T) {
	root := t.TempDir()
	store := session.NewMemoryStore()
	appendSessionEvent(t, store, "tui-current", session.SessionEvent{Kind: session.KindSessionCwd, Text: root})
	appendSessionEvent(t, store, "app-session", session.SessionEvent{Kind: session.KindSessionCwd, Text: root})
	appendSessionEvent(t, store, "tui-other-root", session.SessionEvent{Kind: session.KindSessionCwd, Text: t.TempDir()})
	engine := New(Config{Root: root, Provider: llm.NewFakeProvider(), Store: store})

	for _, target := range []string{"tui-missing", "app-session", "tui-other-root"} {
		t.Run(target, func(t *testing.T) {
			_, err := engine.ResumeSessionByID("tui-current", target)
			if !errors.Is(err, ErrSessionNotResumable) || err.Error() != ErrSessionNotResumable.Error() {
				t.Fatalf("ResumeSessionByID(%q) error = %v", target, err)
			}
		})
	}
}

func TestEngine_ResumeSessionByIDSerializesTargetAdmission(t *testing.T) {
	root := t.TempDir()
	backend := session.NewMemoryStore()
	appendSessionEvent(t, backend, "tui-current", session.SessionEvent{Kind: session.KindSessionCwd, Text: root})
	appendSessionEvent(t, backend, "tui-target", session.SessionEvent{Kind: session.KindSessionCwd, Text: root})
	store := &blockingSessionsStore{
		Store:   backend,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	engine := New(Config{Root: root, Provider: llm.NewFakeProvider(), Store: store})
	t.Cleanup(func() { _ = engine.Shutdown(context.Background()) })

	resumeDone := make(chan error, 1)
	go func() {
		_, err := engine.ResumeSessionByID("tui-current", "tui-target")
		resumeDone <- err
	}()
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("resume did not block in Sessions")
	}

	sendStarted := make(chan struct{})
	sendDone := make(chan error, 1)
	go func() {
		close(sendStarted)
		_, err := engine.SendPrompt("tui-target", session.Prompt{Text: "wait for resume"})
		sendDone <- err
	}()
	<-sendStarted
	select {
	case err := <-sendDone:
		t.Fatalf("SendPrompt completed before resume validation/load released: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(store.release)
	select {
	case err := <-resumeDone:
		if err != nil {
			t.Fatalf("ResumeSessionByID error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ResumeSessionByID deadlocked")
	}
	select {
	case err := <-sendDone:
		if err != nil {
			t.Fatalf("SendPrompt error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SendPrompt did not proceed after resume released admission lock")
	}
}

// TestEngine_SlashNewStopsOldRunSoNewSessionStaysMostRecent covers, end to
// end on the real SQLite store, that /new stops a run still streaming into
// the old session before creating the fresh one. Otherwise the old session
// keeps writing durable events with a later activity timestamp and, after a
// restart, outranks the /new session in the /resume picker.
func TestEngine_SlashNewStopsOldRunSoNewSessionStaysMostRecent(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "atenea.db")
	store, err := session.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	provider := &releasableProvider{started: make(chan struct{}), release: make(chan struct{})}
	engine := New(Config{Root: root, Provider: provider, Store: store})
	oldRun, err := engine.SendPrompt("tui-old", session.Prompt{Text: "old conversation prompt"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("old run did not start streaming")
	}

	newRun, err := engine.SendPrompt("tui-old", session.Prompt{Text: "/new"})
	if err != nil {
		t.Fatal(err)
	}
	close(provider.release)
	waitRunDone(t, engine.Events(), oldRun.RunID)
	if err := engine.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Restart: a fresh engine over a fresh handle to the same database.
	restartedStore, err := session.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restartedStore.Close() })
	restarted := New(Config{Root: root, Provider: llm.NewFakeProvider(), Store: restartedStore})
	summaries, err := restarted.ListResumeSessions(restarted.NewSessionID())
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) == 0 || summaries[0].ID != newRun.SessionID {
		t.Fatalf("most recent resumable session = %+v, want the /new session %q first", summaries, newRun.SessionID)
	}
	events, err := restartedStore.Events(context.Background(), newRun.SessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Message != nil {
			t.Fatalf("/new session carries old conversation content: %+v", event)
		}
	}
}

// waitRunDone drains the engine event pump until the given run reports done.
func waitRunDone(t *testing.T, ch <-chan tea.Msg, runID uint64) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for run %d to finish", runID)
		case msg, ok := <-ch:
			if !ok {
				t.Fatal("engine event channel closed before the run finished")
			}
			if done, isDone := msg.(RunDoneMsg); isDone && done.RunID == runID {
				return
			}
		}
	}
}

func TestModeFromEventsIgnoresUnknownModeAfterValidMode(t *testing.T) {
	events := []session.SessionEvent{
		{Kind: session.KindSessionMode, Text: string(session.ModePlan)},
		{Kind: session.KindSessionMode, Text: "future-mode"},
	}
	if got := modeFromEvents(events); got != session.ModePlan {
		t.Fatalf("modeFromEvents = %q, want prior valid mode %q", got, session.ModePlan)
	}
}

func TestEngine_SendPromptDoesNotStartCheckpointWhenModePersistenceFails(t *testing.T) {
	backend := session.NewMemoryStore()
	modeErr := errors.New("mode persistence failed")
	store := &sessionModeFailingStore{Store: backend, err: modeErr}
	engine := New(Config{
		Root:        t.TempDir(),
		Provider:    llm.NewFakeProvider(),
		Store:       store,
		Checkpoints: fixedCheckpointStore{tree: checkpoint.Tree("before-tree")},
	})

	if _, err := engine.SendPrompt("tui-session", session.Prompt{Text: "hello"}); !errors.Is(err, modeErr) {
		t.Fatalf("SendPrompt error = %v, want %v", err, modeErr)
	}
	events, err := backend.Events(context.Background(), "tui-session", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == session.KindPromptCheckpointStarted {
			t.Fatalf("events = %+v, checkpoint started before mode persisted", events)
		}
	}
}

func TestEngine_PromptHistoryLoadsLatestTUIComposerPrompts(t *testing.T) {
	store := session.NewMemoryStore()
	ctx := context.Background()
	for i := 1; i <= 102; i++ {
		sessionID := "tui-old"
		if i > 51 {
			sessionID = "tui-new"
		}
		if _, err := store.AppendEvent(ctx, sessionID, session.SessionEvent{
			Kind: session.KindComposerPrompt,
			Text: "literal-" + strconv.Itoa(i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.AppendEvent(ctx, "app-session", session.SessionEvent{
		Kind: session.KindComposerPrompt,
		Text: "must not enter",
	}); err != nil {
		t.Fatal(err)
	}

	engine := New(Config{Root: t.TempDir(), Provider: llm.NewFakeProvider(), Store: store})
	got, err := engine.PromptHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != HistoryLimit {
		t.Fatalf("len(PromptHistory()) = %d, want %d", len(got), HistoryLimit)
	}
	if got[0] != "literal-3" || got[len(got)-1] != "literal-102" {
		t.Fatalf("PromptHistory() = [%q ... %q], want the 100 most recent TUI prompts in order", got[0], got[len(got)-1])
	}
}

func TestEngine_PromptHistoryFallsBackToLegacyUserMessages(t *testing.T) {
	store := session.NewMemoryStore()
	ctx := context.Background()
	for i, text := range []string{"old one", agent.AcceptPlanPrompt, "old two"} {
		if _, err := store.AppendEvent(ctx, "tui-legacy", session.SessionEvent{Message: &session.Message{
			ID:   "m" + strconv.Itoa(i),
			Role: session.RoleUser,
			Text: text,
		}}); err != nil {
			t.Fatal(err)
		}
	}

	engine := New(Config{Root: t.TempDir(), Provider: llm.NewFakeProvider(), Store: store})
	got, err := engine.PromptHistory()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"old one", "old two"}
	if !slices.Equal(got, want) {
		t.Fatalf("PromptHistory() = %q, want legacy fallback %q without AcceptPlan's internal prompt", got, want)
	}
}

func TestEngine_PromptHistoryStopsAfterLatestHundredPrompts(t *testing.T) {
	store := session.NewMemoryStore()
	ctx := context.Background()
	if _, err := store.AppendEvent(ctx, "tui-old", session.SessionEvent{Kind: session.KindComposerPrompt, Text: "too old"}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= HistoryLimit; i++ {
		if _, err := store.AppendEvent(ctx, "tui-new", session.SessionEvent{
			Kind: session.KindComposerPrompt,
			Text: "latest-" + strconv.Itoa(i),
		}); err != nil {
			t.Fatal(err)
		}
	}

	guarded := &promptHistoryStore{Store: store, blockedSession: "tui-old"}
	engine := New(Config{Root: t.TempDir(), Provider: llm.NewFakeProvider(), Store: guarded})
	got, err := engine.PromptHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != HistoryLimit || got[0] != "latest-1" || got[len(got)-1] != "latest-100" {
		t.Fatalf("PromptHistory() = [%q ... %q] (%d), want only the %d most recent prompts", got[0], got[len(got)-1], len(got), HistoryLimit)
	}
}

func TestEngine_SendPromptContinuesWhenHistoryPersistenceFails(t *testing.T) {
	store := &promptHistoryStore{Store: session.NewMemoryStore(), failComposerPrompt: true}
	engine := New(Config{Root: t.TempDir(), Provider: llm.NewFakeProvider(), Store: store})

	if _, err := engine.SendPrompt("tui-session", session.Prompt{Text: "hello"}); err != nil {
		t.Fatalf("SendPrompt() error = %v, accepted prompt must run even if history persistence fails", err)
	}
	_, done := collectUntilRunDone(t, engine.Events(), 3*time.Second, nil)
	if done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q, want a clean run", done.Err)
	}
}

// gatedBashTurns scripts the two-turn ask-before-run scenario: turn 1 asks for the gated bash tool with that command and turn 2 responds with text.
func gatedBashTurns(command string) [][]llm.Event {
	input, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		panic(err) // a map[string]string always marshals
	}
	return [][]llm.Event{
		{
			{Kind: llm.StepStarted},
			{Kind: llm.ToolCall, CallID: "c1", ToolName: "bash", Input: input},
			{Kind: llm.StepEnded},
		},
		{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: "ready"},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		},
	}
}

// gatedBashPairTurns scripts two consecutive turns, each with one gated bash
// call, so what the user answered on the first (c1) can be observed on the
// second (c2).
func gatedBashPairTurns(first, second string) [][]llm.Event {
	bashCall := func(callID, command string) llm.Event {
		input, err := json.Marshal(map[string]string{"command": command})
		if err != nil {
			panic(err) // a map[string]string always marshals
		}
		return llm.Event{Kind: llm.ToolCall, CallID: callID, ToolName: "bash", Input: input}
	}
	return [][]llm.Event{
		{{Kind: llm.StepStarted}, bashCall("c1", first), {Kind: llm.StepEnded}},
		{{Kind: llm.StepStarted}, bashCall("c2", second), {Kind: llm.StepEnded}},
		{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: "ready"},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		},
	}
}

// countEvents counts the events of a kind, whatever their CallID.
func countEvents(events []session.SessionEvent, kind session.EventKind) int {
	count := 0
	for _, ev := range events {
		if ev.Kind == kind {
			count++
		}
	}
	return count
}

// collectUntilRunDone consumes the engine channel in the test goroutine: it accumulates the EventMsg until it sees the RunDoneMsg and returns them, taking each message with nextMsg (which fails if the channel closes or times out). onEvent (optional) is called with each event upon arrival; The tests use it to react mid-run (resolve a permission, stop the session).
func collectUntilRunDone(t *testing.T, ch <-chan tea.Msg, timeout time.Duration, onEvent func(session.SessionEvent)) ([]session.SessionEvent, RunDoneMsg) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var events []session.SessionEvent
	for {
		switch m := nextMsg(t, ch, time.Until(deadline)).(type) {
		case EventMsg:
			ev := session.SessionEvent(m)
			events = append(events, ev)
			if onEvent != nil {
				onEvent(ev)
			}
		case RunDoneMsg:
			return events, m
		default:
			t.Fatalf("unexpected message on the engine channel: %T", m)
		}
	}
}

func seedCompactableEngineSession(t *testing.T, store session.Store, sessionID string) {
	t.Helper()
	for _, message := range []session.Message{
		{ID: "u1", Role: session.RoleUser, Text: "old"},
		{ID: "a1", Role: session.RoleAssistant, Text: "answer"},
		{ID: "u2", Role: session.RoleUser, Text: "current"},
	} {
		message := message
		if _, err := store.AppendEvent(context.Background(), sessionID, session.SessionEvent{Message: &message}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEngine_CompactIdleSessionStartsImmediately(t *testing.T) {
	store := session.NewMemoryStore()
	seedCompactableEngineSession(t, store, "s1")
	provider := newCompactQueueProvider()
	close(provider.release)
	e := New(Config{Root: t.TempDir(), Provider: provider, Store: store})

	if _, err := e.SendPrompt("s1", session.Prompt{Text: "/compact"}); err != nil {
		t.Fatalf("SendPrompt(/compact) error = %v", err)
	}
	msg := nextMsg(t, e.Events(), time.Second)
	status, ok := msg.(CompactionStatusMsg)
	if !ok || status.State != CompactionRunning {
		t.Fatalf("first message = %#v, want CompactionRunning", msg)
	}
}

func TestEngine_ShutdownCancelsAndWaitsForActiveRun(t *testing.T) {
	provider := newDelayedCancellationProvider()
	e := New(Config{Root: t.TempDir(), Provider: provider, Store: session.NewMemoryStore()})
	if _, err := e.SendPrompt("s1", session.Prompt{Text: "wait"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("run did not start")
	}
	for len(e.events) < cap(e.events) {
		e.events <- struct{}{}
	}

	done := make(chan error, 1)
	go func() { done <- e.Shutdown(context.Background()) }()
	select {
	case <-provider.cancelSeen:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel the active run")
	}
	select {
	case err := <-done:
		t.Fatalf("Shutdown returned before the canceled run finished: %v", err)
	default:
	}
	close(provider.releaseFirst)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not wait for the canceled run")
	}
}

func TestEngine_ShutdownFinishesCheckpointBeforeSQLiteClose(t *testing.T) {
	root := newUndoWorkspace(t)
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	store, err := session.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	provider := &blockingAfterToolProvider{started: make(chan struct{}), canceled: make(chan struct{})}
	e := New(Config{Root: root, Provider: provider, Store: store, Checkpoints: checkpoint.NewGitStore(t.TempDir())})
	if _, err := e.SendPrompt("s1", session.Prompt{Text: "create file"}); err != nil {
		t.Fatal(err)
	}
	// The write is gated: approve it in the background so the run reaches the
	// blocking turn.
	t.Cleanup(resolveUntilStopped(e, "s1", "write-1", permission.AllowedOnce))
	select {
	case <-provider.started:
	case <-time.After(10 * time.Second):
		t.Fatal("provider did not block after the tool")
	}
	if err := e.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = session.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	boundary, err := store.LatestPromptCheckpoint(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if boundary.AfterTree == "" {
		t.Fatal("checkpoint remained incomplete after shutdown")
	}
}

func TestEngine_ShutdownCancelsAndWaitsForCompaction(t *testing.T) {
	store := session.NewMemoryStore()
	seedCompactableEngineSession(t, store, "s1")
	provider := newBlockingSummaryProvider()
	e := New(Config{Root: t.TempDir(), Provider: provider, Store: store})
	if _, err := e.SendPrompt("s1", session.Prompt{Text: "/compact"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("compaction did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := e.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestEngine_CompactDuringRunQueuesOnceAndDrainsAfterCompletion(t *testing.T) {
	store := session.NewMemoryStore()
	seedCompactableEngineSession(t, store, "s1")
	provider := newCompactQueueProvider()
	e := New(Config{Root: t.TempDir(), Provider: provider, Store: store})

	if _, err := e.SendPrompt("s1", session.Prompt{Text: "continue turn"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("turn did not start")
	}
	for range 2 {
		if _, err := e.SendPrompt("s1", session.Prompt{Text: "/compact"}); err != nil {
			t.Fatal(err)
		}
	}
	var status CompactionStatusMsg
	for {
		msg := nextMsg(t, e.Events(), time.Second)
		if candidate, ok := msg.(CompactionStatusMsg); ok {
			status = candidate
			break
		}
	}
	if status.State != CompactionQueued {
		t.Fatalf("queued message = %#v", status)
	}
	select {
	case duplicate := <-e.Events():
		t.Fatalf("duplicate /compact emitted %#v", duplicate)
	case <-time.After(30 * time.Millisecond):
	}
	close(provider.release)

	deadline := time.After(2 * time.Second)
	seenCompacted := false
	for !seenCompacted {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for Context.Compacted")
		case message := <-e.Events():
			if event, ok := message.(EventMsg); ok && event.Kind == session.KindContextCompacted {
				seenCompacted = true
			}
		}
	}
	if got := provider.callCount(); got != 2 {
		t.Fatalf("provider calls = %d, want turn + one summary", got)
	}
}

func TestEngine_CompactWithArgumentsRemainsNormalPrompt(t *testing.T) {
	store := session.NewMemoryStore()
	provider := newTurnProvider([]llm.Event{{Kind: llm.StepEnded}})
	e := New(Config{Root: t.TempDir(), Provider: provider, Store: store})
	if _, err := e.SendPrompt("s1", session.Prompt{Text: "/compact later"}); err != nil {
		t.Fatal(err)
	}
	_, done := collectUntilRunDone(t, e.Events(), time.Second, nil)
	if done.Err != "" {
		t.Fatal(done.Err)
	}
	messages, err := store.Messages(context.Background(), "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) == 0 || messages[0].Text != "/compact later" {
		t.Fatalf("messages = %+v, want literal prompt", messages)
	}
}

func TestEngine_QueuedCompactRunsAfterCancellation(t *testing.T) {
	store := session.NewMemoryStore()
	seedCompactableEngineSession(t, store, "s1")
	provider := newCompactQueueProvider()
	e := New(Config{Root: t.TempDir(), Provider: provider, Store: store})
	if _, err := e.SendPrompt("s1", session.Prompt{Text: "continue turn"}); err != nil {
		t.Fatal(err)
	}
	<-provider.started
	if _, err := e.SendPrompt("s1", session.Prompt{Text: "/compact"}); err != nil {
		t.Fatal(err)
	}
	e.Stop("s1")
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for compaction after cancellation")
		case message := <-e.Events():
			if event, ok := message.(EventMsg); ok && event.Kind == session.KindContextCompacted {
				return
			}
		}
	}
}

func TestEngine_QueuedCompactWaitsForReplacementRun(t *testing.T) {
	store := session.NewMemoryStore()
	seedCompactableEngineSession(t, store, "s1")
	provider := newReplacementRunCompactionProvider()
	e := New(Config{Root: t.TempDir(), Provider: provider, Store: store})

	if _, err := e.SendPrompt("s1", session.Prompt{Text: "first"}); err != nil {
		t.Fatal(err)
	}
	<-provider.started[0]
	if _, err := e.SendPrompt("s1", session.Prompt{Text: "/compact"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SendPrompt("s1", session.Prompt{Text: "replacement"}); err != nil {
		t.Fatal(err)
	}
	<-provider.started[1]

	select {
	case <-provider.started[2]:
		t.Fatal("queued compaction started while replacement run was still active")
	case <-time.After(100 * time.Millisecond):
	}
	e.Stop("s1")
	select {
	case <-provider.started[2]:
	case <-time.After(time.Second):
		t.Fatal("queued compaction did not start after replacement run stopped")
	}
}

func TestEngine_PromptAfterIdleCompactWaitsForCommittedContext(t *testing.T) {
	store := session.NewMemoryStore()
	seedCompactableEngineSession(t, store, "s1")
	provider := newBlockingSummaryProvider()
	e := New(Config{Root: t.TempDir(), Provider: provider, Store: store})
	if _, err := e.SendPrompt("s1", session.Prompt{Text: "/compact"}); err != nil {
		t.Fatal(err)
	}
	<-provider.started
	promptDone := make(chan error, 1)
	go func() {
		_, err := e.SendPrompt("s1", session.Prompt{Text: "next prompt"})
		promptDone <- err
	}()
	time.Sleep(30 * time.Millisecond)
	if got := provider.callCount(); got != 1 {
		t.Fatalf("provider calls before summary release = %d, prompt overtook compaction", got)
	}
	select {
	case err := <-promptDone:
		t.Fatalf("prompt returned before compaction finished: %v", err)
	default:
	}
	close(provider.release)
	if err := <-promptDone; err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for provider.callCount() < 2 {
		select {
		case <-deadline:
			t.Fatal("prompt did not start after compaction")
		case <-time.After(time.Millisecond):
		}
	}
}

// lastEvent returns the last event with that Kind and CallID, or nil if it did not arrive.
func lastEvent(events []session.SessionEvent, kind session.EventKind, callID string) *session.SessionEvent {
	var found *session.SessionEvent
	for i, ev := range events {
		if ev.Kind == kind && ev.CallID == callID {
			found = &events[i]
		}
	}
	return found
}

// writeSkill creates <root>/.atenea/skills/<name>/SKILL.md with the frontmatter name/description (same format as internal/skill tests): the source from which the wiring derives composer slash-commands.
func writeSkill(t *testing.T, root, name, desc string) {
	t.Helper()
	dir := filepath.Join(root, ".atenea", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	front := "---\nname: " + name + "\ndescription: " + desc + "\n---\nbody of " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(front), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

func TestEngine_ExposesCommandsFromSkills(t *testing.T) {
	// The Engine exposes the slash-commands derived from the discovered skills (mirror of App.ListCommands): the TUI wires them to the composer's "/" menu. It asserts CONTAINMENT, not equality: the wiring also reveals the global skills of the user's home.
	root := t.TempDir()
	writeSkill(t, root, "greets", "greets with style")

	e := New(Config{Root: root, Provider: llm.NewFakeProvider(), Store: session.NewMemoryStore()})

	cmds := e.Commands()
	for _, c := range cmds {
		if c.Name == "greets" {
			if c.Description != "greets with style" {
				t.Fatalf("Commands() returned greets with Description = %q, want %q", c.Description, "greets with style")
			}
			return
		}
	}
	t.Fatalf("Commands() = %v, must contain the command %q derived from the project skill", cmds, "greets")
}

func TestEngine_CommandsListsLocalAndSkillCommandsFromOneSet(t *testing.T) {
	e := New(Config{Root: t.TempDir(), Provider: llm.NewFakeProvider(), Store: session.NewMemoryStore()})
	want := map[string]bool{
		"new": true, "compact": true, "model": true, "mcp": true,
		"connect": true, "resume": true, "undo": true,
	}
	for _, cmd := range e.Commands() {
		if expectedBuiltin, ok := want[cmd.Name]; ok {
			if cmd.BuiltIn != expectedBuiltin {
				t.Fatalf("command %q BuiltIn = %v, want %v", cmd.Name, cmd.BuiltIn, expectedBuiltin)
			}
			delete(want, cmd.Name)
		}
	}
	if len(want) != 0 {
		t.Fatalf("Commands() is missing local commands: %v", want)
	}
}

func TestEngine_ProjectFilesListsWorkspace(t *testing.T) {
	// The Engine lists workspace files (paths relative to the root) for the composer's @-menu (a mirror of App.ListProjectFiles). The actual glob uses ripgrep; without rg installed, the case is skipped.
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skipf("rg unavailable: %v", err)
	}
	root := t.TempDir()
	for _, f := range []string{"a.go", filepath.Join("sub", "b.txt")} {
		path := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte("content"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	e := New(Config{Root: root, Provider: llm.NewFakeProvider(), Store: session.NewMemoryStore()})

	files, err := e.ProjectFiles()
	if err != nil {
		t.Fatalf("ProjectFiles() = %v, expected nil", err)
	}
	for _, want := range []string{"a.go", filepath.Join("sub", "b.txt")} {
		if !slices.Contains(files, want) {
			t.Fatalf("ProjectFiles() = %v, must contain relative path %q", files, want)
		}
	}
}

func TestEngine_SendPromptExpandsSlashCommand(t *testing.T) {
	// SendPrompt expands a slash-command before enqueuing it (agent.Service mirror): the promoted Message user carries the EXPANDED prompt from the skill template, not the literal "/greets...". A non-command prompt passes without changes. It also covers SendPlanPrompt: both share the common send path.
	root := t.TempDir()
	writeSkill(t, root, "greets", "greets with style")
	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "hello"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)
	e := New(Config{Root: root, Provider: fake, Store: session.NewMemoryStore()})

	// lastUserPrompt runs a full run and returns the last Message user promoted between its events.
	lastUserPrompt := func(sessionID, text string) string {
		t.Helper()
		if _, err := e.SendPrompt(sessionID, session.Prompt{Text: text}); err != nil {
			t.Fatalf("SendPrompt(%s, %s) = %v, expected nil", sessionID, text, err)
		}
		events, done := collectUntilRunDone(t, e.Events(), 10*time.Second, nil)
		if done.Err != "" {
			t.Fatalf("RunDoneMsg.Err = %q, want a clean run", done.Err)
		}
		prompt := ""
		for _, ev := range events {
			if ev.Message != nil && ev.Message.Role == session.RoleUser {
				prompt = ev.Message.Text
			}
		}
		return prompt
	}

	// The FromSkills template is emitted by the production command expander.
	want := string([]rune{85, 115, 97, 32, 108, 97, 32, 115, 107, 105, 108, 108, 32, 34, 103, 114, 101, 101, 116, 115, 34, 46}) + "\n\nhello world"
	if got := lastUserPrompt("s1", "/greets hello world"); got != want {
		t.Fatalf("promoted user Message = %q, want expanded prompt %q, not the literal command", got, want)
	}

	// A non-command prompt passes untransformed.
	if got := lastUserPrompt("s2", "hello normal"); got != "hello normal" {
		t.Fatalf("promoted user Message = %q, a non-command prompt must pass unchanged (%q)", got, "hello normal")
	}
}

func TestEngine_StreamsSessionEventsAndSignalsRunDone(t *testing.T) {
	// A text-only turn: the complete script for a clean run.
	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "Hello from the engine"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)

	e := New(Config{Root: t.TempDir(), Provider: fake, Store: session.NewMemoryStore()})

	if _, err := e.SendPrompt("s1", session.Prompt{Text: "hello"}); err != nil {
		t.Fatalf("SendPrompt(s1, hello) = %v, expected nil", err)
	}

	events, done := collectUntilRunDone(t, e.Events(), 5*time.Second, nil)

	var sawUserPrompt bool // (a) the prompt promoted to a durable user message
	var sawTextDelta bool  // (b) at least one Text.Delta
	var deltas strings.Builder
	var sawStepEnded bool // (c) the end of the turn
	for _, ev := range events {
		if ev.Message != nil && ev.Message.Role == session.RoleUser && ev.Message.Text == "hello" {
			sawUserPrompt = true
		}
		if ev.Kind == session.KindTextDelta {
			sawTextDelta = true
			deltas.WriteString(ev.Text)
		}
		if ev.Kind == session.KindStepEnded {
			sawStepEnded = true
		}
	}

	if !sawUserPrompt {
		t.Errorf("promoted user message with Text %q did not arrive among %d events", "hello", len(events))
	}
	if !sawTextDelta {
		t.Errorf("no %s event arrived", session.KindTextDelta)
	} else if got := deltas.String(); !strings.Contains(got, "Hello from the engine") {
		t.Errorf("accumulated text of %s = %q, must contain %q", session.KindTextDelta, got, "Hello from the engine")
	}
	if !sawStepEnded {
		t.Errorf("no %s event arrived", session.KindStepEnded)
	}
	if done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q, expected a clean run (Err empty)", done.Err)
	}
}

func TestEngine_ReplacementRunWaitsForCanceledRunAndKeepsDistinctIdentity(t *testing.T) {
	provider := newDelayedCancellationProvider()
	e := New(Config{Root: t.TempDir(), Provider: provider, Store: session.NewMemoryStore()})

	first, err := e.SendPrompt("s1", session.Prompt{Text: "first"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first run did not start")
	}

	second, err := e.SendPrompt("s1", session.Prompt{Text: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if first.RunID == second.RunID {
		t.Fatalf("run IDs = %d and %d, expected distinct identities", first.RunID, second.RunID)
	}
	select {
	case <-provider.cancelSeen:
	case <-time.After(time.Second):
		t.Fatal("first run did not receive cancellation")
	}
	select {
	case <-provider.secondStarted:
		t.Fatal("new run started before the canceled run ended")
	case <-time.After(50 * time.Millisecond):
	}

	close(provider.releaseFirst)
	select {
	case <-provider.secondStarted:
	case <-time.After(time.Second):
		t.Fatal("new run did not start after the canceled run ended")
	}

	var done []RunDoneMsg
	deadline := time.After(2 * time.Second)
	for len(done) < 2 {
		select {
		case <-deadline:
			t.Fatalf("received completions = %+v, expected both runs", done)
		case msg := <-e.Events():
			if runDone, ok := msg.(RunDoneMsg); ok {
				done = append(done, runDone)
			}
		}
	}
	if done[0].SessionID != "s1" || done[0].RunID != first.RunID {
		t.Fatalf("first completion = %+v, expected session s1 run %d", done[0], first.RunID)
	}
	if done[1].SessionID != "s1" || done[1].RunID != second.RunID {
		t.Fatalf("second completion = %+v, expected session s1 run %d", done[1], second.RunID)
	}
}

func TestEngine_UndoRestoresDeletedAndRecreatedTrackedFile(t *testing.T) {
	root := newUndoWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runUndoGit(t, root, "add", "tracked.txt")
	runUndoGit(t, root, "commit", "-m", "initial")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("preexisting-change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("preexisting-untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	provider := newTurnProvider(
		[]llm.Event{
			{Kind: llm.StepStarted},
			{Kind: llm.ToolCall, CallID: "remove-tracked", ToolName: "bash", Input: json.RawMessage(`{"command":"rm tracked.txt"}`)},
			{Kind: llm.StepEnded},
		},
		[]llm.Event{
			{Kind: llm.StepStarted},
			{Kind: llm.ToolCall, CallID: "rewrite-tracked", ToolName: "write", Input: json.RawMessage(`{"path":"tracked.txt","content":"prompt-change\n"}`)},
			{Kind: llm.StepEnded},
		},
		[]llm.Event{
			{Kind: llm.StepStarted},
			{Kind: llm.ToolCall, CallID: "create-file", ToolName: "write", Input: json.RawMessage(`{"path":"created.txt","content":"created-by-prompt\n"}`)},
			{Kind: llm.StepEnded},
		},
		[]llm.Event{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: "changed files"},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		},
	)
	store := session.NewMemoryStore()
	engine := New(Config{
		Root:        root,
		Provider:    provider,
		Store:       store,
		Checkpoints: checkpoint.NewGitStore(t.TempDir()),
	})

	if _, err := engine.SendPrompt("s1", session.Prompt{Text: "change the files [image#1]", Images: []session.Image{{MediaType: "image/png", Data: []byte("prompt-image")}}}); err != nil {
		t.Fatal(err)
	}
	events, done := collectUntilRunDone(t, engine.Events(), 10*time.Second, approveAllPermissions(t, engine))
	if done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q", done.Err)
	}
	for _, callID := range []string{"remove-tracked", "rewrite-tracked", "create-file"} {
		if lastEvent(events, session.KindToolSuccess, callID) == nil {
			t.Fatalf("tool call %q did not succeed", callID)
		}
	}
	assertUndoFile(t, root, "tracked.txt", "prompt-change\n")
	assertUndoFile(t, root, "created.txt", "created-by-prompt\n")

	result, err := engine.Undo("s1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Prompt.Text != "change the files [image#1]" || len(result.Prompt.Images) != 1 || !bytes.Equal(result.Prompt.Images[0].Data, []byte("prompt-image")) {
		t.Fatalf("Prompt = %+v", result.Prompt)
	}
	assertUndoFile(t, root, "tracked.txt", "preexisting-change\n")
	assertUndoFile(t, root, "notes.txt", "preexisting-untracked\n")
	assertUndoMissing(t, root, "created.txt")

	messages, err := store.Messages(context.Background(), "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("effective messages = %+v, want none", messages)
	}
}

func TestEngine_GatedBashApprovedRunsAndSettles(t *testing.T) {
	provider := newTurnProvider(gatedBashTurns("echo hello-gate")...)
	e := New(Config{Root: t.TempDir(), Provider: provider, Store: session.NewMemoryStore()})

	if _, err := e.SendPrompt("s1", session.Prompt{Text: "run the command"}); err != nil {
		t.Fatalf("SendPrompt(s1, run the command) = %v, expected nil", err)
	}

	// By viewing the permission request, the user APPROVES the tool.
	events, done := collectUntilRunDone(t, e.Events(), 10*time.Second, func(ev session.SessionEvent) {
		if ev.Kind == session.KindToolPermissionRequested && ev.CallID == "c1" {
			t.Cleanup(resolveUntilStopped(e, ev.SessionID, "c1", permission.AllowedOnce))
		}
	})

	success := lastEvent(events, session.KindToolSuccess, "c1")
	if success == nil {
		t.Fatalf("no %s event for c1 among %d events: the approved tool must run and settle", session.KindToolSuccess, len(events))
	}
	if !strings.Contains(success.Text, "hello-gate") {
		t.Errorf("Tool.Success for c1 Text = %q, must contain %q (bash really ran)", success.Text, "hello-gate")
	}
	if done.Err != "" {
		t.Errorf("RunDoneMsg.Err = %q, expected a clean run (Err empty)", done.Err)
	}
}

func TestEngine_GatedBashDeniedFailsWithoutRunning(t *testing.T) {
	root := t.TempDir()
	forbidden := filepath.Join(root, "must-not-exist")
	provider := newTurnProvider(gatedBashTurns("touch " + forbidden)...)
	e := New(Config{Root: root, Provider: provider, Store: session.NewMemoryStore()})

	if _, err := e.SendPrompt("s1", session.Prompt{Text: "run the command"}); err != nil {
		t.Fatalf("SendPrompt(s1, run the command) = %v, expected nil", err)
	}

	// Upon seeing the permission request, the user DENIES the tool.
	events, done := collectUntilRunDone(t, e.Events(), 10*time.Second, func(ev session.SessionEvent) {
		if ev.Kind == session.KindToolPermissionRequested && ev.CallID == "c1" {
			t.Cleanup(resolveUntilStopped(e, ev.SessionID, "c1", permission.Denied))
		}
	})

	if ev := lastEvent(events, session.KindToolSuccess, "c1"); ev != nil {
		t.Fatalf("received %s for c1 with Text %q: a denied tool must not run", session.KindToolSuccess, ev.Text)
	}
	failed := lastEvent(events, session.KindToolFailed, "c1")
	if failed == nil {
		t.Fatalf("no %s event for c1 among %d events: denial must settle the tool as failed", session.KindToolFailed, len(events))
	}
	if !strings.Contains(strings.ToLower(failed.Error), "deni") {
		t.Errorf("Tool.Failed for c1 Error = %q, must mention the denial", failed.Error)
	}
	if done.Err != "" {
		t.Errorf("RunDoneMsg.Err = %q, denial is not a run failure (Err must be empty)", done.Err)
	}
	// The hard proof that bash did NOT run: the file that the command would touch must not exist after the end of the run.
	if _, err := os.Stat(forbidden); !os.IsNotExist(err) {
		t.Errorf("os.Stat(%q) = %v, file must not exist: denied tool must not run the command", forbidden, err)
	}
}

// TestEngine_AllowSessionStopsAskingForTheSameCommandPrefix is the end-to-end
// contract of a session grant: the user answers "allow `echo` this session" on
// the first command and the next command of the same shape runs without asking
// at all — no second permission request reaches the UI, and none is recorded in
// the session's history.
func TestEngine_AllowSessionStopsAskingForTheSameCommandPrefix(t *testing.T) {
	provider := newTurnProvider(gatedBashPairTurns("echo -n uno", "echo -n dos")...)
	e := New(Config{Root: t.TempDir(), Provider: provider, Store: session.NewMemoryStore()})

	if _, err := e.SendPrompt("s1", session.Prompt{Text: "run the commands"}); err != nil {
		t.Fatalf("SendPrompt = %v, want nil", err)
	}
	events, done := collectUntilRunDone(t, e.Events(), 10*time.Second, func(ev session.SessionEvent) {
		if ev.Kind == session.KindToolPermissionRequested && ev.CallID == "c1" {
			t.Cleanup(resolveUntilStopped(e, ev.SessionID, "c1", permission.AllowedSession))
		}
	})

	if got := countEvents(events, session.KindToolPermissionRequested); got != 1 {
		t.Errorf("%s = %d times, want 1: the session grant must avoid the second question", session.KindToolPermissionRequested, got)
	}
	for callID, want := range map[string]string{"c1": "uno", "c2": "dos"} {
		success := lastEvent(events, session.KindToolSuccess, callID)
		if success == nil {
			t.Fatalf("no %s arrived for %s among %d events", session.KindToolSuccess, callID, len(events))
		}
		if !strings.Contains(success.Text, want) {
			t.Errorf("Tool.Success of %s with Text = %q, must contain %q", callID, success.Text, want)
		}
	}
	if done.Err != "" {
		t.Errorf("RunDoneMsg.Err = %q, want a clean run", done.Err)
	}
}

// TestEngine_AllowSessionDoesNotCoverADifferentCommand: the grant is the shape
// the user saw, not a blanket pass on bash. A command with another prefix asks
// again.
func TestEngine_AllowSessionDoesNotCoverADifferentCommand(t *testing.T) {
	provider := newTurnProvider(gatedBashPairTurns("echo -n uno", "printf dos")...)
	e := New(Config{Root: t.TempDir(), Provider: provider, Store: session.NewMemoryStore()})

	if _, err := e.SendPrompt("s1", session.Prompt{Text: "run the commands"}); err != nil {
		t.Fatalf("SendPrompt = %v, want nil", err)
	}
	events, done := collectUntilRunDone(t, e.Events(), 10*time.Second, func(ev session.SessionEvent) {
		if ev.Kind != session.KindToolPermissionRequested {
			return
		}
		verdict := permission.AllowedSession
		if ev.CallID == "c2" {
			verdict = permission.AllowedOnce
		}
		t.Cleanup(resolveUntilStopped(e, ev.SessionID, ev.CallID, verdict))
	})

	if got := countEvents(events, session.KindToolPermissionRequested); got != 2 {
		t.Errorf("%s = %d times, want 2: `printf dos` is not covered by the `echo` grant", session.KindToolPermissionRequested, got)
	}
	if done.Err != "" {
		t.Errorf("RunDoneMsg.Err = %q, want a clean run", done.Err)
	}
}

func TestEngine_StopUnblocksPendingPermission(t *testing.T) {
	// A single turn: the gated tool waits for approval forever; Stop must unlock it and close the clean run.
	provider := newTurnProvider([]llm.Event{
		{Kind: llm.StepStarted},
		{Kind: llm.ToolCall, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"echo blocked"}`)},
		{Kind: llm.StepEnded},
	})
	e := New(Config{Root: t.TempDir(), Provider: provider, Store: session.NewMemoryStore()})

	if _, err := e.SendPrompt("s1", session.Prompt{Text: "run the command"}); err != nil {
		t.Fatalf("SendPrompt(s1, run the command) = %v, expected nil", err)
	}

	// Instead of deciding, the user stops the run.
	events, done := collectUntilRunDone(t, e.Events(), 10*time.Second, func(ev session.SessionEvent) {
		if ev.Kind == session.KindToolPermissionRequested && ev.CallID == "c1" {
			e.Stop("s1")
		}
	})

	if lastEvent(events, session.KindToolFailed, "c1") == nil {
		t.Errorf("no %s arrived for c1: Stop must settle the pending call as interrupted", session.KindToolFailed)
	}
	if done.Err != "" {
		t.Errorf("RunDoneMsg.Err = %q, deliberate cancellation is a clean completion (Err empty)", done.Err)
	}
}

func TestEngine_AcceptPlanRunsImplementationInNormalMode(t *testing.T) {
	// TRIANGULATE: AcceptPlan must return the session to normal mode and promote the fixed implementation prompt as the user prompt, starting the run (mirror of App.AcceptPlan). Observable evidence: the AcceptPlan turn Request announces bash again (normal mode) and between the events the Message user arrives with the fixed prompt text.
	textTurn := func(text string) []llm.Event {
		return []llm.Event{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: text},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		}
	}
	provider := newTurnProvider(textTurn("plan ready"), textTurn("implemented"))
	e := New(Config{Root: t.TempDir(), Provider: provider, Store: session.NewMemoryStore()})

	// Previous plan run: leaves the session in plan-mode with the plan presented.
	if _, err := e.SendPlanPrompt("s1", session.Prompt{Text: "plan"}); err != nil {
		t.Fatalf("SendPlanPrompt(s1, plan) = %v, expected nil", err)
	}
	if _, done := collectUntilRunDone(t, e.Events(), 10*time.Second, nil); done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q, expected a clean run in plan mode", done.Err)
	}
	planCalls := len(provider.requestedTools())

	// The user accepts the plan: he must start the implementation run.
	if _, err := e.AcceptPlan("s1"); err != nil {
		t.Fatalf("AcceptPlan(s1) = %v, expected nil", err)
	}
	events, done := collectUntilRunDone(t, e.Events(), 10*time.Second, nil)
	if done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q, expected a clean run while executing the plan", done.Err)
	}

	calls := provider.requestedTools()
	if len(calls) <= planCalls {
		t.Fatalf("the provider recorded %d Stream calls after AcceptPlan (%d before): accepting the plan must start a new run", len(calls), planCalls)
	}
	acceptTools := calls[len(calls)-1]
	if !slices.Contains(acceptTools, "bash") {
		t.Errorf("AcceptPlan turn tools = %v, must include %q: accepting the plan returns the session to normal mode", acceptTools, "bash")
	}

	var prompt *session.Message
	for _, ev := range events {
		if ev.Message != nil && ev.Message.Role == session.RoleUser {
			msg := *ev.Message
			prompt = &msg
		}
	}
	if prompt == nil {
		t.Fatalf("no user Message arrived among %d events: AcceptPlan must promote the fixed implementation prompt", len(events))
	}
	if !strings.Contains(prompt.Text, "aprobado") {
		t.Errorf("promoted user Message = %q, must contain %q (the fixed implementation prompt)", prompt.Text, "aprobado")
	}
}

func TestEngine_SendPlanPromptRunsInPlanMode(t *testing.T) {
	// TRIANGULATE: SendPlanPrompt should run the turn in REAL plan-mode (like in the Wails app), not delegate to SendPrompt. The observable evidence is the tools that the runner announces to the model in the Request of each turn: plan-mode announces present_plan and hides bash/write; the mode is by send, so a subsequent SendPrompt in the SAME session announces bash again.
	textTurn := func(text string) []llm.Event {
		return []llm.Event{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: text},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		}
	}
	provider := newTurnProvider(textTurn("plan ready"), textTurn("done"))
	e := New(Config{Root: t.TempDir(), Provider: provider, Store: session.NewMemoryStore()})

	// Sending in plan-mode: the shift must announce the planning tools.
	if _, err := e.SendPlanPrompt("s1", session.Prompt{Text: "plan x"}); err != nil {
		t.Fatalf("SendPlanPrompt(s1, plan x) = %v, expected nil", err)
	}
	if _, done := collectUntilRunDone(t, e.Events(), 10*time.Second, nil); done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q, expected a clean run in plan mode", done.Err)
	}
	calls := provider.requestedTools()
	if len(calls) == 0 {
		t.Fatalf("the provider recorded no Stream call after the plan run")
	}
	planTools := calls[len(calls)-1]
	if !slices.Contains(planTools, "present_plan") {
		t.Errorf("plan turn tools = %v, must include %q: SendPlanPrompt must run in real plan mode", planTools, "present_plan")
	}
	for _, forbidden := range []string{"bash", "write"} {
		if slices.Contains(planTools, forbidden) {
			t.Errorf("plan turn tools = %v, must not include %q: plan mode is read-only", planTools, forbidden)
		}
	}

	// Subsequent normal sending in the SAME session: the mode is by sending (mirror of the Wails app) and the turn announces the build tools again.
	if _, err := e.SendPrompt("s1", session.Prompt{Text: "do it"}); err != nil {
		t.Fatalf("SendPrompt(s1, do it) = %v, expected nil", err)
	}
	if _, done := collectUntilRunDone(t, e.Events(), 10*time.Second, nil); done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q, expected a clean run in normal mode", done.Err)
	}
	calls = provider.requestedTools()
	if len(calls) < 2 {
		t.Fatalf("the provider recorded %d Stream calls, expected at least 2", len(calls))
	}
	buildTools := calls[len(calls)-1]
	if !slices.Contains(buildTools, "bash") {
		t.Errorf("normal turn tools = %v, must include %q: SendPrompt returns to build mode", buildTools, "bash")
	}
}

func TestEngine_ToolResultNeverPrecedesAssistantMessageInHistory(t *testing.T) {
	// NETWORK (real bug seen with OpenRouter/Cohere): when the model responds ONLY with a tool call that fails instantly (read with absolute path: dies on sandboxJoin validation, no I/O), the Tool.Failed (which materializes the Message role=tool) can be persisted BEFORE the Step.Ended (which materializes the Message assistant with the tool_calls), because the runner seats the tool in a concurrent goroutine while the StepEnded still travels through the network. The projected history becomes `user, tool, assistant` and the next request to the provider returns 400: "tool call id not found in previous tool calls". The provider's delay reproduces that race deterministically: the last SSE chunk (StepEnded) arrives ~100ms late.
	provider := newTurnProvider(
		[]llm.Event{
			{Kind: llm.StepStarted},
			{Kind: llm.ToolCall, CallID: "c1", ToolName: "read", Input: json.RawMessage(`{"path":"/etc/fuera"}`)},
			{Kind: llm.StepEnded},
		},
		[]llm.Event{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: "could not read it"},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		},
	)
	provider.delayStepEnded = 100 * time.Millisecond
	e := New(Config{Root: t.TempDir(), Provider: provider, Store: session.NewMemoryStore()})

	if _, err := e.SendPrompt("s1", session.Prompt{Text: "read that"}); err != nil {
		t.Fatalf("SendPrompt(s1, read that) = %v, expected nil", err)
	}
	if _, done := collectUntilRunDone(t, e.Events(), 10*time.Second, nil); done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q, expected a clean run (the failed tool is not a run failure)", done.Err)
	}

	calls := provider.requestedMessages()
	if len(calls) < 2 {
		t.Fatalf("the provider recorded %d Stream calls, expected at least 2", len(calls))
	}
	history := calls[1] // projected history seen by the provider on turn 2

	// The projected role sequence, for a readable failure message.
	roles := make([]string, len(history))
	for i, m := range history {
		roles[i] = m.Role
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			roles[i] = "assistant(tool_calls)"
		}
		if m.Role == "tool" {
			roles[i] = "tool(" + m.ToolCallID + ")"
		}
	}

	assistantIdx, toolIdx := -1, -1
	for i, m := range history {
		if m.Role == "assistant" && assistantIdx < 0 {
			for _, tc := range m.ToolCalls {
				if tc.ID == "c1" {
					assistantIdx = i
				}
			}
		}
		if m.Role == "tool" && m.ToolCallID == "c1" && toolIdx < 0 {
			toolIdx = i
		}
	}

	if assistantIdx < 0 {
		t.Fatalf("turn 2 history has no assistant Message with tool call c1; projected roles: %v", roles)
	}
	if toolIdx < 0 {
		t.Fatalf("turn 2 history has no tool Message with ToolCallID c1; projected roles: %v", roles)
	}
	if toolIdx < assistantIdx {
		t.Fatalf("tool Message c1 (index %d) precedes its assistant tool-call Message (index %d); projected roles: %v", toolIdx, assistantIdx, roles)
	}
}

func TestEngine_CapturesSessionCwdOnFirstPrompt(t *testing.T) {
	// The FIRST prompt of a session (when LoadSession still fails) must record the working folder as a SessionEvent Session.Cwd FIRST in the log, before supporting the prompt (mirror of App.captureCwd): thus the Wails app sidebar groups the TUI sessions by folder.
	root := t.TempDir()
	store := session.NewMemoryStore()
	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "hello"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)
	e := New(Config{Root: root, Provider: fake, Store: store})

	if _, err := e.SendPrompt("s1", session.Prompt{Text: "hello"}); err != nil {
		t.Fatalf("SendPrompt(s1, hello) = %v, expected nil", err)
	}
	if _, done := collectUntilRunDone(t, e.Events(), 10*time.Second, nil); done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %v, want a clean run", done.Err)
	}

	// (a) The first durable event in the log (Seq 1) is Session.Cwd with the root.
	ctx := context.Background()
	events, err := store.Events(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("store.Events(s1) = %v, expected nil", err)
	}
	if len(events) == 0 {
		t.Fatal("store.Events(s1) has no events: the run must persist the log")
	}
	first := events[0]
	if first.Seq != 1 || first.Kind != session.KindSessionCwd || first.Text != root {
		t.Errorf("first log event = {Seq:%d Kind:%q Text:%q}, want {Seq:1 Kind:%q Text:%q}: folder must be recorded BEFORE accepting the prompt", first.Seq, first.Kind, first.Text, session.KindSessionCwd, root)
	}

	// (b) The Sessions projection exposes the folder in SessionSummary.Cwd.
	sums, err := store.Sessions(ctx)
	if err != nil {
		t.Fatalf("store.Sessions() = %v, expected nil", err)
	}
	var summary *session.SessionSummary
	for i := range sums {
		if sums[i].ID == "s1" {
			summary = &sums[i]
		}
	}
	if summary == nil {
		t.Fatalf("store.Sessions() = %v, must include session s1", sums)
	}
	if summary.Cwd != root {
		t.Errorf("SessionSummary.Cwd for s1 = %q, want %q: the sidebar groups chats by folder", summary.Cwd, root)
	}
}

func TestEngine_CapturesSessionCwdOnce(t *testing.T) {
	// TRIANGULATE: Session.Cwd capture is IDEMPOTENT. Two consecutive SendPrompts to the SAME session must leave exactly ONE Session.Cwd event in the log (and first): a capture that would append the folder in each sending would dirty the log and the history projected in each follow-up.
	root := t.TempDir()
	store := session.NewMemoryStore()
	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "hello"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)
	e := New(Config{Root: root, Provider: fake, Store: store})

	for i, prompt := range []string{"first prompt", "second prompt"} {
		if _, err := e.SendPrompt("s1", session.Prompt{Text: prompt}); err != nil {
			t.Fatalf("SendPrompt #%d (s1, %q) = %v, expected nil", i+1, prompt, err)
		}
		if _, done := collectUntilRunDone(t, e.Events(), 10*time.Second, nil); done.Err != "" {
			t.Fatalf("RunDoneMsg.Err #%d = %q, expected a clean run", i+1, done.Err)
		}
	}

	events, err := store.Events(context.Background(), "s1", 0)
	if err != nil {
		t.Fatalf("store.Events(s1) = %v, expected nil", err)
	}
	if len(events) == 0 {
		t.Fatal("store.Events(s1) has no events: runs must persist the log")
	}
	var cwdSeqs []session.Seq
	for _, ev := range events {
		if ev.Kind == session.KindSessionCwd {
			cwdSeqs = append(cwdSeqs, ev.Seq)
		}
	}
	if len(cwdSeqs) != 1 {
		t.Fatalf("the log has %d %s events (Seqs %v), want exactly 1: capturing the directory must be idempotent across sends", len(cwdSeqs), session.KindSessionCwd, cwdSeqs)
	}
	if first := events[0]; first.Kind != session.KindSessionCwd || first.Text != root {
		t.Errorf("first log event = {Kind:%q Text:%q}, want {Kind:%q Text:%q}: the only Session.Cwd must be first", first.Kind, first.Text, session.KindSessionCwd, root)
	}
}

func TestEngine_SendPromptNewCreatesFreshDurableSession(t *testing.T) {
	// /new is a TUI reserved command: upon receiving it, the Engine must open another durable session instead of treating it as a prompt for the current session or resolving it as a skill.
	root := t.TempDir()
	store := session.NewMemoryStore()
	if _, err := store.AppendEvent(context.Background(), "s1", session.SessionEvent{
		Kind: session.KindSessionCwd,
		Text: root,
	}); err != nil {
		t.Fatalf("store.AppendEvent(s1, Session.Cwd) = %v, expected nil", err)
	}
	e := New(Config{
		Root:     root,
		Provider: llm.NewFakeProvider(llm.Event{Kind: llm.StepEnded}),
		Store:    store,
	})

	if _, err := e.SendPrompt("s1", session.Prompt{Text: "/new"}); err != nil {
		t.Fatalf("SendPrompt(s1, /new) = %v, expected nil", err)
	}

	sessions, err := store.Sessions(context.Background())
	if err != nil {
		t.Fatalf("store.Sessions() = %v, expected nil", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("store.Sessions() contains %d sessions, expected 2: /new must open a new durable session without sending the command to s1", len(sessions))
	}
}

func TestEngine_SendPromptNewWithArgumentsRemainsRegularPrompt(t *testing.T) {
	// TRIANGULATE: only the exact /new literal is reserved. With arguments, the text retains the normal slash-command/prompt path and does not open another durable session.
	root := t.TempDir()
	store := session.NewMemoryStore()
	e := New(Config{
		Root:     root,
		Provider: llm.NewFakeProvider(llm.Event{Kind: llm.StepEnded}),
		Store:    store,
	})

	if _, err := e.SendPrompt("s1", session.Prompt{Text: "/new algo"}); err != nil {
		t.Fatalf("SendPrompt(s1, /new algo) = %v, expected nil", err)
	}
	_, done := collectUntilRunDone(t, e.Events(), 10*time.Second, nil)
	if done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q, want a clean run", done.Err)
	}

	sessions, err := store.Sessions(context.Background())
	if err != nil {
		t.Fatalf("store.Sessions() = %v, expected nil", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "s1" {
		t.Fatalf("store.Sessions() = %+v, expected only the original s1 session", sessions)
	}
	messages, err := store.Messages(context.Background(), "s1", 0)
	if err != nil {
		t.Fatalf("store.Messages(s1, 0) = %v, expected nil", err)
	}
	if len(messages) != 1 || messages[0].Text != "/new algo" {
		t.Fatalf("messages for s1 = %+v, expected the literal /new algo prompt", messages)
	}
}

func TestEngine_UndoRestoresPrePromptWorkspaceAndEffectiveConversation(t *testing.T) {
	root := newUndoWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runUndoGit(t, root, "add", "tracked.txt")
	runUndoGit(t, root, "commit", "-m", "base")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("preexisting-change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("preexisting-untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	hash := hashline.ComputeFileHash("preexisting-change\n")
	provider := newTurnProvider(
		[]llm.Event{{Kind: llm.StepStarted}, {Kind: llm.ToolCall, CallID: "read-1", ToolName: "read", Input: json.RawMessage(`{"path":"tracked.txt"}`)}, {Kind: llm.StepEnded}},
		[]llm.Event{{Kind: llm.StepStarted}, {Kind: llm.ToolCall, CallID: "edit-1", ToolName: "edit", Input: json.RawMessage(`{"patch":"[tracked.txt#` + hash + `]\nSWAP 1.=1:\n+agent-change"}`)}, {Kind: llm.StepEnded}},
		[]llm.Event{{Kind: llm.StepStarted}, {Kind: llm.ToolCall, CallID: "write-1", ToolName: "write", Input: json.RawMessage(`{"path":"created.txt","content":"created by agent\n"}`)}, {Kind: llm.StepEnded}},
		[]llm.Event{{Kind: llm.StepStarted}, {Kind: llm.StepEnded}},
	)
	store := session.NewMemoryStore()
	engine := New(Config{
		Root:        root,
		Provider:    provider,
		Store:       store,
		Checkpoints: checkpoint.NewGitStore(t.TempDir()),
	})

	if _, err := engine.SendPrompt("s1", session.Prompt{Text: "change the files"}); err != nil {
		t.Fatal(err)
	}
	if _, done := collectUntilRunDone(t, engine.Events(), 10*time.Second, approveAllPermissions(t, engine)); done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q", done.Err)
	}

	result, err := engine.Undo("s1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Prompt.Text != "change the files" {
		t.Fatalf("Prompt = %q", result.Prompt.Text)
	}
	assertUndoFile(t, root, "tracked.txt", "preexisting-change\n")
	assertUndoFile(t, root, "notes.txt", "preexisting-untracked\n")
	assertUndoMissing(t, root, "created.txt")

	messages, err := store.Messages(context.Background(), "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("effective messages = %+v, want none", messages)
	}
}

func TestEngine_UndoFirstPromptPreservesSessionWorkspace(t *testing.T) {
	root := newUndoWorkspace(t)
	store := session.NewMemoryStore()
	engine := newWritingUndoEngine(t, root, store, t.TempDir())

	if _, err := engine.SendPrompt("s1", session.Prompt{Text: "create file"}); err != nil {
		t.Fatal(err)
	}
	if _, done := collectUntilRunDone(t, engine.Events(), 10*time.Second, approveAllPermissions(t, engine)); done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q", done.Err)
	}
	if _, err := engine.Undo("s1"); err != nil {
		t.Fatal(err)
	}

	sessions, err := store.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "s1" || sessions[0].Cwd != root {
		t.Fatalf("Sessions = %+v, want session s1 with cwd %q", sessions, root)
	}
}

func TestEngine_SendPromptSnapshotFailureDoesNotCreateSession(t *testing.T) {
	store := session.NewMemoryStore()
	wantErr := errors.New("snapshot unavailable")
	engine := New(Config{
		Root:        newUndoWorkspace(t),
		Store:       store,
		Checkpoints: failingCheckpointStore{err: wantErr},
	})

	if _, err := engine.SendPrompt("s1", session.Prompt{Text: "hello"}); !errors.Is(err, wantErr) {
		t.Fatalf("SendPrompt error = %v, want %v", err, wantErr)
	}
	if sessions, err := store.Sessions(context.Background()); err != nil || len(sessions) != 0 {
		t.Fatalf("Sessions = %+v, err = %v, want none", sessions, err)
	}
}

func TestEngine_UndoRejectsCheckpointFromAnotherWorkspace(t *testing.T) {
	firstRoot := newUndoWorkspace(t)
	secondRoot := newUndoWorkspace(t)
	store := session.NewMemoryStore()
	checkpointRoot := t.TempDir()
	firstEngine := newWritingUndoEngine(t, firstRoot, store, checkpointRoot)

	if _, err := firstEngine.SendPrompt("s1", session.Prompt{Text: "create file"}); err != nil {
		t.Fatal(err)
	}
	if _, done := collectUntilRunDone(t, firstEngine.Events(), 10*time.Second, approveAllPermissions(t, firstEngine)); done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q", done.Err)
	}

	secondEngine := New(Config{
		Root:        secondRoot,
		Store:       store,
		Checkpoints: checkpoint.NewGitStore(checkpointRoot),
	})
	if _, err := secondEngine.Undo("s1"); err == nil {
		t.Fatal("Undo accepted a checkpoint created for another workspace")
	}
	if _, err := os.Stat(filepath.Join(firstRoot, "created.txt")); err != nil {
		t.Fatalf("first workspace changed after rejected undo: %v", err)
	}
	if entries, err := os.ReadDir(secondRoot); err != nil || len(entries) != 1 || entries[0].Name() != ".git" {
		t.Fatalf("second workspace changed after rejected undo: entries=%v err=%v", entries, err)
	}
}

func TestEngine_UndoTwiceRestoresEachPromptBoundary(t *testing.T) {
	root := newUndoWorkspace(t)
	provider := newTurnProvider(
		[]llm.Event{{Kind: llm.StepStarted}, {Kind: llm.ToolCall, CallID: "write-1", ToolName: "write", Input: json.RawMessage(`{"path":"first.txt","content":"first\n"}`)}, {Kind: llm.StepEnded}},
		[]llm.Event{{Kind: llm.StepStarted}, {Kind: llm.StepEnded}},
		[]llm.Event{{Kind: llm.StepStarted}, {Kind: llm.ToolCall, CallID: "write-2", ToolName: "write", Input: json.RawMessage(`{"path":"second.txt","content":"second\n"}`)}, {Kind: llm.StepEnded}},
		[]llm.Event{{Kind: llm.StepStarted}, {Kind: llm.StepEnded}},
	)
	store := session.NewMemoryStore()
	engine := New(Config{Root: root, Provider: provider, Store: store, Checkpoints: checkpoint.NewGitStore(t.TempDir())})
	for _, prompt := range []string{"first prompt", "second prompt"} {
		if _, err := engine.SendPrompt("s1", session.Prompt{Text: prompt}); err != nil {
			t.Fatal(err)
		}
		if _, done := collectUntilRunDone(t, engine.Events(), 10*time.Second, approveAllPermissions(t, engine)); done.Err != "" {
			t.Fatal(done.Err)
		}
	}

	result, err := engine.Undo("s1")
	if err != nil || result.Prompt.Text != "second prompt" {
		t.Fatalf("first undo = %+v, err = %v", result, err)
	}
	assertUndoFile(t, root, "first.txt", "first\n")
	assertUndoMissing(t, root, "second.txt")
	result, err = engine.Undo("s1")
	if err != nil || result.Prompt.Text != "first prompt" {
		t.Fatalf("second undo = %+v, err = %v", result, err)
	}
	assertUndoMissing(t, root, "first.txt")
	if messages, err := store.Messages(context.Background(), "s1", 0); err != nil || len(messages) != 0 {
		t.Fatalf("Messages = %+v, err = %v", messages, err)
	}
}

func TestEngine_UndoRejectsWorkspaceDivergence(t *testing.T) {
	root := newUndoWorkspace(t)
	engine := newWritingUndoEngine(t, root, session.NewMemoryStore(), t.TempDir())
	if _, err := engine.SendPrompt("s1", session.Prompt{Text: "create file"}); err != nil {
		t.Fatal(err)
	}
	collectUntilRunDone(t, engine.Events(), 10*time.Second, approveAllPermissions(t, engine))
	if err := os.WriteFile(filepath.Join(root, "outside.txt"), []byte("user change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Undo("s1"); !errors.Is(err, ErrWorkspaceDiverged) {
		t.Fatalf("Undo error = %v", err)
	}
	assertUndoFile(t, root, "created.txt", "created\n")
	assertUndoFile(t, root, "outside.txt", "user change\n")
}

func TestEngine_UndoIgnoresIgnoredFileDivergence(t *testing.T) {
	root := newUndoWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runUndoGit(t, root, "add", ".gitignore")
	runUndoGit(t, root, "commit", "-m", "ignore")
	engine := newWritingUndoEngine(t, root, session.NewMemoryStore(), t.TempDir())
	if _, err := engine.SendPrompt("s1", session.Prompt{Text: "create file"}); err != nil {
		t.Fatal(err)
	}
	collectUntilRunDone(t, engine.Events(), 10*time.Second, approveAllPermissions(t, engine))
	if err := os.WriteFile(filepath.Join(root, "ignored.txt"), []byte("preserve me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Undo("s1"); err != nil {
		t.Fatal(err)
	}
	assertUndoMissing(t, root, "created.txt")
	assertUndoFile(t, root, "ignored.txt", "preserve me\n")
}

func TestEngine_UndoCancelsActiveRunBeforeRestore(t *testing.T) {
	root := newUndoWorkspace(t)
	provider := &blockingAfterToolProvider{started: make(chan struct{}), canceled: make(chan struct{})}
	engine := New(Config{Root: root, Provider: provider, Store: session.NewMemoryStore(), Checkpoints: checkpoint.NewGitStore(t.TempDir())})
	if _, err := engine.SendPrompt("s1", session.Prompt{Text: "create file"}); err != nil {
		t.Fatal(err)
	}
	// The write is gated: approve it in the background so the run reaches the
	// blocking turn.
	t.Cleanup(resolveUntilStopped(engine, "s1", "write-1", permission.AllowedOnce))
	select {
	case <-provider.started:
	case <-time.After(10 * time.Second):
		t.Fatal("provider did not block on second turn")
	}
	if _, err := engine.Undo("s1"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.canceled:
	case <-time.After(time.Second):
		t.Fatal("provider context was not canceled")
	}
	assertUndoMissing(t, root, "created.txt")
}

func TestEngine_UndoPersistsAcrossSQLiteReopen(t *testing.T) {
	root := newUndoWorkspace(t)
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	checkpointRoot := t.TempDir()
	store, err := session.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	engine := newWritingUndoEngine(t, root, store, checkpointRoot)
	if _, err := engine.SendPrompt("s1", session.Prompt{Text: "create file"}); err != nil {
		t.Fatal(err)
	}
	collectUntilRunDone(t, engine.Events(), 10*time.Second, approveAllPermissions(t, engine))
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = session.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	engine = New(Config{Root: root, Provider: llm.NewFakeProvider(), Store: store, Checkpoints: checkpoint.NewGitStore(checkpointRoot)})
	if _, err := engine.Undo("s1"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = session.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if messages, err := store.Messages(context.Background(), "s1", 0); err != nil || len(messages) != 0 {
		t.Fatalf("Messages = %+v, err = %v", messages, err)
	}
	if events, err := store.Events(context.Background(), "s1", 0); err != nil || len(events) != 2 ||
		events[0].Kind != session.KindSessionCwd || events[0].Text != root ||
		events[1].Kind != session.KindSessionMode || events[1].Text != string(session.ModeNormal) {
		t.Fatalf("Events = %+v, err = %v, want Session.Cwd %q then Session.Mode %q", events, err, root, session.ModeNormal)
	}
	if contextResult, err := store.ContextForRunner(context.Background(), "s1"); err != nil || len(contextResult.Messages) != 0 {
		t.Fatalf("ContextForRunner = %+v, err = %v", contextResult, err)
	}
	if _, err := store.LatestPromptCheckpoint(context.Background(), "s1"); !errors.Is(err, session.ErrNothingToUndo) {
		t.Fatalf("LatestPromptCheckpoint error = %v", err)
	}
}

func newUndoWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runUndoGit(t, root, "init")
	runUndoGit(t, root, "config", "user.name", "Atenea Test")
	runUndoGit(t, root, "config", "user.email", "atenea@example.test")
	return root
}

func runUndoGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func newWritingUndoEngine(t *testing.T, root string, store session.Store, checkpointRoot string) *Engine {
	t.Helper()
	provider := newTurnProvider(
		[]llm.Event{{Kind: llm.StepStarted}, {Kind: llm.ToolCall, CallID: "write-1", ToolName: "write", Input: json.RawMessage(`{"path":"created.txt","content":"created\n"}`)}, {Kind: llm.StepEnded}},
		[]llm.Event{{Kind: llm.StepStarted}, {Kind: llm.StepEnded}},
	)
	return New(Config{Root: root, Provider: provider, Store: store, Checkpoints: checkpoint.NewGitStore(checkpointRoot)})
}

func assertUndoFile(t *testing.T, root, name, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", name, got, want)
	}
}

func assertUndoMissing(t *testing.T, root, name string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
		t.Fatalf("%s still exists or stat failed: %v", name, err)
	}
}

func TestEngine_MCPServersReadsWorkspaceConfig(t *testing.T) {
	// Isolates the global config (~/.config/atenea/mcp.json) from the machine environment.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	config := `{"mcpServers": {"github": {"command": "npx", "args": ["github-mcp"]}}}`
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	engine := New(Config{Root: root, Provider: llm.NewFakeProvider(), Store: session.NewMemoryStore()})

	servers, err := engine.MCPServers()
	if err != nil {
		t.Fatalf("MCPServers: %v", err)
	}
	if len(servers) != 1 || servers[0].Name != "github" || servers[0].Connected {
		t.Fatalf("servers = %+v, want github listed disconnected", servers)
	}
	if err := engine.ConnectMCPServer("missing"); err == nil {
		t.Fatal("connecting an undeclared server must fail")
	}
	// Disconnecting a server that is not connected is idempotent, like the manager.
	if err := engine.DisconnectMCPServer("github"); err != nil {
		t.Fatalf("DisconnectMCPServer: %v", err)
	}
}

// connectModelService is a minimal ModelService that also implements
// ConnectService, to verify the engine delegates /connect to it.
type connectModelService struct {
	connectable []providerconfig.ConnectableProvider
	connects    []struct{ providerID, key string }
	logins      []string
	cancels     []struct {
		providerID string
		attempt    uint64
	}
	active providerconfig.Active
}

func (s *connectModelService) Active() providerconfig.Active            { return s.active }
func (s *connectModelService) Catalog() []providerconfig.ProviderModels { return nil }
func (s *connectModelService) Refresh(context.Context) ([]providerconfig.ProviderModels, error) {
	return nil, nil
}
func (s *connectModelService) Select(_ context.Context, providerID, model string) (providerconfig.Active, error) {
	return s.active, nil
}
func (s *connectModelService) Connectable() []providerconfig.ConnectableProvider {
	return s.connectable
}
func (s *connectModelService) Connect(_ context.Context, providerID, apiKey string) (providerconfig.Active, error) {
	s.connects = append(s.connects, struct{ providerID, key string }{providerID, apiKey})
	return s.active, nil
}
func (s *connectModelService) StartDeviceLogin(_ context.Context, providerID string) (providerconfig.DeviceLogin, error) {
	s.logins = append(s.logins, providerID)
	return providerconfig.DeviceLogin{ProviderID: providerID, UserCode: "V3H5-1MW96", Attempt: uint64(len(s.logins))}, nil
}
func (s *connectModelService) AwaitDeviceLogin(_ context.Context, providerID string) (providerconfig.Active, error) {
	return s.active, nil
}
func (s *connectModelService) CancelDeviceLoginAttempt(providerID string, attempt uint64) {
	s.cancels = append(s.cancels, struct {
		providerID string
		attempt    uint64
	}{providerID, attempt})
}

func TestEngine_ConnectProviderDelegatesToConnectService(t *testing.T) {
	service := &connectModelService{
		connectable: []providerconfig.ConnectableProvider{{ID: "openrouter", Name: "OpenRouter"}},
		active:      providerconfig.Active{ProviderID: "openrouter", Model: "openrouter/free"},
	}
	engine := New(Config{Root: t.TempDir(), Provider: llm.NewFakeProvider(), Store: session.NewMemoryStore(), Models: service})
	defer engine.Shutdown(context.Background())

	if got := engine.ConnectableProviders(); len(got) != 1 || got[0].ID != "openrouter" {
		t.Fatalf("ConnectableProviders = %#v", got)
	}
	active, err := engine.ConnectProvider("openrouter", "sk-or-key")
	if err != nil || active.Model != "openrouter/free" {
		t.Fatalf("ConnectProvider = %#v err=%v", active, err)
	}
	if len(service.connects) != 1 || service.connects[0].key != "sk-or-key" {
		t.Fatalf("connects = %#v", service.connects)
	}
}

// TestEngine_DeviceLoginDelegatesToConnectService: the login half of /connect goes
// through the same optional surface, so a provider connected by logging in is
// drivable from the terminal exactly as far as one connected by key.
func TestEngine_DeviceLoginDelegatesToConnectService(t *testing.T) {
	service := &connectModelService{
		connectable: []providerconfig.ConnectableProvider{{ID: "openai-codex", Name: "OpenAI (ChatGPT subscription)", Kind: providerconfig.ConnectDeviceCode}},
		active:      providerconfig.Active{ProviderID: "openai-codex", Model: "gpt-5.5"},
	}
	engine := New(Config{Root: t.TempDir(), Provider: llm.NewFakeProvider(), Store: session.NewMemoryStore(), Models: service})
	defer engine.Shutdown(context.Background())

	login, err := engine.StartDeviceLogin("openai-codex")
	if err != nil || login.UserCode != "V3H5-1MW96" {
		t.Fatalf("StartDeviceLogin = %#v err=%v", login, err)
	}
	active, err := engine.AwaitDeviceLogin("openai-codex")
	if err != nil || active.Model != "gpt-5.5" {
		t.Fatalf("AwaitDeviceLogin = %#v err=%v", active, err)
	}
	// The attempt handle travels with the cancellation, so the service can tell the
	// login the panel started from whichever one is pending by the time it lands.
	engine.CancelDeviceLogin("openai-codex", login.Attempt)
	if len(service.logins) != 1 || len(service.cancels) != 1 {
		t.Fatalf("logins = %#v cancels = %#v", service.logins, service.cancels)
	}
	if service.cancels[0].providerID != "openai-codex" || service.cancels[0].attempt != login.Attempt {
		t.Fatalf("cancels = %#v, want the attempt this panel started", service.cancels)
	}
}

func TestEngine_ConnectUnavailableWithoutConnectService(t *testing.T) {
	engine := New(Config{Root: t.TempDir(), Provider: llm.NewFakeProvider(), Store: session.NewMemoryStore()})
	defer engine.Shutdown(context.Background())

	if got := engine.ConnectableProviders(); got != nil {
		t.Fatalf("ConnectableProviders = %#v, want nil", got)
	}
	if _, err := engine.ConnectProvider("openrouter", "sk"); err == nil {
		t.Fatal("expected an error without a connect-capable model service")
	}
	if _, err := engine.StartDeviceLogin("openai-codex"); err == nil {
		t.Fatal("expected an error without a connect-capable model service")
	}
	if _, err := engine.AwaitDeviceLogin("openai-codex"); err == nil {
		t.Fatal("expected an error without a connect-capable model service")
	}
	// Cancelling without a service is a no-op rather than a panic: the panel
	// cancels on its way out whatever the host can do.
	engine.CancelDeviceLogin("openai-codex", 1)
}

// TestEngine_RequestCompactionDuringShutdownReleasesCompactingSlot pins the
// startCompaction shutdown branch: it must release the compacting slot claimed
// in requestCompaction. Otherwise the key leaks and every later compaction
// request for that session is a silent no-op against the pending/compacting
// guard.
func TestEngine_RequestCompactionDuringShutdownReleasesCompactingSlot(t *testing.T) {
	e := New(Config{Root: t.TempDir(), Provider: llm.NewFakeProvider(), Store: session.NewMemoryStore()})

	e.lifecycleMu.Lock()
	e.shuttingDown = true
	e.lifecycleMu.Unlock()

	e.requestCompaction("s1")

	e.mu.Lock()
	leaked := e.compacting["s1"]
	e.mu.Unlock()
	if leaked {
		t.Fatal("requestCompaction during shutdown left the compacting slot set; later requests would be silent no-ops")
	}
}

// selectionModelService is a ModelService whose selection the test moves, to
// exercise what the engine asks it per turn rather than at assembly time.
type selectionModelService struct {
	mu          sync.Mutex
	active      providerconfig.Active
	effort      llm.ReasoningEffort
	prefError   error
	selectError error
}

func (s *selectionModelService) Active() providerconfig.Active {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}
func (s *selectionModelService) set(active providerconfig.Active) {
	s.mu.Lock()
	s.active = active
	s.mu.Unlock()
}
func (s *selectionModelService) Catalog() []providerconfig.ProviderModels { return nil }
func (s *selectionModelService) Refresh(context.Context) ([]providerconfig.ProviderModels, error) {
	return nil, nil
}
func (s *selectionModelService) Select(_ context.Context, providerID, model string) (providerconfig.Active, error) {
	if s.selectError != nil {
		return s.Active(), s.selectError
	}
	return s.Active(), nil
}
func (s *selectionModelService) ReasoningEffort() llm.ReasoningEffort {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.effort
}
func (s *selectionModelService) SetReasoningEffort(effort llm.ReasoningEffort) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.prefError != nil {
		return s.prefError
	}
	s.effort = effort
	return nil
}

func TestEngine_ReasoningEffortLoadsPersistsAndResetsForSelectedModel(t *testing.T) {
	service := &selectionModelService{effort: llm.ReasoningEffortHigh}
	engine := New(Config{Root: t.TempDir(), Provider: llm.NewFakeProvider(), Store: session.NewMemoryStore(), Models: service})
	defer engine.Shutdown(context.Background())
	if got := engine.ReasoningEffort(); got != llm.ReasoningEffortHigh {
		t.Fatalf("initial reasoning effort = %q, want high", got)
	}
	if err := engine.SetReasoningEffort(llm.ReasoningEffortLow); err != nil {
		t.Fatal(err)
	}
	if got := service.ReasoningEffort(); got != llm.ReasoningEffortLow {
		t.Fatalf("persisted reasoning effort = %q, want low", got)
	}
	if _, err := engine.SelectModel("p", "m"); err != nil {
		t.Fatal(err)
	}
	if got := engine.ReasoningEffort(); got != "" {
		t.Fatalf("reasoning effort after selecting model = %q, want provider default", got)
	}
}

func TestEngine_ReasoningPersistenceFailureKeepsActivePreference(t *testing.T) {
	service := &selectionModelService{effort: llm.ReasoningEffortHigh, prefError: errors.New("disk full")}
	engine := New(Config{Root: t.TempDir(), Provider: llm.NewFakeProvider(), Store: session.NewMemoryStore(), Models: service})
	defer engine.Shutdown(context.Background())
	if err := engine.SetReasoningEffort(llm.ReasoningEffortLow); err == nil {
		t.Fatal("expected persistence error")
	}
	if got := engine.ReasoningEffort(); got != llm.ReasoningEffortHigh {
		t.Fatalf("reasoning effort = %q, want previous high", got)
	}
}

func TestEngine_ModelSelectionFailureKeepsActiveReasoningPreference(t *testing.T) {
	service := &selectionModelService{effort: llm.ReasoningEffortHigh, selectError: errors.New("disk full")}
	engine := New(Config{Root: t.TempDir(), Provider: llm.NewFakeProvider(), Store: session.NewMemoryStore(), Models: service})
	defer engine.Shutdown(context.Background())
	if _, err := engine.SelectModel("p", "m"); err == nil {
		t.Fatal("expected selection error")
	}
	if got := engine.ReasoningEffort(); got != llm.ReasoningEffortHigh {
		t.Fatalf("reasoning effort = %q, want previous high", got)
	}
}

// systemRecordingProvider captures the system prompt of every turn it serves.
type systemRecordingProvider struct {
	mu      sync.Mutex
	systems []string
}

func (p *systemRecordingProvider) Stream(_ context.Context, req llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	p.systems = append(p.systems, req.System)
	p.mu.Unlock()
	out := make(chan llm.Event, 2)
	out <- llm.Event{Kind: llm.StepStarted}
	out <- llm.Event{Kind: llm.StepEnded}
	close(out)
	return out, nil
}

func (p *systemRecordingProvider) captured() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.systems...)
}

// TestEngine_LocalEndpointSelectionSwitchesTheSystemPrompt: an endpoint serving
// models on this machine gets the prompt that spells out the tool-calling protocol,
// because a local model id (qwen2.5-coder) carries no family to route on. The TUI
// could not reach that prompt at all before the selection started declaring it, and
// the question is asked per turn: switching back to a cloud model has to switch the
// prompt back without anything being re-assembled.
func TestEngine_LocalEndpointSelectionSwitchesTheSystemPrompt(t *testing.T) {
	service := &selectionModelService{active: providerconfig.Active{
		ProviderID: "lm-studio", ProviderName: "LM Studio", Model: "qwen2.5-coder", LocalModels: true,
	}}
	provider := &systemRecordingProvider{}
	engine := New(Config{Root: t.TempDir(), Provider: provider, Store: session.NewMemoryStore(), Models: service})
	defer engine.Shutdown(context.Background())

	if _, err := engine.SendPrompt("s1", session.Prompt{Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	collectUntilRunDone(t, engine.Events(), 10*time.Second, nil)

	// "# How you act" is local.txt's own heading; the other two variants do not
	// carry it. Matching on "function-calling" would prove nothing — every variant
	// tells the model to emit real tool calls.
	systems := provider.captured()
	if len(systems) != 1 || !strings.Contains(systems[0], "# How you act") {
		t.Fatalf("system prompt of the local turn = %q, want the local variant", systems)
	}

	service.set(providerconfig.Active{ProviderID: "anthropic", ProviderName: "Anthropic", Model: "claude-opus-4-8"})
	if _, err := engine.SendPrompt("s1", session.Prompt{Text: "otra"}); err != nil {
		t.Fatal(err)
	}
	collectUntilRunDone(t, engine.Events(), 10*time.Second, nil)

	systems = provider.captured()
	if len(systems) != 2 || strings.Contains(systems[1], "# How you act") {
		t.Fatalf("system prompt after switching to a cloud model = %q, want the family-routed prompt", systems)
	}
}

func TestEngine_CheckpointAndRewindRestoreWorkspaceAndPruneConversation(t *testing.T) {
	root := newUndoWorkspace(t)
	provider := newTurnProvider(
		[]llm.Event{{Kind: llm.StepStarted}, {Kind: llm.ToolCall, CallID: "write-1", ToolName: "write", Input: json.RawMessage(`{"path":"after.txt","content":"after checkpoint\n"}`)}, {Kind: llm.StepEnded}},
		[]llm.Event{{Kind: llm.StepStarted}, {Kind: llm.StepEnded}},
	)
	store := session.NewMemoryStore()
	engine := New(Config{Root: root, Provider: provider, Store: store, Checkpoints: checkpoint.NewGitStore(t.TempDir())})
	checkpointResult, err := engine.Checkpoint("s1")
	if err != nil || checkpointResult.ID == "" {
		t.Fatalf("Checkpoint = %+v, err = %v", checkpointResult, err)
	}
	if _, err := engine.SendPrompt("s1", session.Prompt{Text: "change after checkpoint"}); err != nil {
		t.Fatal(err)
	}
	if _, done := collectUntilRunDone(t, engine.Events(), 10*time.Second, approveAllPermissions(t, engine)); done.Err != "" {
		t.Fatal(done.Err)
	}
	if _, err := os.Stat(filepath.Join(root, "after.txt")); err != nil {
		t.Fatalf("turn did not change workspace: %v", err)
	}

	rewind, err := engine.Rewind("s1")
	if err != nil || rewind.CheckpointID != checkpointResult.ID {
		t.Fatalf("Rewind = %+v, err = %v", rewind, err)
	}
	assertUndoMissing(t, root, "after.txt")
	messages, err := store.Messages(context.Background(), "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("messages after rewind = %+v, want checkpoint context", messages)
	}
	if _, err := engine.SendPrompt("s1", session.Prompt{Text: "continue from checkpoint"}); err != nil {
		t.Fatal(err)
	}
	if _, done := collectUntilRunDone(t, engine.Events(), 10*time.Second, approveAllPermissions(t, engine)); done.Err != "" {
		t.Fatal(done.Err)
	}
	messages, err = store.Messages(context.Background(), "s1", 0)
	if err != nil || len(messages) == 0 || messages[0].Text != "continue from checkpoint" {
		t.Fatalf("continued messages = %+v, err = %v", messages, err)
	}
}

func TestEngine_ModelToolsCheckpointThenRewindWithoutOrphanedToolCalls(t *testing.T) {
	root := newUndoWorkspace(t)
	provider := newTurnProvider(
		[]llm.Event{{Kind: llm.StepStarted}, {Kind: llm.ToolCall, CallID: "checkpoint-1", ToolName: "checkpoint", Input: json.RawMessage(`{}`)}, {Kind: llm.StepEnded}},
		[]llm.Event{{Kind: llm.StepStarted}, {Kind: llm.StepEnded}},
		[]llm.Event{{Kind: llm.StepStarted}, {Kind: llm.ToolCall, CallID: "write-1", ToolName: "write", Input: json.RawMessage(`{"path":"after.txt","content":"after checkpoint\n"}`)}, {Kind: llm.StepEnded}},
		[]llm.Event{{Kind: llm.StepStarted}, {Kind: llm.StepEnded}},
		[]llm.Event{{Kind: llm.StepStarted}, {Kind: llm.ToolCall, CallID: "rewind-1", ToolName: "rewind", Input: json.RawMessage(`{}`)}, {Kind: llm.StepEnded}},
		[]llm.Event{{Kind: llm.StepStarted}, {Kind: llm.StepEnded}},
	)
	store := session.NewMemoryStore()
	engine := New(Config{Root: root, Provider: provider, Store: store, Checkpoints: checkpoint.NewGitStore(t.TempDir())})
	for _, prompt := range []string{"checkpoint before exploration", "do exploratory work", "rewind exploration"} {
		if _, err := engine.SendPrompt("s1", session.Prompt{Text: prompt}); err != nil {
			t.Fatal(err)
		}
		if _, done := collectUntilRunDone(t, engine.Events(), 10*time.Second, approveAllPermissions(t, engine)); done.Err != "" {
			t.Fatalf("run %q failed: %s", prompt, done.Err)
		}
	}
	assertUndoMissing(t, root, "after.txt")
	messages, err := store.Messages(context.Background(), "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.Role == session.RoleTool && message.ToolCallID == "" {
			t.Fatalf("orphaned tool result after model rewind: %+v", messages)
		}
	}
	if len(messages) < 3 || messages[len(messages)-1].ToolCallID != "checkpoint-1" {
		t.Fatalf("messages after model rewind = %+v, want checkpoint call and settlement preserved", messages)
	}
}

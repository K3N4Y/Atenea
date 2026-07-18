package tui

import (
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

	"atenea/internal/agent"
	"atenea/internal/checkpoint"
	"atenea/internal/llm"
	"atenea/internal/providerconfig"
	"atenea/internal/session"
	"atenea/internal/tool/hashline"
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

// turnProvider implementa llm.Provider con un guion POR TURNO: la i-esima
// llamada a Stream reproduce el i-esimo guion. Contrasta con llm.FakeProvider,
// que repite el mismo guion en cada Stream (loop infinito si el guion pide
// tools). Si los guiones se acaban, emite un turno de solo StepEnded para que
// la corrida cierre limpia. Protegido con mutex: el runner llama Stream desde
// su propia goroutine.
type turnProvider struct {
	mu    sync.Mutex
	turns [][]llm.Event
	next  int
	// toolNames registra, por cada llamada a Stream, los nombres de las tools
	// anunciadas en el Request: la evidencia observable del modo del turno
	// (plan-mode anuncia present_plan y esconde bash/write).
	toolNames [][]string
	// messages registra, por cada llamada a Stream, el historial proyectado que
	// el runner envio al proveedor: la evidencia observable del orden en que los
	// eventos se materializaron como Messages.
	messages [][]llm.Message
	// delayStepEnded, si es > 0, duerme ese lapso entre un ToolCall del guion y
	// el StepEnded que lo sigue: espejo deterministico del ultimo chunk SSE que
	// llega tarde por la red mientras la tool ya se esta asentando localmente.
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

// requestedTools devuelve una copia de los nombres de tools anunciados en cada
// llamada a Stream, en orden de llegada. Con mutex: el runner llama Stream
// desde su propia goroutine.
func (p *turnProvider) requestedTools() [][]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]string(nil), p.toolNames...)
}

// requestedMessages devuelve una copia del historial proyectado enviado en cada
// llamada a Stream, en orden de llegada. Con mutex: el runner llama Stream
// desde su propia goroutine.
func (p *turnProvider) requestedMessages() [][]llm.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]llm.Message(nil), p.messages...)
}

// nextMsg saca el siguiente mensaje del canal del engine, con timeout generoso
// para no ser flaky. Falla el test si el canal se cierra o vence el timeout.
func nextMsg(t *testing.T, ch <-chan tea.Msg, timeout time.Duration) tea.Msg {
	t.Helper()
	select {
	case <-time.After(timeout):
		t.Fatalf("timeout de %v esperando el siguiente mensaje del engine", timeout)
		return nil
	case msg, ok := <-ch:
		if !ok {
			t.Fatal("canal del engine cerrado antes de tiempo")
		}
		return msg
	}
}

// resolveUntilStopped entrega la decision del permiso via el API publico del
// engine, reintentando en segundo plano hasta que el test lo detenga. El
// reintento elimina una carrera real: el runner publica
// Tool.Permission.Requested ANTES de que gate.Ask registre la solicitud, asi
// que una entrega unica podria adelantarse al registro y perderse (el gate
// descarta decisiones sin Ask pendiente). Reintentar es inocuo: la entrega
// efectiva retira la solicitud del gate y los reintentos posteriores son no-op.
func resolveUntilStopped(e *Engine, sessionID, callID string, approved bool) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			e.ResolvePermission(sessionID, callID, approved)
			select {
			case <-done:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	return func() { close(done); wg.Wait() }
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

	engine := NewEngine(EngineConfig{Root: root, Provider: llm.NewFakeProvider(), Store: store})
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
	engine := NewEngine(EngineConfig{Root: root, Provider: llm.NewFakeProvider(), Store: store})
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

	engine := NewEngine(EngineConfig{Root: root, Provider: llm.NewFakeProvider(), Store: store})
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

	engine := NewEngine(EngineConfig{Root: root, Provider: llm.NewFakeProvider(), Store: store})
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

	engine := NewEngine(EngineConfig{Root: rootFile, Provider: llm.NewFakeProvider(), Store: store})
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

	engine := NewEngine(EngineConfig{Root: root, Provider: llm.NewFakeProvider(), Store: store})
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

	engine := NewEngine(EngineConfig{Root: root, Provider: llm.NewFakeProvider(), Store: store})
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

	engine := NewEngine(EngineConfig{Root: root, Provider: llm.NewFakeProvider(), Store: store})
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

	engine := NewEngine(EngineConfig{Root: root, Provider: llm.NewFakeProvider(), Store: store})
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
	for i := 1; i <= historyLimit+2; i++ {
		appendSessionEvent(t, store, "tui-target", session.SessionEvent{Message: &session.Message{
			ID: "u-" + strconv.Itoa(i), Role: session.RoleUser, Text: "legacy-" + strconv.Itoa(i),
		}})
	}

	engine := NewEngine(EngineConfig{Root: root, Provider: llm.NewFakeProvider(), Store: store})
	result, err := engine.ResumeSessionByID("tui-current", "tui-target")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.History) != historyLimit || result.History[0] != "legacy-3" || result.History[len(result.History)-1] != "legacy-102" {
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

	engine := NewEngine(EngineConfig{Root: root, Provider: llm.NewFakeProvider(), Store: store})
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
	engine := NewEngine(EngineConfig{Root: root, Provider: provider, Store: store})
	if _, err := engine.SendPrompt("tui-current", "keep running"); err != nil {
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
	engine := NewEngine(EngineConfig{Root: root, Provider: provider, Store: store})
	if _, err := engine.SendPrompt("tui-target", "keep target running"); err != nil {
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
	engine := NewEngine(EngineConfig{Root: root, Provider: llm.NewFakeProvider(), Store: store})

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
	engine := NewEngine(EngineConfig{Root: root, Provider: llm.NewFakeProvider(), Store: store})
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
		_, err := engine.SendPrompt("tui-target", "wait for resume")
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
	engine := NewEngine(EngineConfig{Root: root, Provider: provider, Store: store})
	oldRun, err := engine.SendPrompt("tui-old", "old conversation prompt")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("old run did not start streaming")
	}

	newRun, err := engine.SendPrompt("tui-old", "/new")
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
	restarted := NewEngine(EngineConfig{Root: root, Provider: llm.NewFakeProvider(), Store: restartedStore})
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
	engine := NewEngine(EngineConfig{
		Root:        t.TempDir(),
		Provider:    llm.NewFakeProvider(),
		Store:       store,
		Checkpoints: fixedCheckpointStore{tree: checkpoint.Tree("before-tree")},
	})

	if _, err := engine.SendPrompt("tui-session", "hola"); !errors.Is(err, modeErr) {
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
		Text: "no debe entrar",
	}); err != nil {
		t.Fatal(err)
	}

	engine := NewEngine(EngineConfig{Root: t.TempDir(), Provider: llm.NewFakeProvider(), Store: store})
	got, err := engine.PromptHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != historyLimit {
		t.Fatalf("len(PromptHistory()) = %d, quiero %d", len(got), historyLimit)
	}
	if got[0] != "literal-3" || got[len(got)-1] != "literal-102" {
		t.Fatalf("PromptHistory() = [%q ... %q], quiero los 100 prompts TUI mas recientes en orden", got[0], got[len(got)-1])
	}
}

func TestEngine_PromptHistoryFallsBackToLegacyUserMessages(t *testing.T) {
	store := session.NewMemoryStore()
	ctx := context.Background()
	for i, text := range []string{"viejo uno", agent.AcceptPlanPrompt, "viejo dos"} {
		if _, err := store.AppendEvent(ctx, "tui-legacy", session.SessionEvent{Message: &session.Message{
			ID:   "m" + strconv.Itoa(i),
			Role: session.RoleUser,
			Text: text,
		}}); err != nil {
			t.Fatal(err)
		}
	}

	engine := NewEngine(EngineConfig{Root: t.TempDir(), Provider: llm.NewFakeProvider(), Store: store})
	got, err := engine.PromptHistory()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"viejo uno", "viejo dos"}
	if !slices.Equal(got, want) {
		t.Fatalf("PromptHistory() = %q, quiero fallback legacy %q sin el prompt interno de AcceptPlan", got, want)
	}
}

func TestEngine_PromptHistoryStopsAfterLatestHundredPrompts(t *testing.T) {
	store := session.NewMemoryStore()
	ctx := context.Background()
	if _, err := store.AppendEvent(ctx, "tui-old", session.SessionEvent{Kind: session.KindComposerPrompt, Text: "too old"}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= historyLimit; i++ {
		if _, err := store.AppendEvent(ctx, "tui-new", session.SessionEvent{
			Kind: session.KindComposerPrompt,
			Text: "latest-" + strconv.Itoa(i),
		}); err != nil {
			t.Fatal(err)
		}
	}

	guarded := &promptHistoryStore{Store: store, blockedSession: "tui-old"}
	engine := NewEngine(EngineConfig{Root: t.TempDir(), Provider: llm.NewFakeProvider(), Store: guarded})
	got, err := engine.PromptHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != historyLimit || got[0] != "latest-1" || got[len(got)-1] != "latest-100" {
		t.Fatalf("PromptHistory() = [%q ... %q] (%d), quiero solo los %d prompts mas recientes", got[0], got[len(got)-1], len(got), historyLimit)
	}
}

func TestEngine_SendPromptContinuesWhenHistoryPersistenceFails(t *testing.T) {
	store := &promptHistoryStore{Store: session.NewMemoryStore(), failComposerPrompt: true}
	engine := NewEngine(EngineConfig{Root: t.TempDir(), Provider: llm.NewFakeProvider(), Store: store})

	if _, err := engine.SendPrompt("tui-session", "hola"); err != nil {
		t.Fatalf("SendPrompt() error = %v, el prompt ya admitido debe ejecutarse aunque falle su historial", err)
	}
	_, done := collectUntilRunDone(t, engine.Events(), 3*time.Second, nil)
	if done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q, se esperaba corrida limpia", done.Err)
	}
}

// gatedBashTurns arma el guion de dos turnos del escenario ask-before-run: el
// turno 1 pide la tool gateada bash con ese comando y el turno 2 responde texto.
func gatedBashTurns(command string) [][]llm.Event {
	input, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		panic(err) // un map[string]string siempre marshalea
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
			{Kind: llm.TextDelta, Text: "listo"},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		},
	}
}

// collectUntilRunDone consume el canal del engine en el goroutine del test:
// acumula los EventMsg hasta ver el RunDoneMsg y los devuelve, tomando cada
// mensaje con nextMsg (que falla si el canal se cierra o vence el timeout).
// onEvent (opcional) se invoca con cada evento al llegar; los tests lo usan
// para reaccionar a mitad de corrida (resolver un permiso, detener la sesion).
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
			t.Fatalf("mensaje inesperado en el canal del engine: %T", m)
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
	e := NewEngine(EngineConfig{Root: t.TempDir(), Provider: provider, Store: store})

	if _, err := e.SendPrompt("s1", "/compact"); err != nil {
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
	e := NewEngine(EngineConfig{Root: t.TempDir(), Provider: provider, Store: session.NewMemoryStore()})
	if _, err := e.SendPrompt("s1", "wait"); err != nil {
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
	e := NewEngine(EngineConfig{Root: root, Provider: provider, Store: store, Checkpoints: checkpoint.NewGitStore(t.TempDir())})
	if _, err := e.SendPrompt("s1", "create file"); err != nil {
		t.Fatal(err)
	}
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
	e := NewEngine(EngineConfig{Root: t.TempDir(), Provider: provider, Store: store})
	if _, err := e.SendPrompt("s1", "/compact"); err != nil {
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
	e := NewEngine(EngineConfig{Root: t.TempDir(), Provider: provider, Store: store})

	if _, err := e.SendPrompt("s1", "continue turn"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("turn did not start")
	}
	for range 2 {
		if _, err := e.SendPrompt("s1", "/compact"); err != nil {
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
	e := NewEngine(EngineConfig{Root: t.TempDir(), Provider: provider, Store: store})
	if _, err := e.SendPrompt("s1", "/compact later"); err != nil {
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
	e := NewEngine(EngineConfig{Root: t.TempDir(), Provider: provider, Store: store})
	if _, err := e.SendPrompt("s1", "continue turn"); err != nil {
		t.Fatal(err)
	}
	<-provider.started
	if _, err := e.SendPrompt("s1", "/compact"); err != nil {
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
	e := NewEngine(EngineConfig{Root: t.TempDir(), Provider: provider, Store: store})

	if _, err := e.SendPrompt("s1", "first"); err != nil {
		t.Fatal(err)
	}
	<-provider.started[0]
	if _, err := e.SendPrompt("s1", "/compact"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SendPrompt("s1", "replacement"); err != nil {
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
	e := NewEngine(EngineConfig{Root: t.TempDir(), Provider: provider, Store: store})
	if _, err := e.SendPrompt("s1", "/compact"); err != nil {
		t.Fatal(err)
	}
	<-provider.started
	promptDone := make(chan error, 1)
	go func() {
		_, err := e.SendPrompt("s1", "next prompt")
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

// lastEvent devuelve el ultimo evento con ese Kind y CallID, o nil si no llego.
func lastEvent(events []session.SessionEvent, kind session.EventKind, callID string) *session.SessionEvent {
	var found *session.SessionEvent
	for i, ev := range events {
		if ev.Kind == kind && ev.CallID == callID {
			found = &events[i]
		}
	}
	return found
}

// writeSkill crea <root>/.atenea/skills/<name>/SKILL.md con el frontmatter
// name/description (mismo formato que los tests de internal/skill): la fuente
// de la que el wiring deriva los slash-commands del composer.
func writeSkill(t *testing.T, root, name, desc string) {
	t.Helper()
	dir := filepath.Join(root, ".atenea", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	front := "---\nname: " + name + "\ndescription: " + desc + "\n---\ncuerpo de " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(front), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

func TestEngine_ExposesCommandsFromSkills(t *testing.T) {
	// El Engine expone los slash-commands derivados de las skills descubiertas
	// (espejo de App.ListCommands): la TUI los cablea al menu "/" del composer.
	// Se asierta CONTENCION, no igualdad: el wiring tambien descubre las skills
	// globales del home del usuario.
	root := t.TempDir()
	writeSkill(t, root, "saluda", "saluda con estilo")

	e := NewEngine(EngineConfig{Root: root, Provider: llm.NewFakeProvider(), Store: session.NewMemoryStore()})

	cmds := e.Commands()
	for _, c := range cmds {
		if c.Name == "saluda" {
			if c.Description != "saluda con estilo" {
				t.Fatalf("Commands() dio saluda con Description = %q, quiero %q", c.Description, "saluda con estilo")
			}
			return
		}
	}
	t.Fatalf("Commands() = %v, debe contener el comando %q derivado de la skill del proyecto", cmds, "saluda")
}

func TestEngine_ProjectFilesListsWorkspace(t *testing.T) {
	// El Engine lista los archivos del workspace (rutas relativas a la raiz)
	// para el @-menu del composer (espejo de App.ListProjectFiles). El glob
	// real usa ripgrep: sin rg instalado el caso se salta.
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skipf("rg unavailable: %v", err)
	}
	root := t.TempDir()
	for _, f := range []string{"a.go", filepath.Join("sub", "b.txt")} {
		path := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte("contenido"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	e := NewEngine(EngineConfig{Root: root, Provider: llm.NewFakeProvider(), Store: session.NewMemoryStore()})

	files, err := e.ProjectFiles()
	if err != nil {
		t.Fatalf("ProjectFiles() = %v, se esperaba nil", err)
	}
	for _, want := range []string{"a.go", filepath.Join("sub", "b.txt")} {
		if !slices.Contains(files, want) {
			t.Fatalf("ProjectFiles() = %v, debe contener la ruta relativa %q", files, want)
		}
	}
}

func TestEngine_SendPromptExpandsSlashCommand(t *testing.T) {
	// SendPrompt expande un slash-command antes de encolarlo (espejo de
	// agent.Service): el Message user promovido lleva el prompt EXPANDIDO
	// de la plantilla de la skill, no el literal "/saluda ...". Un prompt que
	// no es comando pasa sin cambios. Cubre tambien SendPlanPrompt: ambos
	// comparten el camino comun de send.
	root := t.TempDir()
	writeSkill(t, root, "saluda", "saluda con estilo")
	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "hola"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)
	e := NewEngine(EngineConfig{Root: root, Provider: fake, Store: session.NewMemoryStore()})

	// lastUserPrompt corre una corrida completa y devuelve el ultimo Message
	// user promovido entre sus eventos.
	lastUserPrompt := func(sessionID, text string) string {
		t.Helper()
		if _, err := e.SendPrompt(sessionID, text); err != nil {
			t.Fatalf("SendPrompt(%s, %s) = %v, se esperaba nil", sessionID, text, err)
		}
		events, done := collectUntilRunDone(t, e.Events(), 10*time.Second, nil)
		if done.Err != "" {
			t.Fatalf("RunDoneMsg.Err = %q, se esperaba corrida limpia", done.Err)
		}
		prompt := ""
		for _, ev := range events {
			if ev.Message != nil && ev.Message.Role == session.RoleUser {
				prompt = ev.Message.Text
			}
		}
		return prompt
	}

	// La plantilla de FromSkills es `Usa la skill %q.\n\n$ARGUMENTS`.
	want := "Usa la skill \"saluda\".\n\nhola mundo"
	if got := lastUserPrompt("s1", "/saluda hola mundo"); got != want {
		t.Fatalf("Message user promovido = %q, quiero el prompt expandido %q, no el literal del comando", got, want)
	}

	// Un prompt que no es comando pasa sin transformar.
	if got := lastUserPrompt("s2", "hola normal"); got != "hola normal" {
		t.Fatalf("Message user promovido = %q, un prompt que no es comando debe pasar sin cambios (%q)", got, "hola normal")
	}
}

func TestEngine_StreamsSessionEventsAndSignalsRunDone(t *testing.T) {
	// Un turno de solo texto: el guion completo de una corrida limpia.
	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "Hola desde el engine"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)

	e := NewEngine(EngineConfig{Root: t.TempDir(), Provider: fake, Store: session.NewMemoryStore()})

	if _, err := e.SendPrompt("s1", "hola"); err != nil {
		t.Fatalf("SendPrompt(s1, hola) = %v, se esperaba nil", err)
	}

	events, done := collectUntilRunDone(t, e.Events(), 5*time.Second, nil)

	var sawUserPrompt bool // (a) el prompt promovido a mensaje user durable
	var sawTextDelta bool  // (b) al menos un Text.Delta
	var deltas strings.Builder
	var sawStepEnded bool // (c) el cierre del turno
	for _, ev := range events {
		if ev.Message != nil && ev.Message.Role == session.RoleUser && ev.Message.Text == "hola" {
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
		t.Errorf("no llego el mensaje user promovido con Text %q entre %d eventos", "hola", len(events))
	}
	if !sawTextDelta {
		t.Errorf("no llego ningun evento %s", session.KindTextDelta)
	} else if got := deltas.String(); !strings.Contains(got, "Hola desde el engine") {
		t.Errorf("texto acumulado de %s = %q, debe contener %q", session.KindTextDelta, got, "Hola desde el engine")
	}
	if !sawStepEnded {
		t.Errorf("no llego ningun evento %s", session.KindStepEnded)
	}
	if done.Err != "" {
		t.Errorf("RunDoneMsg.Err = %q, se esperaba corrida limpia (Err vacio)", done.Err)
	}
}

func TestEngine_ReplacementRunWaitsForCanceledRunAndKeepsDistinctIdentity(t *testing.T) {
	provider := newDelayedCancellationProvider()
	e := NewEngine(EngineConfig{Root: t.TempDir(), Provider: provider, Store: session.NewMemoryStore()})

	first, err := e.SendPrompt("s1", "primera")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("la primera corrida no inicio")
	}

	second, err := e.SendPrompt("s1", "segunda")
	if err != nil {
		t.Fatal(err)
	}
	if first.RunID == second.RunID {
		t.Fatalf("run IDs = %d y %d, se esperaban identidades distintas", first.RunID, second.RunID)
	}
	select {
	case <-provider.cancelSeen:
	case <-time.After(time.Second):
		t.Fatal("la primera corrida no recibio cancelacion")
	}
	select {
	case <-provider.secondStarted:
		t.Fatal("la corrida nueva inicio antes de que terminara la cancelada")
	case <-time.After(50 * time.Millisecond):
	}

	close(provider.releaseFirst)
	select {
	case <-provider.secondStarted:
	case <-time.After(time.Second):
		t.Fatal("la corrida nueva no inicio tras terminar la cancelada")
	}

	var done []RunDoneMsg
	deadline := time.After(2 * time.Second)
	for len(done) < 2 {
		select {
		case <-deadline:
			t.Fatalf("cierres recibidos = %+v, se esperaban ambas corridas", done)
		case msg := <-e.Events():
			if runDone, ok := msg.(RunDoneMsg); ok {
				done = append(done, runDone)
			}
		}
	}
	if done[0].SessionID != "s1" || done[0].RunID != first.RunID {
		t.Fatalf("primer cierre = %+v, se esperaba session s1 run %d", done[0], first.RunID)
	}
	if done[1].SessionID != "s1" || done[1].RunID != second.RunID {
		t.Fatalf("segundo cierre = %+v, se esperaba session s1 run %d", done[1], second.RunID)
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
			{Kind: llm.TextDelta, Text: "archivos cambiados"},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		},
	)
	store := session.NewMemoryStore()
	engine := NewEngine(EngineConfig{
		Root:        root,
		Provider:    provider,
		Store:       store,
		Checkpoints: checkpoint.NewGitStore(t.TempDir()),
	})

	if _, err := engine.SendPrompt("s1", "cambia los archivos"); err != nil {
		t.Fatal(err)
	}
	events, done := collectUntilRunDone(t, engine.Events(), 10*time.Second, func(ev session.SessionEvent) {
		if ev.Kind == session.KindToolPermissionRequested && ev.CallID == "remove-tracked" {
			t.Cleanup(resolveUntilStopped(engine, ev.SessionID, ev.CallID, true))
		}
	})
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
	if result.Prompt != "cambia los archivos" {
		t.Fatalf("Prompt = %q", result.Prompt)
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
	provider := newTurnProvider(gatedBashTurns("echo hola-gate")...)
	e := NewEngine(EngineConfig{Root: t.TempDir(), Provider: provider, Store: session.NewMemoryStore()})

	if _, err := e.SendPrompt("s1", "corre el comando"); err != nil {
		t.Fatalf("SendPrompt(s1, corre el comando) = %v, se esperaba nil", err)
	}

	// Al ver la solicitud de permiso, el usuario APRUEBA la tool.
	events, done := collectUntilRunDone(t, e.Events(), 10*time.Second, func(ev session.SessionEvent) {
		if ev.Kind == session.KindToolPermissionRequested && ev.CallID == "c1" {
			t.Cleanup(resolveUntilStopped(e, ev.SessionID, "c1", true))
		}
	})

	success := lastEvent(events, session.KindToolSuccess, "c1")
	if success == nil {
		t.Fatalf("no llego ningun %s de c1 entre %d eventos: la tool aprobada debe ejecutarse y asentarse", session.KindToolSuccess, len(events))
	}
	if !strings.Contains(success.Text, "hola-gate") {
		t.Errorf("Tool.Success de c1 con Text = %q, debe contener %q (bash ejecuto de verdad)", success.Text, "hola-gate")
	}
	if done.Err != "" {
		t.Errorf("RunDoneMsg.Err = %q, se esperaba corrida limpia (Err vacio)", done.Err)
	}
}

func TestEngine_GatedBashDeniedFailsWithoutRunning(t *testing.T) {
	root := t.TempDir()
	forbidden := filepath.Join(root, "no-debe-existir")
	provider := newTurnProvider(gatedBashTurns("touch " + forbidden)...)
	e := NewEngine(EngineConfig{Root: root, Provider: provider, Store: session.NewMemoryStore()})

	if _, err := e.SendPrompt("s1", "corre el comando"); err != nil {
		t.Fatalf("SendPrompt(s1, corre el comando) = %v, se esperaba nil", err)
	}

	// Al ver la solicitud de permiso, el usuario DENIEGA la tool.
	events, done := collectUntilRunDone(t, e.Events(), 10*time.Second, func(ev session.SessionEvent) {
		if ev.Kind == session.KindToolPermissionRequested && ev.CallID == "c1" {
			t.Cleanup(resolveUntilStopped(e, ev.SessionID, "c1", false))
		}
	})

	if ev := lastEvent(events, session.KindToolSuccess, "c1"); ev != nil {
		t.Fatalf("llego %s de c1 con Text %q: una tool denegada NO debe ejecutarse", session.KindToolSuccess, ev.Text)
	}
	failed := lastEvent(events, session.KindToolFailed, "c1")
	if failed == nil {
		t.Fatalf("no llego ningun %s de c1 entre %d eventos: la denegacion debe asentar la tool como fallida", session.KindToolFailed, len(events))
	}
	if !strings.Contains(strings.ToLower(failed.Error), "deni") {
		t.Errorf("Tool.Failed de c1 con Error = %q, debe mencionar la denegacion", failed.Error)
	}
	if done.Err != "" {
		t.Errorf("RunDoneMsg.Err = %q, la denegacion no es un fallo de la corrida (Err vacio)", done.Err)
	}
	// La prueba dura de que bash NO corrio: el archivo que el comando tocaria
	// no debe existir tras el fin de la corrida.
	if _, err := os.Stat(forbidden); !os.IsNotExist(err) {
		t.Errorf("os.Stat(%q) = %v, el archivo no debe existir: la tool denegada no debe ejecutar el comando", forbidden, err)
	}
}

func TestEngine_StopUnblocksPendingPermission(t *testing.T) {
	// Un solo turno: la tool gateada queda esperando aprobacion para siempre;
	// Stop debe desbloquearla y cerrar la corrida limpia.
	provider := newTurnProvider([]llm.Event{
		{Kind: llm.StepStarted},
		{Kind: llm.ToolCall, CallID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"echo bloqueado"}`)},
		{Kind: llm.StepEnded},
	})
	e := NewEngine(EngineConfig{Root: t.TempDir(), Provider: provider, Store: session.NewMemoryStore()})

	if _, err := e.SendPrompt("s1", "corre el comando"); err != nil {
		t.Fatalf("SendPrompt(s1, corre el comando) = %v, se esperaba nil", err)
	}

	// En vez de decidir, el usuario detiene la corrida.
	events, done := collectUntilRunDone(t, e.Events(), 10*time.Second, func(ev session.SessionEvent) {
		if ev.Kind == session.KindToolPermissionRequested && ev.CallID == "c1" {
			e.Stop("s1")
		}
	})

	if lastEvent(events, session.KindToolFailed, "c1") == nil {
		t.Errorf("no llego ningun %s de c1: Stop debe asentar la call pendiente como interrumpida", session.KindToolFailed)
	}
	if done.Err != "" {
		t.Errorf("RunDoneMsg.Err = %q, una cancelacion deliberada es cierre limpio (Err vacio)", done.Err)
	}
}

func TestEngine_AcceptPlanRunsImplementationInNormalMode(t *testing.T) {
	// TRIANGULATE: AcceptPlan debe volver la sesion a modo normal y promover el
	// prompt fijo de implementacion como prompt del usuario, arrancando la
	// corrida (espejo de App.AcceptPlan). Evidencia observable: el Request del
	// turno de AcceptPlan vuelve a anunciar bash (modo normal) y entre los
	// eventos llega el Message user con el texto del prompt fijo.
	textTurn := func(text string) []llm.Event {
		return []llm.Event{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: text},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		}
	}
	provider := newTurnProvider(textTurn("plan listo"), textTurn("implementado"))
	e := NewEngine(EngineConfig{Root: t.TempDir(), Provider: provider, Store: session.NewMemoryStore()})

	// Corrida de plan previa: deja la sesion en plan-mode con el plan presentado.
	if _, err := e.SendPlanPrompt("s1", "planea"); err != nil {
		t.Fatalf("SendPlanPrompt(s1, planea) = %v, se esperaba nil", err)
	}
	if _, done := collectUntilRunDone(t, e.Events(), 10*time.Second, nil); done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q, se esperaba corrida limpia en plan-mode", done.Err)
	}
	planCalls := len(provider.requestedTools())

	// El usuario acepta el plan: debe arrancar la corrida de implementacion.
	if _, err := e.AcceptPlan("s1"); err != nil {
		t.Fatalf("AcceptPlan(s1) = %v, se esperaba nil", err)
	}
	events, done := collectUntilRunDone(t, e.Events(), 10*time.Second, nil)
	if done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q, se esperaba corrida limpia al ejecutar el plan", done.Err)
	}

	calls := provider.requestedTools()
	if len(calls) <= planCalls {
		t.Fatalf("el provider registro %d llamadas a Stream tras AcceptPlan (habia %d): aceptar el plan debe arrancar una corrida nueva", len(calls), planCalls)
	}
	acceptTools := calls[len(calls)-1]
	if !slices.Contains(acceptTools, "bash") {
		t.Errorf("tools del turno de AcceptPlan = %v, debe incluir %q: aceptar el plan vuelve la sesion a modo normal", acceptTools, "bash")
	}

	var prompt *session.Message
	for _, ev := range events {
		if ev.Message != nil && ev.Message.Role == session.RoleUser {
			msg := *ev.Message
			prompt = &msg
		}
	}
	if prompt == nil {
		t.Fatalf("no llego ningun Message user entre %d eventos: AcceptPlan debe promover el prompt fijo de implementacion", len(events))
	}
	if !strings.Contains(prompt.Text, "aprobado") {
		t.Errorf("Message user promovido = %q, debe contener %q (el prompt fijo de implementacion)", prompt.Text, "aprobado")
	}
}

func TestEngine_SendPlanPromptRunsInPlanMode(t *testing.T) {
	// TRIANGULATE: SendPlanPrompt debe correr el turno en plan-mode REAL (como
	// en la app Wails), no delegar en SendPrompt. La evidencia observable son
	// las tools que el runner anuncia al modelo en el Request de cada turno:
	// plan-mode anuncia present_plan y esconde bash/write; el modo es por envio,
	// asi que un SendPrompt posterior en la MISMA sesion vuelve a anunciar bash.
	textTurn := func(text string) []llm.Event {
		return []llm.Event{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: text},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		}
	}
	provider := newTurnProvider(textTurn("plan listo"), textTurn("hecho"))
	e := NewEngine(EngineConfig{Root: t.TempDir(), Provider: provider, Store: session.NewMemoryStore()})

	// Envio en plan-mode: el turno debe anunciar las tools de planificacion.
	if _, err := e.SendPlanPrompt("s1", "planea x"); err != nil {
		t.Fatalf("SendPlanPrompt(s1, planea x) = %v, se esperaba nil", err)
	}
	if _, done := collectUntilRunDone(t, e.Events(), 10*time.Second, nil); done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q, se esperaba corrida limpia en plan-mode", done.Err)
	}
	calls := provider.requestedTools()
	if len(calls) == 0 {
		t.Fatalf("el provider no registro ninguna llamada a Stream tras la corrida de plan")
	}
	planTools := calls[len(calls)-1]
	if !slices.Contains(planTools, "present_plan") {
		t.Errorf("tools del turno de plan = %v, debe incluir %q: SendPlanPrompt debe correr en plan-mode real", planTools, "present_plan")
	}
	for _, forbidden := range []string{"bash", "write"} {
		if slices.Contains(planTools, forbidden) {
			t.Errorf("tools del turno de plan = %v, NO debe incluir %q: plan-mode es de solo lectura", planTools, forbidden)
		}
	}

	// Envio normal posterior en la MISMA sesion: el modo es por envio (espejo
	// de la app Wails) y el turno vuelve a anunciar las tools de build.
	if _, err := e.SendPrompt("s1", "hazlo"); err != nil {
		t.Fatalf("SendPrompt(s1, hazlo) = %v, se esperaba nil", err)
	}
	if _, done := collectUntilRunDone(t, e.Events(), 10*time.Second, nil); done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q, se esperaba corrida limpia en modo normal", done.Err)
	}
	calls = provider.requestedTools()
	if len(calls) < 2 {
		t.Fatalf("el provider registro %d llamadas a Stream, se esperaban al menos 2 (turno de plan + turno normal)", len(calls))
	}
	buildTools := calls[len(calls)-1]
	if !slices.Contains(buildTools, "bash") {
		t.Errorf("tools del turno normal = %v, debe incluir %q: el modo es por envio y SendPrompt vuelve a build", buildTools, "bash")
	}
}

func TestEngine_ToolResultNeverPrecedesAssistantMessageInHistory(t *testing.T) {
	// RED (bug real visto con OpenRouter/Cohere): cuando el modelo responde SOLO
	// con una tool call que falla al instante (read con ruta absoluta: muere en
	// la validacion de sandboxJoin, sin I/O), el Tool.Failed (que materializa el
	// Message role=tool) puede persistirse ANTES que el Step.Ended (que
	// materializa el Message assistant con los tool_calls), porque el runner
	// asienta la tool en una goroutine concurrente mientras el StepEnded aun
	// viaja por la red. El historial proyectado queda `user, tool, assistant` y
	// el siguiente request al provider devuelve 400: "tool call id not found in
	// previous tool calls". El delay del provider reproduce esa carrera de forma
	// deterministica: el ultimo chunk SSE (StepEnded) llega ~100ms tarde.
	provider := newTurnProvider(
		[]llm.Event{
			{Kind: llm.StepStarted},
			{Kind: llm.ToolCall, CallID: "c1", ToolName: "read", Input: json.RawMessage(`{"path":"/etc/fuera"}`)},
			{Kind: llm.StepEnded},
		},
		[]llm.Event{
			{Kind: llm.StepStarted},
			{Kind: llm.TextStarted},
			{Kind: llm.TextDelta, Text: "no pude leerlo"},
			{Kind: llm.TextEnded},
			{Kind: llm.StepEnded},
		},
	)
	provider.delayStepEnded = 100 * time.Millisecond
	e := NewEngine(EngineConfig{Root: t.TempDir(), Provider: provider, Store: session.NewMemoryStore()})

	if _, err := e.SendPrompt("s1", "lee eso"); err != nil {
		t.Fatalf("SendPrompt(s1, lee eso) = %v, se esperaba nil", err)
	}
	if _, done := collectUntilRunDone(t, e.Events(), 10*time.Second, nil); done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q, se esperaba corrida limpia (la tool fallida no es fallo de la corrida)", done.Err)
	}

	calls := provider.requestedMessages()
	if len(calls) < 2 {
		t.Fatalf("el provider registro %d llamadas a Stream, se esperaban al menos 2 (turno de la tool + turno de cierre)", len(calls))
	}
	history := calls[1] // el historial proyectado que ve el provider en el turno 2

	// La secuencia de roles proyectada, para un mensaje de fallo legible.
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
		t.Fatalf("el historial del turno 2 no tiene ningun Message assistant con la tool call c1; secuencia de roles proyectada: %v", roles)
	}
	if toolIdx < 0 {
		t.Fatalf("el historial del turno 2 no tiene ningun Message role=tool con ToolCallID c1; secuencia de roles proyectada: %v", roles)
	}
	if toolIdx < assistantIdx {
		t.Fatalf("el Message role=tool de c1 (indice %d) precede al Message assistant con sus tool_calls (indice %d); un provider real lo rechaza con 400 (tool call id not found in previous tool calls); secuencia de roles proyectada: %v", toolIdx, assistantIdx, roles)
	}
}

func TestEngine_CapturesSessionCwdOnFirstPrompt(t *testing.T) {
	// El PRIMER prompt de una sesion (cuando LoadSession aun da error) debe
	// grabar la carpeta de trabajo como un SessionEvent Session.Cwd de PRIMERO
	// en el log, antes de admitir el prompt (espejo de App.captureCwd): asi la
	// sidebar de la app Wails agrupa las sesiones de la TUI por carpeta.
	root := t.TempDir()
	store := session.NewMemoryStore()
	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "hola"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)
	e := NewEngine(EngineConfig{Root: root, Provider: fake, Store: store})

	if _, err := e.SendPrompt("s1", "hola"); err != nil {
		t.Fatalf("SendPrompt(s1, hola) = %v, se esperaba nil", err)
	}
	if _, done := collectUntilRunDone(t, e.Events(), 10*time.Second, nil); done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q, se esperaba corrida limpia", done.Err)
	}

	// (a) El primer evento durable del log (Seq 1) es el Session.Cwd con la raiz.
	ctx := context.Background()
	events, err := store.Events(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("store.Events(s1) = %v, se esperaba nil", err)
	}
	if len(events) == 0 {
		t.Fatal("store.Events(s1) sin eventos: la corrida debe persistir el log")
	}
	first := events[0]
	if first.Seq != 1 || first.Kind != session.KindSessionCwd || first.Text != root {
		t.Errorf("primer evento del log = {Seq:%d Kind:%q Text:%q}, quiero {Seq:1 Kind:%q Text:%q}: la carpeta debe grabarse ANTES de admitir el prompt", first.Seq, first.Kind, first.Text, session.KindSessionCwd, root)
	}

	// (b) La proyeccion Sessions expone la carpeta en SessionSummary.Cwd.
	sums, err := store.Sessions(ctx)
	if err != nil {
		t.Fatalf("store.Sessions() = %v, se esperaba nil", err)
	}
	var summary *session.SessionSummary
	for i := range sums {
		if sums[i].ID == "s1" {
			summary = &sums[i]
		}
	}
	if summary == nil {
		t.Fatalf("store.Sessions() = %v, debe incluir la sesion s1", sums)
	}
	if summary.Cwd != root {
		t.Errorf("SessionSummary.Cwd de s1 = %q, quiero %q: la sidebar agrupa los chats por carpeta", summary.Cwd, root)
	}
}

func TestEngine_CapturesSessionCwdOnce(t *testing.T) {
	// TRIANGULATE: la captura del Session.Cwd es IDEMPOTENTE. Dos SendPrompt
	// consecutivos a la MISMA sesion deben dejar en el log exactamente UN evento
	// Session.Cwd (y de primero): una captura que appendeara la carpeta en cada
	// envio ensuciaria el log y el historial proyectado en cada follow-up.
	root := t.TempDir()
	store := session.NewMemoryStore()
	fake := llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "hola"},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)
	e := NewEngine(EngineConfig{Root: root, Provider: fake, Store: store})

	for i, prompt := range []string{"primer prompt", "segundo prompt"} {
		if _, err := e.SendPrompt("s1", prompt); err != nil {
			t.Fatalf("SendPrompt #%d (s1, %q) = %v, se esperaba nil", i+1, prompt, err)
		}
		if _, done := collectUntilRunDone(t, e.Events(), 10*time.Second, nil); done.Err != "" {
			t.Fatalf("RunDoneMsg.Err #%d = %q, se esperaba corrida limpia", i+1, done.Err)
		}
	}

	events, err := store.Events(context.Background(), "s1", 0)
	if err != nil {
		t.Fatalf("store.Events(s1) = %v, se esperaba nil", err)
	}
	if len(events) == 0 {
		t.Fatal("store.Events(s1) sin eventos: las corridas deben persistir el log")
	}
	var cwdSeqs []session.Seq
	for _, ev := range events {
		if ev.Kind == session.KindSessionCwd {
			cwdSeqs = append(cwdSeqs, ev.Seq)
		}
	}
	if len(cwdSeqs) != 1 {
		t.Fatalf("el log tiene %d eventos %s (Seqs %v), quiero exactamente 1: la captura de la carpeta debe ser idempotente entre envios", len(cwdSeqs), session.KindSessionCwd, cwdSeqs)
	}
	if first := events[0]; first.Kind != session.KindSessionCwd || first.Text != root {
		t.Errorf("primer evento del log = {Kind:%q Text:%q}, quiero {Kind:%q Text:%q}: el unico Session.Cwd debe ser el primero", first.Kind, first.Text, session.KindSessionCwd, root)
	}
}

func TestEngine_SendPromptNewCreatesFreshDurableSession(t *testing.T) {
	// /new es un comando reservado de la TUI: al recibirlo, el Engine debe
	// abrir otra sesion durable en vez de tratarlo como un prompt para la
	// sesion actual o resolverlo como una skill.
	root := t.TempDir()
	store := session.NewMemoryStore()
	if _, err := store.AppendEvent(context.Background(), "s1", session.SessionEvent{
		Kind: session.KindSessionCwd,
		Text: root,
	}); err != nil {
		t.Fatalf("store.AppendEvent(s1, Session.Cwd) = %v, se esperaba nil", err)
	}
	e := NewEngine(EngineConfig{
		Root:     root,
		Provider: llm.NewFakeProvider(llm.Event{Kind: llm.StepEnded}),
		Store:    store,
	})

	if _, err := e.SendPrompt("s1", "/new"); err != nil {
		t.Fatalf("SendPrompt(s1, /new) = %v, se esperaba nil", err)
	}

	sessions, err := store.Sessions(context.Background())
	if err != nil {
		t.Fatalf("store.Sessions() = %v, se esperaba nil", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("store.Sessions() contiene %d sesiones, se esperaban 2: /new debe abrir una sesion durable nueva sin enviar el comando a s1", len(sessions))
	}
}

func TestModel_SubmittingNewActivatesFreshSessionForFuturePrompts(t *testing.T) {
	// TRIANGULATE: crear la fila durable no basta. El composer debe cambiar al
	// ID nuevo para que el siguiente prompt no vuelva a la sesion anterior.
	root := t.TempDir()
	store := session.NewMemoryStore()
	if _, err := store.AppendEvent(context.Background(), "s1", session.SessionEvent{
		Kind: session.KindSessionCwd,
		Text: root,
	}); err != nil {
		t.Fatalf("store.AppendEvent(s1, Session.Cwd) = %v, se esperaba nil", err)
	}
	e := NewEngine(EngineConfig{
		Root:     root,
		Provider: llm.NewFakeProvider(llm.Event{Kind: llm.StepEnded}),
		Store:    store,
	})
	m := NewModel(e, "s1", e.Events())

	m = typeRunes(t, m, "/new")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	sessions, err := store.Sessions(context.Background())
	if err != nil {
		t.Fatalf("store.Sessions() = %v, se esperaba nil", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("store.Sessions() contiene %d sesiones, se esperaban 2", len(sessions))
	}
	newSessionID := ""
	for _, s := range sessions {
		if s.ID != "s1" {
			newSessionID = s.ID
			break
		}
	}
	if newSessionID == "" {
		t.Fatal("no se encontro la sesion creada por /new")
	}
	m = typeRunes(t, m, "continua aqui")
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	_, done := collectUntilRunDone(t, e.Events(), 10*time.Second, nil)
	if done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q, se esperaba corrida limpia", done.Err)
	}
	messages, err := store.Messages(context.Background(), newSessionID, 0)
	if err != nil {
		t.Fatalf("store.Messages(%s, 0) = %v, se esperaba nil", newSessionID, err)
	}
	if len(messages) != 1 || messages[0].Text != "continua aqui" {
		t.Fatalf("mensajes de %s = %+v, se esperaba que el siguiente prompt se enviara a la sesion nueva", newSessionID, messages)
	}
}

func TestEngine_SendPromptNewWithArgumentsRemainsRegularPrompt(t *testing.T) {
	// TRIANGULATE: solo el literal exacto /new esta reservado. Con argumentos,
	// el texto conserva el camino normal de slash-command/prompt y no abre otra
	// sesion durable.
	root := t.TempDir()
	store := session.NewMemoryStore()
	e := NewEngine(EngineConfig{
		Root:     root,
		Provider: llm.NewFakeProvider(llm.Event{Kind: llm.StepEnded}),
		Store:    store,
	})

	if _, err := e.SendPrompt("s1", "/new algo"); err != nil {
		t.Fatalf("SendPrompt(s1, /new algo) = %v, se esperaba nil", err)
	}
	_, done := collectUntilRunDone(t, e.Events(), 10*time.Second, nil)
	if done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q, se esperaba corrida limpia", done.Err)
	}

	sessions, err := store.Sessions(context.Background())
	if err != nil {
		t.Fatalf("store.Sessions() = %v, se esperaba nil", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "s1" {
		t.Fatalf("store.Sessions() = %+v, se esperaba solo la sesion original s1", sessions)
	}
	messages, err := store.Messages(context.Background(), "s1", 0)
	if err != nil {
		t.Fatalf("store.Messages(s1, 0) = %v, se esperaba nil", err)
	}
	if len(messages) != 1 || messages[0].Text != "/new algo" {
		t.Fatalf("mensajes de s1 = %+v, se esperaba el prompt literal /new algo", messages)
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
	engine := NewEngine(EngineConfig{
		Root:        root,
		Provider:    provider,
		Store:       store,
		Checkpoints: checkpoint.NewGitStore(t.TempDir()),
	})

	if _, err := engine.SendPrompt("s1", "cambia los archivos"); err != nil {
		t.Fatal(err)
	}
	if _, done := collectUntilRunDone(t, engine.Events(), 10*time.Second, nil); done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q", done.Err)
	}

	result, err := engine.Undo("s1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Prompt != "cambia los archivos" {
		t.Fatalf("Prompt = %q", result.Prompt)
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

	if _, err := engine.SendPrompt("s1", "create file"); err != nil {
		t.Fatal(err)
	}
	if _, done := collectUntilRunDone(t, engine.Events(), 10*time.Second, nil); done.Err != "" {
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
	engine := NewEngine(EngineConfig{
		Root:        newUndoWorkspace(t),
		Store:       store,
		Checkpoints: failingCheckpointStore{err: wantErr},
	})

	if _, err := engine.SendPrompt("s1", "hello"); !errors.Is(err, wantErr) {
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

	if _, err := firstEngine.SendPrompt("s1", "create file"); err != nil {
		t.Fatal(err)
	}
	if _, done := collectUntilRunDone(t, firstEngine.Events(), 10*time.Second, nil); done.Err != "" {
		t.Fatalf("RunDoneMsg.Err = %q", done.Err)
	}

	secondEngine := NewEngine(EngineConfig{
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
	engine := NewEngine(EngineConfig{Root: root, Provider: provider, Store: store, Checkpoints: checkpoint.NewGitStore(t.TempDir())})
	for _, prompt := range []string{"first prompt", "second prompt"} {
		if _, err := engine.SendPrompt("s1", prompt); err != nil {
			t.Fatal(err)
		}
		if _, done := collectUntilRunDone(t, engine.Events(), 10*time.Second, nil); done.Err != "" {
			t.Fatal(done.Err)
		}
	}

	result, err := engine.Undo("s1")
	if err != nil || result.Prompt != "second prompt" {
		t.Fatalf("first undo = %+v, err = %v", result, err)
	}
	assertUndoFile(t, root, "first.txt", "first\n")
	assertUndoMissing(t, root, "second.txt")
	result, err = engine.Undo("s1")
	if err != nil || result.Prompt != "first prompt" {
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
	if _, err := engine.SendPrompt("s1", "create file"); err != nil {
		t.Fatal(err)
	}
	collectUntilRunDone(t, engine.Events(), 10*time.Second, nil)
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
	if _, err := engine.SendPrompt("s1", "create file"); err != nil {
		t.Fatal(err)
	}
	collectUntilRunDone(t, engine.Events(), 10*time.Second, nil)
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
	engine := NewEngine(EngineConfig{Root: root, Provider: provider, Store: session.NewMemoryStore(), Checkpoints: checkpoint.NewGitStore(t.TempDir())})
	if _, err := engine.SendPrompt("s1", "create file"); err != nil {
		t.Fatal(err)
	}
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
	if _, err := engine.SendPrompt("s1", "create file"); err != nil {
		t.Fatal(err)
	}
	collectUntilRunDone(t, engine.Events(), 10*time.Second, nil)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = session.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	engine = NewEngine(EngineConfig{Root: root, Provider: llm.NewFakeProvider(), Store: store, Checkpoints: checkpoint.NewGitStore(checkpointRoot)})
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
	return NewEngine(EngineConfig{Root: root, Provider: provider, Store: store, Checkpoints: checkpoint.NewGitStore(checkpointRoot)})
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
	// Aisla el config global (~/.config/atenea/mcp.json) del entorno de la maquina.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	config := `{"mcpServers": {"github": {"command": "npx", "args": ["github-mcp"]}}}`
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(EngineConfig{Root: root, Provider: llm.NewFakeProvider(), Store: session.NewMemoryStore()})

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
	// Desconectar un server que no esta conectado es idempotente, como el manager.
	if err := engine.DisconnectMCPServer("github"); err != nil {
		t.Fatalf("DisconnectMCPServer: %v", err)
	}
}

// connectModelService is a minimal ModelService that also implements
// ConnectService, to verify the engine delegates /connect to it.
type connectModelService struct {
	connectable []providerconfig.ConnectableProvider
	connects    []struct{ providerID, key string }
	active      providerconfig.Active
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

func TestEngine_ConnectProviderDelegatesToConnectService(t *testing.T) {
	service := &connectModelService{
		connectable: []providerconfig.ConnectableProvider{{ID: "openrouter", Name: "OpenRouter"}},
		active:      providerconfig.Active{ProviderID: "openrouter", Model: "openrouter/free"},
	}
	engine := NewEngine(EngineConfig{Root: t.TempDir(), Provider: llm.NewFakeProvider(), Store: session.NewMemoryStore(), Models: service})
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

func TestEngine_ConnectUnavailableWithoutConnectService(t *testing.T) {
	engine := NewEngine(EngineConfig{Root: t.TempDir(), Provider: llm.NewFakeProvider(), Store: session.NewMemoryStore()})
	defer engine.Shutdown(context.Background())

	if got := engine.ConnectableProviders(); got != nil {
		t.Fatalf("ConnectableProviders = %#v, want nil", got)
	}
	if _, err := engine.ConnectProvider("openrouter", "sk"); err == nil {
		t.Fatal("expected an error without a connect-capable model service")
	}
}

package learning

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	corellm "github.com/K3N4Y/atenea/agentcore/llm"
	coresession "github.com/K3N4Y/atenea/agentcore/session"
	internalllm "github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/session/prompt"
)

type scripted struct {
	mu        sync.Mutex
	calls     int
	active    int
	maxActive int
	order     []string
	release   <-chan struct{}
	response  string
}

func (p *scripted) Stream(ctx context.Context, req corellm.Request) (<-chan corellm.Event, error) {
	p.mu.Lock()
	p.calls++
	p.order = append(p.order, req.SessionKey)
	p.active++
	if p.active > p.maxActive {
		p.maxActive = p.active
	}
	p.mu.Unlock()
	ch := make(chan corellm.Event, 4)
	go func() {
		defer close(ch)
		defer func() { p.mu.Lock(); p.active--; p.mu.Unlock() }()
		if p.release != nil {
			select {
			case <-ctx.Done():
				return
			case <-p.release:
			}
		}
		ch <- corellm.Event{Kind: corellm.TextDelta, Text: p.response}
		ch <- corellm.Event{Kind: corellm.StepEnded, Usage: &corellm.Usage{InputTokens: 10, OutputTokens: 5}}
	}()
	return ch, nil
}

func TestEnqueueDoesNotBlockPastOldQueueCapacityAndGloballyCapsWorkers(t *testing.T) {
	release := make(chan struct{})
	p := &scripted{release: release, response: `{"type":"no_candidate","reason":"nothing durable"}`}
	switcher, _ := internalllm.NewSwitchableProvider(internalllm.ProviderSnapshot{ProviderID: "p", ProviderName: "P", BaseURL: "local", Model: "m", Provider: p})
	svc := NewService(context.Background(), NewMemoryStore(), durableSession(t), switcher, nil)
	defer svc.Close()
	start := time.Now()
	for i := 0; i < 70; i++ {
		if _, err := svc.Enqueue(context.Background(), fmt.Sprintf("w-%d", i), "s"); err != nil {
			t.Fatal(err)
		}
	}
	if time.Since(start) > time.Second {
		t.Fatalf("enqueue blocked for %s", time.Since(start))
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		calls, max := p.calls, p.maxActive
		p.mu.Unlock()
		if calls >= 2 {
			if max != 2 {
				t.Fatalf("active=%d, want global cap 2", max)
			}
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		calls := p.calls
		p.mu.Unlock()
		if calls == 70 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("queued work did not drain")
}

func TestTransitionMatrix(t *testing.T) {
	valid := map[[2]Status]bool{{Queued, Running}: true, {Queued, Cancelled}: true, {Running, Cancelling}: true, {Running, Ready}: true, {Cancelling, Cancelled}: true, {Ready, Approved}: true, {Ready, Rejected}: true}
	statuses := []Status{Queued, Running, Ready, NoCandidate, Failed, Cancelling, Cancelled, Approved, Rejected, Interrupted}
	for _, from := range statuses {
		for _, to := range statuses {
			want := from == to || valid[[2]Status{from, to}] || from == Queued && to == Interrupted || from == Running && (to == NoCandidate || to == Failed || to == Cancelled || to == Interrupted) || from == Cancelling && to == Interrupted
			if CanTransition(from, to) != want {
				t.Fatalf("%s -> %s", from, to)
			}
		}
	}
}

type completionBarrierStore struct {
	*MemoryStore
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *completionBarrierStore) UpdateRunCAS(ctx context.Context, expected Status, r Run) (bool, error) {
	if expected == Running && r.Status == Ready {
		s.once.Do(func() { close(s.entered) })
		<-s.release
	}
	return s.MemoryStore.UpdateRunCAS(ctx, expected, r)
}
func TestCancellationWinsConcurrentCompletionWithoutStaleOverwrite(t *testing.T) {
	base := NewMemoryStore()
	store := &completionBarrierStore{MemoryStore: base, entered: make(chan struct{}), release: make(chan struct{})}
	p := &scripted{response: `{"type":"candidate","statement":"Scope cache keys","scope":"workspace caches","exceptions":"none","evidence":[{"seq":2,"summary":"root"}]}`}
	switcher, _ := internalllm.NewSwitchableProvider(internalllm.ProviderSnapshot{ProviderID: "p", ProviderName: "P", BaseURL: "local", Model: "m", Provider: p})
	svc := NewService(context.Background(), store, durableSession(t), switcher, nil)
	defer svc.Close()
	r, err := svc.Enqueue(context.Background(), "w", "s")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("completion did not reach CAS")
	}
	if err := svc.Cancel(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	close(store.release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got, _ := store.Run(context.Background(), r.ID)
		if got.Status == Cancelled {
			return
		}
		time.Sleep(time.Millisecond)
	}
	got, _ := store.Run(context.Background(), r.ID)
	t.Fatalf("status=%s", got.Status)
}

func durableSession(t *testing.T) *session.MemoryStore {
	t.Helper()
	s := session.NewMemoryStore()
	ctx := context.Background()
	_, _ = s.AppendEvent(ctx, "s", session.SessionEvent{Message: &session.Message{Role: session.RoleUser, Text: "Fix flaky cache tests"}})
	_, _ = s.AppendEvent(ctx, "s", session.SessionEvent{Kind: session.KindStepEnded, Message: &session.Message{Role: session.RoleAssistant, Text: "The cache key lacked workspace scope"}})
	return s
}

func TestCaptureOmitsIncompleteTurnAfterStableCut(t *testing.T) {
	s := durableSession(t)
	ctx := context.Background()
	_, _ = s.AppendEvent(ctx, "s", session.SessionEvent{Message: &session.Message{Role: session.RoleUser, Text: "new unfinished evidence"}})
	in, cut, err := Capture(ctx, s, "s")
	if err != nil {
		t.Fatal(err)
	}
	if cut != 2 {
		t.Fatalf("cut=%d", cut)
	}
	for _, m := range in.Messages {
		if strings.Contains(m.Text, "unfinished") {
			t.Fatal("captured incomplete turn")
		}
	}
}

func TestCaptureUsesCompactionProjectionAndPreservesNewestEvidence(t *testing.T) {
	s := session.NewMemoryStore()
	ctx := context.Background()
	_, _ = s.AppendEvent(ctx, "s", session.SessionEvent{Message: &session.Message{Role: session.RoleUser, Text: "covered secret"}})
	_, _ = s.AppendEvent(ctx, "s", session.SessionEvent{Kind: session.KindStepEnded, Message: &session.Message{Role: session.RoleAssistant, Text: "old answer"}})
	_, _ = s.AppendEvent(ctx, "s", session.SessionEvent{Kind: session.KindContextCompacted, Compaction: &session.CompactionCheckpoint{Summary: session.StructuredSummary{CurrentGoal: "summary anchor"}, CoveredThroughSeq: 2, PreservedFromSeq: 4}})
	_, _ = s.AppendEvent(ctx, "s", session.SessionEvent{Message: &session.Message{Role: session.RoleUser, Text: strings.Repeat("n", contextBudget)}})
	_, _ = s.AppendEvent(ctx, "s", session.SessionEvent{Kind: session.KindStepFailed, Error: "newest failure"})
	in, _, err := Capture(ctx, s, "s")
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, m := range in.Messages {
		joined += m.Text
	}
	if strings.Contains(joined, "covered secret") || !strings.Contains(joined, "newest failure") {
		t.Fatalf("projection=%q", joined[:min(len(joined), 200)])
	}
}

func TestExtractorUsesOneToolFreeCallAndValidatesEvidence(t *testing.T) {
	p := &scripted{response: `{"type":"candidate","statement":"Scope cache keys by workspace","scope":"workspace caches","exceptions":"single-workspace processes","evidence":[{"seq":2,"summary":"root cause"}]}`}
	x, err := (Extractor{}).Extract(context.Background(), p, "m", "r", Input{Messages: []InputMessage{{Seq: 2, Role: "assistant", Text: "root cause"}}})
	if err != nil {
		t.Fatal(err)
	}
	if x.Candidate == nil || x.Usage.OutputTokens != 5 {
		t.Fatalf("extraction=%+v", x)
	}
	if p.calls != 1 {
		t.Fatalf("calls=%d", p.calls)
	}
}

func TestExtractorRejectsUnknownAndOppositeVariantFields(t *testing.T) {
	for _, response := range []string{`{"type":"no_candidate","reason":"none","extra":true}`, `{"type":"no_candidate","reason":"none","statement":"wrong"}`, `{"type":"candidate","statement":"s","scope":"x","evidence":[{"seq":1,"summary":"e"}],"reason":"wrong"}`} {
		p := &scripted{response: response}
		if _, err := (Extractor{}).Extract(context.Background(), p, "m", "r", Input{Messages: []InputMessage{{Seq: 1, Text: "e"}}}); err == nil {
			t.Fatalf("accepted %s", response)
		}
		if p.calls != 1 {
			t.Fatalf("calls=%d", p.calls)
		}
	}
}

func TestServiceEnqueueReturnsBeforeWorkerAndApprovesIdempotently(t *testing.T) {
	release := make(chan struct{})
	p := &scripted{release: release, response: `{"type":"candidate","statement":"Scope cache keys by workspace","scope":"workspace caches","exceptions":"single workspace","evidence":[{"seq":2,"summary":"root cause"}]}`}
	switcher, err := internalllm.NewSwitchableProvider(internalllm.ProviderSnapshot{ProviderID: "p", ProviderName: "P", BaseURL: "local", Model: "m", Provider: p})
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStore()
	svc := NewService(context.Background(), store, durableSession(t), switcher, nil)
	defer svc.Close()
	r, err := svc.Enqueue(context.Background(), "w", "s")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != Queued {
		t.Fatalf("status=%s", r.Status)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		r, _ = store.Run(context.Background(), r.ID)
		if r.Status == Ready {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if r.Status != Ready {
		t.Fatalf("status=%s err=%s", r.Status, r.Error)
	}
	first, err := svc.Approve(context.Background(), r.ID, *r.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Approve(context.Background(), r.ID, *r.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatal("approval duplicated lesson")
	}
}

func TestRunningCancellationCancelsProviderAndSettles(t *testing.T) {
	release := make(chan struct{})
	p := &scripted{release: release, response: `{"type":"no_candidate","reason":"none"}`}
	switcher, _ := internalllm.NewSwitchableProvider(internalllm.ProviderSnapshot{ProviderID: "p", ProviderName: "P", BaseURL: "local", Model: "m", Provider: p})
	store := NewMemoryStore()
	svc := NewService(context.Background(), store, durableSession(t), switcher, nil)
	defer svc.Close()
	r, _ := svc.Enqueue(context.Background(), "w", "s")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got, _ := store.Run(context.Background(), r.ID)
		if got.Status == Running {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := svc.Cancel(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got, _ := store.Run(context.Background(), r.ID)
		if got.Status == Cancelled {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("run did not settle cancelled")
}

func TestQueuedCancellationIsAtomicAndIdempotent(t *testing.T) {
	store := NewMemoryStore()
	r := Run{ID: "queued", Workspace: "w", SessionID: "s", CutSeq: 1, Status: Queued}
	_, _, _ = store.CreateRun(context.Background(), r)
	svc := NewService(context.Background(), store, durableSession(t), &scripted{}, nil)
	defer svc.Close()
	if err := svc.Cancel(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Cancel(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Run(context.Background(), r.ID)
	if got.Status != Cancelled {
		t.Fatalf("status=%s", got.Status)
	}
}

func TestWorkspaceQueueRunsFIFO(t *testing.T) {
	sessions := session.NewMemoryStore()
	for _, id := range []string{"s1", "s2", "s3"} {
		_, _ = sessions.AppendEvent(context.Background(), id, session.SessionEvent{Message: &session.Message{Role: session.RoleUser, Text: id}})
		_, _ = sessions.AppendEvent(context.Background(), id, session.SessionEvent{Kind: session.KindStepEnded, Message: &session.Message{Role: session.RoleAssistant, Text: "done"}})
	}
	release := make(chan struct{})
	p := &scripted{release: release, response: `{"type":"no_candidate","reason":"none"}`}
	switcher, _ := internalllm.NewSwitchableProvider(internalllm.ProviderSnapshot{ProviderID: "p", ProviderName: "P", BaseURL: "local", Model: "m", Provider: p})
	svc := NewService(context.Background(), NewMemoryStore(), sessions, switcher, nil)
	defer svc.Close()
	var ids []string
	for _, sid := range []string{"s1", "s2", "s3"} {
		r, err := svc.Enqueue(context.Background(), "w", sid)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, "learning-"+r.ID)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		order := append([]string(nil), p.order...)
		p.mu.Unlock()
		if len(order) == 3 {
			for i := range ids {
				if order[i] != ids[i] {
					t.Fatalf("order=%v want=%v", order, ids)
				}
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("queue did not drain")
}

func TestFileStoreSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "learning.json")
	s, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	r := Run{ID: "r", Workspace: "w", SessionID: "s", CutSeq: 1, Status: Queued, CreatedAt: time.Now()}
	if _, _, err = s.CreateRun(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.Run(context.Background(), "r"); err != nil {
		t.Fatal(err)
	}
}

func TestFileStoreSerializesIndependentInstancesAndReloadsReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "learning.json")
	first, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	runA := Run{ID: "a", Workspace: "w", SessionID: "s", CutSeq: 1, Status: Queued, CreatedAt: time.Now()}
	runB := Run{ID: "b", Workspace: "w", SessionID: "s", CutSeq: 2, Status: Queued, CreatedAt: time.Now()}
	if _, _, err := first.CreateRun(ctx, runA); err != nil {
		t.Fatal(err)
	}
	if _, _, err := second.CreateRun(ctx, runB); err != nil {
		t.Fatal(err)
	}
	for _, store := range []*FileStore{first, second} {
		runs, err := store.Runs(ctx, "w")
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) != 2 {
			t.Fatalf("store %p sees %d runs, want 2: %+v", store, len(runs), runs)
		}
	}
}

func TestFileStoreLessonMutationsUseLatestCrossProcessState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "learning.json")
	first, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	run := Run{ID: "run", Workspace: "w", SessionID: "s", CutSeq: 1, Status: Ready, CreatedAt: time.Now()}
	candidate := Candidate{Statement: "Use focused tests", Scope: "workspace", Evidence: []Evidence{{Seq: 1, Summary: "observed"}}}
	run.Candidate = &candidate
	if _, _, err := first.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	lesson, err := first.Approve(ctx, run.ID, candidate, Lesson{ID: "lesson", Workspace: "w", RunID: run.ID, Enabled: true, CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.DeleteLesson(ctx, lesson.ID); err != nil {
		t.Fatal(err)
	}
	updated, err := first.SetLessonEnabled(ctx, lesson.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Deleted {
		t.Fatal("enabling from a stale store resurrected a deleted lesson")
	}
}

func TestFileStoreFailedPersistenceIsNotVisible(t *testing.T) {
	s := &FileStore{MemoryStore: NewMemoryStore(), path: "/proc/atenea-learning/state.json"}
	r := Run{ID: "r", Workspace: "w", SessionID: "s", CutSeq: 1, Status: Queued}
	if _, _, err := s.CreateRun(context.Background(), r); err == nil {
		t.Fatal("expected persistence failure")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runs["r"]; ok {
		t.Fatal("failed persistence left mutation visible in memory")
	}
}

func TestSelectorIsBoundedDeterministicAndExcludesDisabled(t *testing.T) {
	var ls []Lesson
	for i := 0; i < 7; i++ {
		ls = append(ls, Lesson{ID: string(rune('a' + i)), Enabled: true, Candidate: Candidate{Statement: "workspace cache key", Scope: "cache invalidation"}})
	}
	ls[0].Enabled = false
	got := Select("CACHE, workspace!", ls)
	if len(got) != 5 {
		t.Fatalf("len=%d", len(got))
	}
	if got[0].ID != "b" {
		t.Fatalf("first=%s", got[0].ID)
	}
	if !strings.Contains(RenderLessons(got), "approved_workspace_lessons") {
		t.Fatal("missing stable section")
	}
}

func TestSelectorVetoesMatchingException(t *testing.T) {
	ls := []Lesson{{ID: "a", Enabled: true, Candidate: Candidate{Statement: "cache workspace", Scope: "cache", Exceptions: "windows"}}}
	if got := Select("workspace cache on Windows", ls); len(got) != 0 {
		t.Fatalf("selected contraindicated lesson")
	}
}

func TestRecoverMarksEveryWorkspaceInterrupted(t *testing.T) {
	store := NewMemoryStore()
	for _, r := range []Run{{ID: "a", Workspace: "one", SessionID: "s", CutSeq: 1, Status: Running}, {ID: "b", Workspace: "two", SessionID: "s", CutSeq: 2, Status: Queued}} {
		_, _, _ = store.CreateRun(context.Background(), r)
	}
	svc := NewService(context.Background(), store, durableSession(t), &scripted{}, nil)
	defer svc.Close()
	if err := svc.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		r, _ := store.Run(context.Background(), id)
		if r.Status != Interrupted {
			t.Fatalf("%s=%s", id, r.Status)
		}
	}
}

func TestRecoverDoesNotInterruptRunLeasedByAnotherService(t *testing.T) {
	path := filepath.Join(t.TempDir(), "learning.json")
	first, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	p := &scripted{release: release, response: `{"type":"no_candidate","reason":"none"}`}
	switcher, err := internalllm.NewSwitchableProvider(internalllm.ProviderSnapshot{ProviderID: "p", ProviderName: "P", BaseURL: "local", Model: "m", Provider: p})
	if err != nil {
		t.Fatal(err)
	}
	owner := NewService(context.Background(), first, durableSession(t), switcher, nil)
	r, err := owner.Enqueue(context.Background(), "w", "s")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got, _ := second.Run(context.Background(), r.ID)
		if got.Status == Running {
			break
		}
		time.Sleep(time.Millisecond)
	}
	recoverer := NewService(context.Background(), second, durableSession(t), switcher, nil)
	if err := recoverer.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := second.Run(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Running {
		t.Fatalf("live leased run became %s", got.Status)
	}
	owner.Close()
	recoverer.Close()
}

func TestCloseSettlesAllJobsBeyondWorkerCapacity(t *testing.T) {
	release := make(chan struct{})
	p := &scripted{release: release, response: `{"type":"no_candidate","reason":"none"}`}
	switcher, err := internalllm.NewSwitchableProvider(internalllm.ProviderSnapshot{ProviderID: "p", ProviderName: "P", BaseURL: "local", Model: "m", Provider: p})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "learning.json")
	store, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(context.Background(), store, durableSession(t), switcher, nil)
	var ids []string
	for i := 0; i < 8; i++ {
		r, err := svc.Enqueue(context.Background(), fmt.Sprintf("w-%d", i), "s")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	svc.Close()
	for _, id := range ids {
		r, err := store.Run(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status == Queued || r.Status == Running || r.Status == Cancelling {
			t.Fatalf("%s remained %s after Close", id, r.Status)
		}
	}
	reopened, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		r, err := reopened.Run(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status == Queued || r.Status == Running || r.Status == Cancelling {
			t.Fatalf("%s was durably left %s after Close", id, r.Status)
		}
	}
}

func TestFileStoreRecoverMarksOrphanRunInterrupted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "learning.json")
	store, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	orphan := Run{ID: "../../untrusted", Workspace: "w", SessionID: "s", CutSeq: 1, Status: Running, CreatedAt: time.Now()}
	if _, _, err := store.CreateRun(context.Background(), orphan); err != nil {
		t.Fatal(err)
	}
	svc := NewService(context.Background(), store, durableSession(t), &scripted{}, nil)
	defer svc.Close()
	if err := svc.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Run(context.Background(), orphan.ID)
	if got.Status != Interrupted {
		t.Fatalf("orphan status=%s", got.Status)
	}
}

func TestSelectedLessonsHaveStablePromptPlacement(t *testing.T) {
	section := RenderLessons([]Lesson{{ID: "l", Enabled: true, Candidate: Candidate{Statement: "scope cache keys", Scope: "workspace caches"}}})
	got := prompt.BuildWithLessons("model", prompt.Env{WorkingDir: "/work"}, "project rules", "skills", section)
	lessonAt, envAt := strings.Index(got, section), strings.Index(got, "Current working directory")
	if lessonAt < strings.Index(got, "project rules") || lessonAt > envAt {
		t.Fatalf("lesson section has wrong placement")
	}
}

var _ coresession.Seq

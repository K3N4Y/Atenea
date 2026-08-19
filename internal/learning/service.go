package learning

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	corellm "github.com/K3N4Y/atenea/agentcore/llm"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/session"
)

type ChangeFunc func(workspace string)
type Service struct {
	store     Store
	sessions  session.Store
	provider  corellm.Provider
	extract   Extractor
	changed   ChangeFunc
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	queues    map[string]*jobQueue
	active    map[string]context.CancelFunc
	workers   chan struct{}
	workerWG  sync.WaitGroup
	closeOnce sync.Once
	closed    bool
}
type jobQueue struct {
	pending []job
	wake    chan struct{}
}
type job struct {
	id       string
	snapshot llm.ProviderSnapshot
	lease    RunLease
}

func NewService(parent context.Context, store Store, sessions session.Store, provider corellm.Provider, changed ChangeFunc) *Service {
	ctx, cancel := context.WithCancel(parent)
	s := &Service{store: store, sessions: sessions, provider: provider, changed: changed, ctx: ctx, cancel: cancel, queues: map[string]*jobQueue{}, active: map[string]context.CancelFunc{}, workers: make(chan struct{}, 2)}
	return s
}
func (s *Service) Close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		var pending []job
		for _, q := range s.queues {
			pending = append(pending, q.pending...)
			q.pending = nil
		}
		for _, cancel := range s.active {
			cancel()
		}
		s.mu.Unlock()
		s.cancel()
		for _, j := range pending {
			s.abandon(j)
		}
		s.workerWG.Wait()
	})
}

func (s *Service) Recover(ctx context.Context) error {
	workspaces, err := s.store.Workspaces(ctx)
	if err != nil {
		return err
	}
	for _, workspace := range workspaces {
		runs, err := s.store.Runs(ctx, workspace)
		if err != nil {
			return err
		}
		for _, r := range runs {
			if r.Status != Queued && r.Status != Running && r.Status != Cancelling {
				continue
			}
			lease, acquired, err := s.tryLease(r.ID)
			if err != nil {
				return err
			}
			if !acquired {
				continue
			}
			from := r.Status
			r.Status = Interrupted
			now := time.Now().UTC()
			r.FinishedAt = &now
			_, updateErr := s.store.UpdateRunCAS(ctx, from, r)
			if lease != nil {
				updateErr = errors.Join(updateErr, lease.Release())
			}
			if updateErr != nil {
				return updateErr
			}
		}
	}
	return nil
}

func (s *Service) Enqueue(ctx context.Context, workspace, sessionID string) (Run, error) {
	return s.EnqueueWithProvider(ctx, workspace, sessionID, s.provider)
}

// EnqueueWithProvider captures the same immutable session cut as Enqueue but
// snapshots an explicitly resolved provider/model for this run. The caller owns
// selection; the queue owns the resulting snapshot, so later chat model changes
// cannot affect an extraction already accepted.
func (s *Service) EnqueueWithProvider(ctx context.Context, workspace, sessionID string, provider corellm.Provider) (Run, error) {
	if workspace == "" || sessionID == "" {
		return Run{}, errors.New("learning requires an active workspace and session")
	}
	input, cut, err := Capture(ctx, s.sessions, sessionID)
	if err != nil {
		return Run{}, err
	}
	snap := llm.Acquire(provider)
	if snap.Provider == nil {
		return Run{}, errors.New("learning requires an active provider")
	}
	r := Run{ID: newID(), Workspace: workspace, SessionID: sessionID, CutSeq: cut, Status: Queued, Input: input, ProviderID: snap.ProviderID, Model: snap.Model, CreatedAt: time.Now().UTC()}
	lease, acquired, err := s.tryLease(r.ID)
	if err != nil {
		return Run{}, err
	}
	if !acquired {
		return Run{}, errors.New("learning run lease unexpectedly unavailable")
	}
	j := job{id: r.ID, snapshot: snap, lease: lease}
	r, created, err := s.store.CreateRun(ctx, r)
	if err != nil || !created {
		j.release()
		return r, err
	}
	if !s.queue(workspace, j) {
		s.abandon(j)
		return r, context.Canceled
	}
	s.notify(workspace)
	return r, nil
}
func (s *Service) queue(workspace string, j job) bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	q := s.queues[workspace]
	if q == nil {
		q = &jobQueue{wake: make(chan struct{}, 1)}
		s.queues[workspace] = q
		s.workerWG.Add(1)
		go s.worker(workspace, q)
	}
	q.pending = append(q.pending, j)
	s.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return true
}
func (s *Service) worker(workspace string, q *jobQueue) {
	defer s.workerWG.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-q.wake:
			for {
				s.mu.Lock()
				if len(q.pending) == 0 {
					s.mu.Unlock()
					break
				}
				j := q.pending[0]
				q.pending = q.pending[1:]
				s.mu.Unlock()
				select {
				case s.workers <- struct{}{}:
				case <-s.ctx.Done():
					s.abandon(j)
					return
				}
				s.execute(workspace, j)
				<-s.workers
			}
		}
	}
}
func (s *Service) execute(workspace string, j job) {
	ctx, cancel := context.WithCancel(s.ctx)
	s.mu.Lock()
	s.active[j.id] = cancel
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.active, j.id)
		s.mu.Unlock()
		j.release()
	}()
	r, err := s.store.Run(ctx, j.id)
	if err != nil || r.Status != Queued {
		return
	}
	now := time.Now().UTC()
	r.Status = Running
	r.StartedAt = &now
	if ok, updateErr := s.store.UpdateRunCAS(context.WithoutCancel(ctx), Queued, r); updateErr != nil || !ok {
		return
	}
	s.notify(workspace)
	x, err := s.extract.Extract(ctx, j.snapshot.Provider, j.snapshot.Model, r.ID, r.Input)
	done := time.Now().UTC()
	r.FinishedAt = &done
	if errors.Is(ctx.Err(), context.Canceled) {
		r.Status = Cancelled
	} else if err != nil {
		r.Status = Failed
		r.Error = err.Error()
	} else {
		r.Usage = x.Usage
		if x.Candidate != nil {
			r.Status = Ready
			r.Candidate = x.Candidate
		} else {
			r.Status = NoCandidate
			r.NoCandidateReason = x.NoCandidateReason
		}
	}
	s.finish(context.WithoutCancel(ctx), r)
	s.notify(workspace)
}
func (s *Service) finish(ctx context.Context, result Run) {
	for {
		current, err := s.store.Run(ctx, result.ID)
		if err != nil {
			return
		}
		switch current.Status {
		case Cancelling:
			current.Status = Cancelled
			current.FinishedAt = result.FinishedAt
			_, _ = s.store.UpdateRunCAS(ctx, Cancelling, current)
			return
		case Running:
			if ok, _ := s.store.UpdateRunCAS(ctx, Running, result); ok {
				return
			}
			continue
		default:
			return
		}
	}
}
func (s *Service) Cancel(ctx context.Context, id string) error {
	r, err := s.store.Run(ctx, id)
	if err != nil {
		return err
	}
	if r.Status == Cancelled {
		return nil
	}
	if r.Status == Queued {
		r.Status = Cancelled
		now := time.Now().UTC()
		r.FinishedAt = &now
		if ok, err := s.store.UpdateRunCAS(ctx, Queued, r); err != nil {
			return err
		} else if !ok {
			return s.Cancel(ctx, id)
		}
		s.notify(r.Workspace)
		return nil
	}
	if r.Status != Running && r.Status != Cancelling {
		return ErrInvalidTransition
	}
	from := r.Status
	r.Status = Cancelling
	if ok, err := s.store.UpdateRunCAS(ctx, from, r); err != nil {
		return err
	} else if !ok {
		return s.Cancel(ctx, id)
	}
	s.mu.Lock()
	cancel := s.active[id]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.notify(r.Workspace)
	return nil
}
func (s *Service) Retry(ctx context.Context, id string) (Run, error) {
	return s.RetryWithProvider(ctx, id, s.provider)
}

// RetryWithProvider repeats a failed extraction with an explicitly resolved
// provider/model. The caller snapshots its current learning configuration.
func (s *Service) RetryWithProvider(ctx context.Context, id string, provider corellm.Provider) (Run, error) {
	old, err := s.store.Run(ctx, id)
	if err != nil {
		return Run{}, err
	}
	if old.Status != Failed && old.Status != Cancelled && old.Status != Interrupted {
		return Run{}, ErrInvalidTransition
	}
	snap := llm.Acquire(provider)
	if snap.Provider == nil {
		return Run{}, errors.New("learning requires an active provider")
	}
	r := old
	r.ID = newID()
	r.Status = Queued
	r.ProviderID = snap.ProviderID
	r.Model = snap.Model
	r.Candidate = nil
	r.Error = ""
	r.NoCandidateReason = ""
	r.CreatedAt = time.Now().UTC()
	r.StartedAt = nil
	r.FinishedAt = nil
	r.DecidedAt = nil
	lease, acquired, err := s.tryLease(r.ID)
	if err != nil {
		return Run{}, err
	}
	if !acquired {
		return Run{}, errors.New("learning run lease unexpectedly unavailable")
	}
	j := job{id: r.ID, snapshot: snap, lease: lease}
	r, created, err := s.store.CreateRun(ctx, r)
	if err != nil || !created {
		j.release()
		return r, err
	}
	if !s.queue(r.Workspace, j) {
		s.abandon(j)
		return r, context.Canceled
	}
	s.notify(r.Workspace)
	return r, nil
}
func (s *Service) Approve(ctx context.Context, id string, c Candidate) (Lesson, error) {
	r, err := s.store.Run(ctx, id)
	if err != nil {
		return Lesson{}, err
	}
	l := Lesson{ID: newID(), Workspace: r.Workspace, RunID: id, Candidate: c, Enabled: true, CreatedAt: time.Now().UTC()}
	l, err = s.store.Approve(ctx, id, c, l)
	if err == nil {
		s.notify(r.Workspace)
	}
	return l, err
}
func (s *Service) Reject(ctx context.Context, id string) error {
	r, err := s.store.Run(ctx, id)
	if err != nil {
		return err
	}
	if err = s.store.Reject(ctx, id); err == nil {
		s.notify(r.Workspace)
	}
	return err
}
func (s *Service) Audit(ctx context.Context, w string) ([]Run, error) { return s.store.Runs(ctx, w) }
func (s *Service) Lessons(ctx context.Context, w string) ([]Lesson, error) {
	return s.store.Lessons(ctx, w)
}
func (s *Service) SetLessonEnabled(ctx context.Context, id string, enabled bool) error {
	lesson, err := s.store.SetLessonEnabled(ctx, id, enabled)
	if err == nil {
		s.notify(lesson.Workspace)
	}
	return err
}
func (s *Service) DeleteLesson(ctx context.Context, id string) error {
	lesson, err := s.store.DeleteLesson(ctx, id)
	if err == nil {
		s.notify(lesson.Workspace)
	}
	return err
}
func (s *Service) notify(w string) {
	if s.changed != nil {
		s.changed(w)
	}
}
func (s *Service) tryLease(id string) (RunLease, bool, error) {
	store, ok := s.store.(runLeaseStore)
	if !ok {
		return nil, true, nil
	}
	return store.TryRunLease(id)
}

func (s *Service) abandon(j job) {
	r, err := s.store.Run(context.Background(), j.id)
	if err == nil && r.Status == Queued {
		now := time.Now().UTC()
		r.Status = Interrupted
		r.FinishedAt = &now
		_, _ = s.store.UpdateRunCAS(context.Background(), Queued, r)
		s.notify(r.Workspace)
	}
	j.release()
}

func (j *job) release() {
	if j.lease != nil {
		_ = j.lease.Release()
		j.lease = nil
	}
}
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(b[:])
}

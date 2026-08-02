package event

import (
	"context"
	"sync"

	"github.com/K3N4Y/atenea/internal/session"
)

// ChildActivityStore decorates a child's in-memory store. Selected live events
// are forwarded on the parent's bus channel after the durable child append.
// The child envelope keeps its SessionID and Seq and is attributed to the exact
// parent task call so consumers can distinguish concurrent tasks.
type ChildActivityStore struct {
	inner           session.Store
	bus             *Bus
	parentSessionID string
	parentCallID    string
	includeActivity bool
	mu              sync.Mutex
}

// NewChildActivityStore wraps inner for child permissions and, when requested
// by the host, live tool-batch activity.
func NewChildActivityStore(parentSessionID, parentCallID string, inner session.Store, bus *Bus, includeActivity bool) *ChildActivityStore {
	return &ChildActivityStore{inner: inner, bus: bus, parentSessionID: parentSessionID, parentCallID: parentCallID, includeActivity: includeActivity}
}

var _ session.Store = (*ChildActivityStore)(nil)

func (s *ChildActivityStore) forwards(kind session.EventKind) bool {
	switch kind {
	case session.KindToolPermissionRequested, session.KindToolSuccess, session.KindToolFailed:
		return true
	case session.KindStepStarted, session.KindStepEnded, session.KindStepFailed, session.KindToolCalled:
		return s.includeActivity
	default:
		return false
	}
}

// AppendEvent publishes only after inner accepts the child event.
func (s *ChildActivityStore) AppendEvent(ctx context.Context, sessionID string, ev session.SessionEvent) (session.Seq, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, err := s.inner.AppendEvent(ctx, sessionID, ev)
	if err != nil {
		return seq, err
	}
	if s.forwards(ev.Kind) {
		ev.SessionID = sessionID
		ev.Seq = seq
		s.bus.PublishOn(s.parentSessionID, session.WithParentTaskCall(ev, s.parentCallID))
	}
	return seq, nil
}

// LoadSession delegates without publishing.
func (s *ChildActivityStore) LoadSession(ctx context.Context, sessionID string) (session.Session, error) {
	return s.inner.LoadSession(ctx, sessionID)
}

// Messages delegates without publishing.
func (s *ChildActivityStore) Messages(ctx context.Context, sessionID string, sinceSeq session.Seq) ([]session.Message, error) {
	return s.inner.Messages(ctx, sessionID, sinceSeq)
}

// Sessions delegates without publishing.
func (s *ChildActivityStore) Sessions(ctx context.Context) ([]session.SessionSummary, error) {
	return s.inner.Sessions(ctx)
}

// Events delegates without publishing.
func (s *ChildActivityStore) Events(ctx context.Context, sessionID string, sinceSeq session.Seq) ([]session.SessionEvent, error) {
	return s.inner.Events(ctx, sessionID, sinceSeq)
}

// Epoch delegates without publishing.
func (s *ChildActivityStore) Epoch(ctx context.Context, sessionID string) (session.ContextEpoch, error) {
	return s.inner.Epoch(ctx, sessionID)
}

// PendingToolCalls delegates without publishing.
func (s *ChildActivityStore) PendingToolCalls(ctx context.Context, sessionID string) ([]session.PendingTool, error) {
	return s.inner.PendingToolCalls(ctx, sessionID)
}

// DeleteSession delegates without publishing.
func (s *ChildActivityStore) DeleteSession(ctx context.Context, sessionID string) error {
	return s.inner.DeleteSession(ctx, sessionID)
}

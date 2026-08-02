package subagent

import (
	"context"
	"sync/atomic"

	"github.com/K3N4Y/atenea/internal/session"
)

// countingStore counts successfully appended immediate-child Tool.Called
// occurrences. Embedding preserves every other Store operation unchanged.
type countingStore struct {
	session.Store
	total atomic.Int64
}

func (s *countingStore) AppendEvent(ctx context.Context, sessionID string, event session.SessionEvent) (session.Seq, error) {
	seq, err := s.Store.AppendEvent(ctx, sessionID, event)
	if err == nil && event.Kind == session.KindToolCalled {
		s.total.Add(1)
	}
	return seq, err
}

func (s *countingStore) count() int { return int(s.total.Load()) }

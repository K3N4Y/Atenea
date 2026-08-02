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
	total    atomic.Int64
	onChange func(int)
}

func (s *countingStore) AppendEvent(ctx context.Context, sessionID string, event session.SessionEvent) (session.Seq, error) {
	seq, err := s.Store.AppendEvent(ctx, sessionID, event)
	if err == nil && event.Kind == session.KindToolCalled {
		total := int(s.total.Add(1))
		if s.onChange != nil {
			s.onChange(total)
		}
	}
	return seq, err
}

func (s *countingStore) count() int { return int(s.total.Load()) }

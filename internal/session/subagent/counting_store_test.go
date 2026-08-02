package subagent

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/K3N4Y/atenea/internal/session"
)

type failingAppenderStore struct{ session.Store }

func (s failingAppenderStore) AppendEvent(context.Context, string, session.SessionEvent) (session.Seq, error) {
	return 0, errors.New("append")
}

func TestCountingStoreSuccessfulOccurrencesAndConcurrency(t *testing.T) {
	s := &countingStore{Store: session.NewMemoryStore()}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.AppendEvent(context.Background(), "child", session.SessionEvent{Kind: session.KindToolCalled, CallID: "repeated"})
		}()
	}
	wg.Wait()
	_, _ = s.AppendEvent(context.Background(), "child", session.SessionEvent{Kind: session.KindToolSuccess})
	if s.count() != 50 {
		t.Fatalf("count = %d, want 50", s.count())
	}
	failed := &countingStore{Store: failingAppenderStore{Store: session.NewMemoryStore()}}
	_, _ = failed.AppendEvent(context.Background(), "child", session.SessionEvent{Kind: session.KindToolCalled})
	if failed.count() != 0 {
		t.Fatal("failed append counted")
	}
}

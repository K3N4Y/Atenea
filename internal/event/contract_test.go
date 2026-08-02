package event

import (
	"sync"
	"testing"

	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/session/sessiontest"
)

// A decorator is where a store contract quietly rots: it delegates nine methods
// and adds behavior to one, and nothing checks that the nine still behave. Both
// decorators here sit between the runner and the real store, so the runner is
// entitled to the same contract through them as without them.

// countingEmitter is a Bus sink that only counts. It is called while the
// decorator holds its lock, and the contract appends from many goroutines, so it
// locks anyway rather than relying on that.
type countingEmitter struct {
	mu     sync.Mutex
	events int
}

func (c *countingEmitter) emit(string, ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events++
}

func TestEmittingStore_Contract(t *testing.T) {
	sessiontest.StoreContract(t, func(t *testing.T) session.Store {
		return NewEmittingStore(session.NewMemoryStore(), NewBus((&countingEmitter{}).emit))
	})
}

func TestChildActivityStore_Contract(t *testing.T) {
	sessiontest.StoreContract(t, func(t *testing.T) session.Store {
		return NewChildActivityStore("parent", "task-call", session.NewMemoryStore(), NewBus((&countingEmitter{}).emit), true)
	})
}

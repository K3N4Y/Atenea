package tool

import (
	"context"
	"sync"

	"atenea/internal/tool/hashline"
)

type sessionIDKey struct{}

// WithSessionID anota el contexto de ejecucion de tools con la sesion actual.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDKey{}, sessionID)
}

// SnapshotProvider entrega el store de snapshots que debe usar una tool.
type SnapshotProvider interface {
	Snapshots(ctx context.Context) hashline.SnapshotStore
}

// SessionSnapshots mantiene un SnapshotStore por sesion.
type SessionSnapshots struct {
	mu     sync.Mutex
	stores map[string]hashline.SnapshotStore
}

func NewSessionSnapshots() *SessionSnapshots {
	return &SessionSnapshots{stores: map[string]hashline.SnapshotStore{}}
}

func (s *SessionSnapshots) Snapshots(ctx context.Context) hashline.SnapshotStore {
	sessionID, _ := ctx.Value(sessionIDKey{}).(string)
	if sessionID == "" {
		sessionID = "default"
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	store := s.stores[sessionID]
	if store == nil {
		store = hashline.NewMemSnapshotStore()
		s.stores[sessionID] = store
	}
	return store
}

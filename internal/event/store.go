package event

import (
	"context"
	"sync"

	"atenea/internal/session"
)

// EmittingStore decora un session.Store: tras un AppendEvent exitoso reenvia el
// evento ya sellado (con SessionID y Seq) al Bus. Serializa append y emit bajo
// un mutex para entregar las emisiones en orden de Seq.
type EmittingStore struct {
	inner session.Store
	bus   *Bus
	mu    sync.Mutex
}

// NewEmittingStore envuelve inner para emitir al bus cada evento appendeado.
func NewEmittingStore(inner session.Store, bus *Bus) *EmittingStore {
	return &EmittingStore{inner: inner, bus: bus}
}

// var _ session.Store = (*EmittingStore)(nil) asegura en tiempo de compilacion
// que el decorador cumple la interface.
var _ session.Store = (*EmittingStore)(nil)

// AppendEvent delega en inner bajo candado y, si el append fue exitoso, sella su
// copia local con SessionID y Seq y la publica en el bus. Si inner falla,
// devuelve el error sin emitir.
func (s *EmittingStore) AppendEvent(ctx context.Context, sessionID string, ev session.SessionEvent) (session.Seq, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	seq, err := s.inner.AppendEvent(ctx, sessionID, ev)
	if err != nil {
		return seq, err
	}
	ev.SessionID = sessionID
	ev.Seq = seq
	s.bus.Publish(ev)
	return seq, nil
}

// LoadSession delega sin candado ni emision.
func (s *EmittingStore) LoadSession(ctx context.Context, sessionID string) (session.Session, error) {
	return s.inner.LoadSession(ctx, sessionID)
}

// Messages delega sin candado ni emision.
func (s *EmittingStore) Messages(ctx context.Context, sessionID string, sinceSeq session.Seq) ([]session.Message, error) {
	return s.inner.Messages(ctx, sessionID, sinceSeq)
}

// Epoch delega sin candado ni emision.
func (s *EmittingStore) Epoch(ctx context.Context, sessionID string) (session.ContextEpoch, error) {
	return s.inner.Epoch(ctx, sessionID)
}

// PendingToolCalls delega sin candado ni emision.
func (s *EmittingStore) PendingToolCalls(ctx context.Context, sessionID string) ([]session.PendingTool, error) {
	return s.inner.PendingToolCalls(ctx, sessionID)
}

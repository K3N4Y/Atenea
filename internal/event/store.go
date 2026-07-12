package event

import (
	"context"
	"errors"
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
var _ session.CompactionStore = (*EmittingStore)(nil)

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

// Sessions delega sin candado ni emision.
func (s *EmittingStore) Sessions(ctx context.Context) ([]session.SessionSummary, error) {
	return s.inner.Sessions(ctx)
}

// Events delega sin candado ni emision.
func (s *EmittingStore) Events(ctx context.Context, sessionID string, sinceSeq session.Seq) ([]session.SessionEvent, error) {
	return s.inner.Events(ctx, sessionID, sinceSeq)
}

// Epoch delega sin candado ni emision.
func (s *EmittingStore) Epoch(ctx context.Context, sessionID string) (session.ContextEpoch, error) {
	return s.inner.Epoch(ctx, sessionID)
}

// PendingToolCalls delega sin candado ni emision.
func (s *EmittingStore) PendingToolCalls(ctx context.Context, sessionID string) ([]session.PendingTool, error) {
	return s.inner.PendingToolCalls(ctx, sessionID)
}

// DeleteSession delega sin candado ni emision.
func (s *EmittingStore) DeleteSession(ctx context.Context, sessionID string) error {
	return s.inner.DeleteSession(ctx, sessionID)
}

func (s *EmittingStore) ContextForRunner(ctx context.Context, sessionID string) (session.RunnerContext, error) {
	store, ok := s.inner.(session.CompactionStore)
	if ok {
		return store.ContextForRunner(ctx, sessionID)
	}
	epoch, err := s.inner.Epoch(ctx, sessionID)
	if err != nil {
		return session.RunnerContext{}, err
	}
	messages, err := s.inner.Messages(ctx, sessionID, epoch.BaselineSeq)
	if err != nil {
		return session.RunnerContext{}, err
	}
	return session.RunnerContext{Epoch: epoch, Messages: messages}, nil
}

func (s *EmittingStore) CommitCompaction(ctx context.Context, sessionID string, checkpoint session.CompactionCheckpoint) (session.Seq, error) {
	store, ok := s.inner.(session.CompactionStore)
	if !ok {
		return 0, errors.New("inner store does not support context compaction")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, err := store.CommitCompaction(ctx, sessionID, checkpoint)
	if err != nil {
		return seq, err
	}
	checkpointCopy := checkpoint
	s.bus.Publish(session.SessionEvent{
		SessionID:  sessionID,
		Seq:        seq,
		Kind:       session.KindContextCompacted,
		Compaction: &checkpointCopy,
	})
	return seq, nil
}

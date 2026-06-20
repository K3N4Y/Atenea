package session

import (
	"context"
	"sync"
)

// MemoryStore es la implementacion en memoria del Store para M1..M9. Guarda el
// log de eventos por sesion bajo un mutex y deriva los mensajes al vuelo.
type MemoryStore struct {
	mu       sync.Mutex
	sessions map[string][]SessionEvent
}

// NewMemoryStore crea un store vacio listo para usar.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{sessions: make(map[string][]SessionEvent)}
}

// AppendEvent agrega ev al log durable de sessionID, le asigna el siguiente Seq
// monotonico y lo devuelve. Crea la sesion si es su primer evento. El SessionID
// y Seq que traiga ev se ignoran: los fija el Store.
func (s *MemoryStore) AppendEvent(ctx context.Context, sessionID string, ev SessionEvent) (Seq, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	log := s.sessions[sessionID]
	seq := Seq(len(log) + 1)
	ev.SessionID = sessionID
	ev.Seq = seq
	s.sessions[sessionID] = append(log, ev)
	return seq, nil
}

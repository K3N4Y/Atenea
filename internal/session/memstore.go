package session

import (
	"context"
	"sort"
	"sync"
)

// MemoryStore es la implementacion en memoria del Store para M1..M9. Guarda el
// log de eventos por sesion bajo un mutex y deriva los mensajes al vuelo.
type MemoryStore struct {
	mu       sync.Mutex
	sessions map[string][]SessionEvent
	// lastSeen marca el orden global de insercion del ultimo evento de cada
	// sesion: el equivalente en memoria del MAX(rowid) que ordena Sessions por
	// recencia. Un contador monotonico global lo alimenta en cada AppendEvent.
	lastSeen map[string]int
	clock    int
}

// NewMemoryStore crea un store vacio listo para usar.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		sessions: make(map[string][]SessionEvent),
		lastSeen: make(map[string]int),
	}
}

// var _ Store = (*MemoryStore)(nil) asegura en tiempo de compilacion que
// MemoryStore cumple la interface.
var _ Store = (*MemoryStore)(nil)

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
	s.clock++
	s.lastSeen[sessionID] = s.clock
	return seq, nil
}

// LoadSession devuelve el agregado de la sesion. ErrSessionNotFound si la sesion
// nunca recibio un evento.
func (s *MemoryStore) LoadSession(ctx context.Context, sessionID string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.sessions[sessionID]; !ok {
		return Session{}, ErrSessionNotFound
	}
	return Session{ID: sessionID}, nil
}

// Messages reproyecta los mensajes de la sesion en orden de Seq y devuelve solo
// los materializados por eventos con Seq > sinceSeq. ErrSessionNotFound si la
// sesion no existe.
func (s *MemoryStore) Messages(ctx context.Context, sessionID string, sinceSeq Seq) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	log, ok := s.sessions[sessionID]
	if !ok {
		return nil, ErrSessionNotFound
	}

	var out []Message
	for _, ev := range log {
		if ev.Message == nil || ev.Seq <= sinceSeq {
			continue
		}
		m := *ev.Message
		m.Seq = ev.Seq
		out = append(out, m)
	}
	return out, nil
}

// Sessions devuelve un resumen por sesion con al menos un evento, ordenado por
// actividad mas reciente primero. Replica el orden por rowid del store durable
// usando el indice del ultimo evento agregado a cada log; el Title es el primer
// mensaje del usuario de la sesion (truncado), "" si aun no hay uno.
func (s *MemoryStore) Sessions(ctx context.Context) ([]SessionSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	type entry struct {
		id    string
		title string
		last  int // posicion global del ultimo evento: aproxima MAX(rowid)
	}
	entries := make([]entry, 0, len(s.sessions))
	for id, log := range s.sessions {
		if len(log) == 0 {
			continue
		}
		title := ""
		for _, ev := range log {
			if ev.Message != nil && ev.Message.Role == RoleUser {
				title = truncateTitle(ev.Message.Text)
				break
			}
		}
		// El ultimo evento del log lleva el Seq mas alto de la sesion, pero entre
		// sesiones eso no ordena por recencia. Igualamos al store durable usando un
		// contador de insercion global; aqui lo reconstruimos por el orden de
		// llegada que ya quedo en cada log via el contador del store.
		entries = append(entries, entry{id: id, title: title, last: s.lastSeen[id]})
	}
	sort.Slice(entries, func(a, b int) bool { return entries[a].last > entries[b].last })

	out := make([]SessionSummary, 0, len(entries))
	for _, e := range entries {
		out = append(out, SessionSummary{ID: e.id, Title: e.title})
	}
	return out, nil
}

// Events devuelve todos los SessionEvent durables de la sesion en orden de Seq
// con Seq > sinceSeq. ErrSessionNotFound si la sesion no existe. El MemoryStore
// guarda los eventos verbatim, asi que solo filtra y copia.
func (s *MemoryStore) Events(ctx context.Context, sessionID string, sinceSeq Seq) ([]SessionEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	log, ok := s.sessions[sessionID]
	if !ok {
		return nil, ErrSessionNotFound
	}

	out := make([]SessionEvent, 0)
	for _, ev := range log {
		if ev.Seq > sinceSeq {
			out = append(out, ev)
		}
	}
	return out, nil
}

// Epoch devuelve la foto del contexto de la sesion. M7 no tiene aun una fuente real
// de contexto (agente/modelo de config, reconciliacion de archivos), asi que
// MemoryStore devuelve el epoch cero estable: snapshot y recheck coinciden y el
// runner no reconstruye. ErrSessionNotFound si la sesion no existe. El driver real
// del epoch llega en M10.
func (s *MemoryStore) Epoch(ctx context.Context, sessionID string) (ContextEpoch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.sessions[sessionID]; !ok {
		return ContextEpoch{}, ErrSessionNotFound
	}
	return ContextEpoch{}, nil
}

// PendingToolCalls reconstruye la proyeccion durable de Tool.Called que aun no
// tienen Tool.Success ni Tool.Failed posterior, manteniendo el orden de llamada.
func (s *MemoryStore) PendingToolCalls(ctx context.Context, sessionID string) ([]PendingTool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	log, ok := s.sessions[sessionID]
	if !ok {
		return nil, ErrSessionNotFound
	}

	pending := make(map[string]PendingTool)
	order := make([]string, 0)
	for _, ev := range log {
		switch ev.Kind {
		case KindToolCalled:
			if _, ok := pending[ev.CallID]; !ok {
				order = append(order, ev.CallID)
			}
			pending[ev.CallID] = PendingTool{CallID: ev.CallID, ToolName: ev.ToolName}
		case KindToolSuccess, KindToolFailed:
			delete(pending, ev.CallID)
		}
	}

	out := make([]PendingTool, 0, len(pending))
	for _, callID := range order {
		if tool, ok := pending[callID]; ok {
			out = append(out, tool)
		}
	}
	return out, nil
}

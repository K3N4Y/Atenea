package session

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryStore es la implementacion en memoria del Store para M1..M9. Guarda el
// log de eventos por sesion bajo un mutex y deriva los mensajes al vuelo.
type MemoryStore struct {
	mu          sync.Mutex
	sessions    map[string][]SessionEvent
	epochs      map[string]ContextEpoch
	checkpoints map[string]CompactionCheckpoint
	// lastSeen marca el orden global de insercion del ultimo evento de cada
	// sesion: el equivalente en memoria del MAX(rowid) que ordena Sessions por
	// recencia. Un contador monotonico global lo alimenta en cada AppendEvent.
	lastSeen     map[string]int
	lastActivity map[string]time.Time
	clock        int
}

// NewMemoryStore crea un store vacio listo para usar.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		sessions:     make(map[string][]SessionEvent),
		epochs:       make(map[string]ContextEpoch),
		checkpoints:  make(map[string]CompactionCheckpoint),
		lastSeen:     make(map[string]int),
		lastActivity: make(map[string]time.Time),
	}
}

// var _ Store = (*MemoryStore)(nil) asegura en tiempo de compilacion que
// MemoryStore cumple la interface.
var _ Store = (*MemoryStore)(nil)
var _ CompactionStore = (*MemoryStore)(nil)
var _ UndoStore = (*MemoryStore)(nil)

// AppendEvent agrega ev al log durable de sessionID, le asigna el siguiente Seq
// monotonico y lo devuelve. Crea la sesion si es su primer evento. El SessionID
// y Seq que traiga ev se ignoran: los fija el Store.
func (s *MemoryStore) AppendEvent(ctx context.Context, sessionID string, ev SessionEvent) (Seq, error) {
	if ev.Kind == KindContextCompacted || ev.Compaction != nil {
		return 0, ErrCompactionRequiresCommit
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	log := s.sessions[sessionID]
	seq := Seq(len(log) + 1)
	ev = cloneSessionEvent(ev)
	ev.SessionID = sessionID
	ev.Seq = seq
	s.sessions[sessionID] = append(log, ev)
	if ev.Kind == KindPromptCheckpointReverted {
		epoch := s.epochs[sessionID]
		epoch.Revision++
		s.epochs[sessionID] = epoch
	}
	s.clock++
	s.lastSeen[sessionID] = s.clock
	s.lastActivity[sessionID] = time.Now().UTC()
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

	return foldMessages(EffectiveEvents(log), sinceSeq), nil
}

// Sessions devuelve un resumen por sesion con al menos un evento, ordenado por
// actividad mas reciente primero. Replica el orden por rowid del store durable
// usando el indice del ultimo evento agregado a cada log; el Title es el primer
// mensaje del usuario de la sesion (truncado), "" si aun no hay uno.
func (s *MemoryStore) Sessions(ctx context.Context) ([]SessionSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	type entry struct {
		id           string
		title        string
		cwd          string
		last         int // posicion global del ultimo evento: aproxima MAX(rowid)
		lastActivity time.Time
	}
	entries := make([]entry, 0, len(s.sessions))
	for id, raw := range s.sessions {
		log := EffectiveEvents(raw)
		if len(log) == 0 {
			continue
		}
		// El titulo generado (ultimo Session.Title) gana sobre el primer mensaje del
		// usuario, que queda como fallback si aun no se genero ninguno. El Cwd es el
		// ultimo Session.Cwd (la carpeta vigente de la sesion).
		firstUser, generated, cwd := "", "", ""
		for _, ev := range log {
			if ev.Kind == KindSessionTitle {
				generated = ev.Text
				continue
			}
			if ev.Kind == KindSessionCwd {
				cwd = ev.Text
				continue
			}
			if firstUser == "" && ev.Message != nil && ev.Message.Role == RoleUser {
				firstUser = ev.Message.Text
			}
		}
		title := firstUser
		if generated != "" {
			title = generated
		}
		title = truncateTitle(title)
		// El ultimo evento del log lleva el Seq mas alto de la sesion, pero entre
		// sesiones eso no ordena por recencia. Igualamos al store durable usando un
		// contador de insercion global; aqui lo reconstruimos por el orden de
		// llegada que ya quedo en cada log via el contador del store.
		entries = append(entries, entry{
			id:           id,
			title:        title,
			cwd:          cwd,
			last:         s.lastSeen[id],
			lastActivity: s.lastActivity[id],
		})
	}
	sort.Slice(entries, func(a, b int) bool { return entries[a].last > entries[b].last })

	out := make([]SessionSummary, 0, len(entries))
	for _, e := range entries {
		out = append(out, SessionSummary{ID: e.id, Title: e.title, Cwd: e.cwd, LastActivity: e.lastActivity})
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
	for _, ev := range EffectiveEvents(log) {
		if ev.Seq > sinceSeq {
			out = append(out, cloneSessionEvent(ev))
		}
	}
	return out, nil
}

// Epoch devuelve la foto del contexto vigente de la sesion.
func (s *MemoryStore) Epoch(ctx context.Context, sessionID string) (ContextEpoch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.sessions[sessionID]; !ok {
		return ContextEpoch{}, ErrSessionNotFound
	}
	return s.epochs[sessionID], nil
}

// ContextForRunner proyecta el checkpoint vigente y los mensajes posteriores
// al baseline. Si el anchor quedo cubierto, lo rehidrata por separado.
func (s *MemoryStore) ContextForRunner(ctx context.Context, sessionID string) (RunnerContext, error) {
	if err := ctx.Err(); err != nil {
		return RunnerContext{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return RunnerContext{}, err
	}

	events, ok := s.sessions[sessionID]
	if !ok {
		return RunnerContext{}, ErrSessionNotFound
	}

	events = EffectiveEvents(events)
	epoch := s.epochs[sessionID]
	result := RunnerContext{
		Epoch: epoch,
	}
	checkpoint, ok := s.checkpoints[sessionID]
	if !ok {
		result.Messages = foldMessages(events, epoch.BaselineSeq)
		return result, nil
	}

	checkpointCopy := cloneCompactionCheckpoint(checkpoint)
	result.Checkpoint = &checkpointCopy
	result.Messages = foldMessages(events, checkpoint.PreservedFromSeq-1)
	for _, message := range foldMessages(events, 0) {
		if message.Seq == checkpoint.AnchorUserSeq && message.Seq <= epoch.BaselineSeq {
			anchor := message
			result.Anchor = &anchor
			break
		}
	}
	return result, nil
}

// CommitCompaction agrega el evento durable y avanza epoch/checkpoint como una
// unica operacion protegida por el mutex. Un epoch obsoleto no modifica estado.
func (s *MemoryStore) CommitCompaction(ctx context.Context, sessionID string, checkpoint CompactionCheckpoint) (Seq, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := ValidateCompactionCheckpoint(checkpoint); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	events, ok := s.sessions[sessionID]
	if !ok {
		return 0, ErrSessionNotFound
	}
	current := s.epochs[sessionID]
	if current != checkpoint.ExpectedEpoch || checkpoint.CoveredThroughSeq < current.BaselineSeq {
		return 0, compactionConflict("epoch mismatch: expected=%+v current=%+v covered=%d", checkpoint.ExpectedEpoch, current, checkpoint.CoveredThroughSeq)
	}
	if checkpoint.CoveredThroughSeq <= 0 || int(checkpoint.CoveredThroughSeq) > len(events) {
		return 0, compactionConflict("covered sequence out of range: covered=%d event_count=%d", checkpoint.CoveredThroughSeq, len(events))
	}
	if !validCompactionReferences(events, checkpoint) {
		return 0, compactionConflict("invalid references: covered=%d anchor=%d preserved=%d", checkpoint.CoveredThroughSeq, checkpoint.AnchorUserSeq, checkpoint.PreservedFromSeq)
	}

	seq := Seq(len(events) + 1)
	checkpointCopy := cloneCompactionCheckpoint(checkpoint)
	event := SessionEvent{
		SessionID:  sessionID,
		Seq:        seq,
		Kind:       KindContextCompacted,
		Compaction: &checkpointCopy,
	}
	s.sessions[sessionID] = append(events, cloneSessionEvent(event))
	current.BaselineSeq = checkpoint.CoveredThroughSeq
	current.Revision++
	s.epochs[sessionID] = current
	s.checkpoints[sessionID] = checkpointCopy
	s.clock++
	s.lastSeen[sessionID] = s.clock
	s.lastActivity[sessionID] = time.Now().UTC()
	return seq, nil
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
	for _, ev := range EffectiveEvents(log) {
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

func (s *MemoryStore) LatestPromptCheckpoint(ctx context.Context, sessionID string) (EffectiveCheckpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	log, ok := s.sessions[sessionID]
	if !ok {
		return EffectiveCheckpoint{}, ErrSessionNotFound
	}
	return LatestEffectiveCheckpoint(log)
}

// DeleteSession borra todos los eventos durables de la sesion. ErrSessionNotFound
// si la sesion no existe. Las demas sesiones quedan intactas.
func (s *MemoryStore) DeleteSession(ctx context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.sessions[sessionID]; !ok {
		return ErrSessionNotFound
	}
	delete(s.sessions, sessionID)
	delete(s.lastSeen, sessionID)
	delete(s.lastActivity, sessionID)
	delete(s.epochs, sessionID)
	delete(s.checkpoints, sessionID)
	return nil
}

package hashline

import "sync"

// Snapshot es una version capturada de un archivo: su texto completo, el hash de
// esa version y el set de lineas que el modelo ya vio (Seen). El edit rechaza
// anclas a lineas fuera de Seen.
type Snapshot struct {
	Path string
	Text string
	Hash string
	Seen map[int]struct{}
}

// SnapshotStore guarda el historial de versiones por path. El read solo usa
// Record/Head/RecordSeenLines; ByHash e Invalidate quedan para el edit.
type SnapshotStore interface {
	Head(path string) *Snapshot
	ByHash(path, hash string) *Snapshot
	Record(path, fullText string) string
	RecordSeenLines(path, hash string, lines []int)
	Invalidate(path string)
}

// MemSnapshotStore es un SnapshotStore en memoria, seguro para uso concurrente:
// el runner asienta tools en paralelo. El historial por path guarda el mas
// reciente primero.
type MemSnapshotStore struct {
	mu      sync.Mutex
	history map[string][]*Snapshot
}

// NewMemSnapshotStore crea un MemSnapshotStore vacio.
func NewMemSnapshotStore() *MemSnapshotStore {
	return &MemSnapshotStore{history: make(map[string][]*Snapshot)}
}

// Record computa el hash del texto completo y, si el Head del path ya tiene ese
// hash, refresca recencia sin duplicar (read-fusion); si no, antepone una nueva
// version. Devuelve el tag.
func (s *MemSnapshotStore) Record(path, fullText string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	hash := ComputeFileHash(fullText)
	hist := s.history[path]
	if len(hist) > 0 && hist[0].Hash == hash {
		// El Head ya es esta version: refrescamos recencia sin duplicar.
		head := hist[0]
		s.history[path] = append([]*Snapshot{head}, hist[1:]...)
		return hash
	}

	snap := &Snapshot{Path: path, Text: fullText, Hash: hash, Seen: map[int]struct{}{}}
	s.history[path] = append([]*Snapshot{snap}, hist...)
	return hash
}

// Head devuelve la version mas reciente del path, o nil.
func (s *MemSnapshotStore) Head(path string) *Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	hist := s.history[path]
	if len(hist) == 0 {
		return nil
	}
	return hist[0]
}

// ByHash busca en el historial del path la version de ese hash, o nil.
func (s *MemSnapshotStore) ByHash(path, hash string) *Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, snap := range s.history[path] {
		if snap.Hash == hash {
			return snap
		}
	}
	return nil
}

// RecordSeenLines marca las lineas vistas en el snapshot por path+hash; si ningun
// hash matchea, usa el Head.
func (s *MemSnapshotStore) RecordSeenLines(path, hash string, lines []int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	hist := s.history[path]
	if len(hist) == 0 {
		return
	}

	target := hist[0]
	for _, snap := range hist {
		if snap.Hash == hash {
			target = snap
			break
		}
	}
	for _, line := range lines {
		target.Seen[line] = struct{}{}
	}
}

// Invalidate borra el historial del path.
func (s *MemSnapshotStore) Invalidate(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.history, path)
}

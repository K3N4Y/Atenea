package hashline

import "sync"

const (
	defaultMaxPaths    = 30
	defaultMaxVersions = 4
	defaultMaxBytes    = 64 << 20
)

// Snapshot es una version capturada de un archivo: su texto completo, el hash de
// esa version y el set de lineas que el modelo ya vio (Seen). El edit rechaza
// anclas a lineas fuera de Seen.
type Snapshot struct {
	Path string
	Text string
	Hash string
	Seen map[int]struct{}
}

// SnapshotStore guarda el historial de versiones por path. Record devuelve
// recorded=false y un hash vacio cuando la version no puede retenerse de forma
// inequivoca: excede el presupuesto individual o colisiona con otro texto
// retenido bajo el mismo tag corto. El rechazo no muta el store.
type SnapshotStore interface {
	Head(path string) *Snapshot
	ByHash(path, hash string) *Snapshot
	Record(path, fullText string) (hash string, recorded bool)
	RecordSeenLines(path, hash string, lines []int)
	Invalidate(path string)
}

// MemSnapshotStore es un SnapshotStore en memoria, seguro para uso concurrente:
// el runner asienta tools en paralelo. El historial por path guarda el mas
// reciente primero.
type MemSnapshotStore struct {
	mu      sync.Mutex
	history map[string][]*Snapshot
	recency map[string]uint64
	clock   uint64
	bytes   int64
}

// NewMemSnapshotStore crea un MemSnapshotStore vacio.
func NewMemSnapshotStore() *MemSnapshotStore {
	return &MemSnapshotStore{history: make(map[string][]*Snapshot), recency: make(map[string]uint64)}
}

// Record computa el hash del texto completo y fusiona solo contenido identico.
// Rechaza antes de mutar una version individual sobredimensionada o un texto
// distinto cuyo tag de 16 bits ya identifica otra version retenida.
func (s *MemSnapshotStore) Record(path, fullText string) (string, bool) {
	if int64(len(fullText)) > defaultMaxBytes {
		return "", false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	hash := ComputeFileHash(fullText)
	hist := s.history[path]
	// Fusion is based on exact text, never on the collision-prone 16-bit tag.
	for i, snap := range hist {
		if snap.Text == fullText {
			hist = append([]*Snapshot{snap}, append(hist[:i], hist[i+1:]...)...)
			s.history[path] = hist
			s.touch(path)
			return hash, true
		}
	}
	for _, snap := range hist {
		if snap.Hash == hash {
			// The 16-bit tag cannot identify two different retained texts safely.
			return "", false
		}
	}

	snap := &Snapshot{Path: path, Text: fullText, Hash: hash, Seen: map[int]struct{}{}}
	s.history[path] = append([]*Snapshot{snap}, hist...)
	s.bytes += int64(len(fullText))
	if len(s.history[path]) > defaultMaxVersions {
		drop := s.history[path][defaultMaxVersions:]
		for _, old := range drop {
			s.bytes -= int64(len(old.Text))
		}
		s.history[path] = s.history[path][:defaultMaxVersions]
	}
	s.touch(path)
	s.evict(path)
	return hash, true
}

// Head devuelve una copia de la version mas reciente del path, o nil. La copia es
// defensiva: Seen se clona bajo el mutex para que el lector (p.ej. el Patcher) no
// itere el map vivo mientras RecordSeenLines lo escribe.
func (s *MemSnapshotStore) Head(path string) *Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	hist := s.history[path]
	if len(hist) == 0 {
		return nil
	}
	return copySnapshot(hist[0])
}

// ByHash busca en el historial del path la version de ese hash y devuelve una copia
// defensiva (con Seen clonado bajo el mutex), o nil. El Patcher itera snap.Seen
// fuera del mutex, asi que no debe recibir el map vivo.
func (s *MemSnapshotStore) ByHash(path, hash string) *Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	var found *Snapshot
	for _, snap := range s.history[path] {
		if snap.Hash == hash {
			// A short-tag collision cannot identify a version safely.
			if found != nil && found.Text != snap.Text {
				return nil
			}
			found = snap
		}
	}
	if found != nil {
		s.touch(path)
		return copySnapshot(found)
	}
	return nil
}

// copySnapshot devuelve una copia del snapshot con Seen clonado, para que los
// lectores no compartan el map vivo. Debe llamarse con el mutex tomado.
func copySnapshot(snap *Snapshot) *Snapshot {
	seen := make(map[int]struct{}, len(snap.Seen))
	for line := range snap.Seen {
		seen[line] = struct{}{}
	}
	cp := *snap
	cp.Seen = seen
	return &cp
}

// RecordSeenLines marks only an unambiguous known path+hash. A short hash may
// identify multiple distinct snapshots; granting provenance to either would
// make a collision choose an edit base, so zero or multiple matches are no-ops.
func (s *MemSnapshotStore) RecordSeenLines(path, hash string, lines []int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var target *Snapshot
	for _, snap := range s.history[path] {
		if snap.Hash != hash {
			continue
		}
		if target != nil && target.Text != snap.Text {
			return
		}
		target = snap
	}
	if target == nil {
		return
	}
	for _, line := range lines {
		if line > 0 {
			target.Seen[line] = struct{}{}
		}
	}
	s.touch(path)
}

func (s *MemSnapshotStore) touch(path string) { s.clock++; s.recency[path] = s.clock }

func (s *MemSnapshotStore) evict(protectedPath string) {
	for len(s.history) > defaultMaxPaths || s.bytes > defaultMaxBytes {
		var victim string
		var oldest uint64
		for path, stamp := range s.recency {
			if path == protectedPath {
				continue
			}
			if victim == "" || stamp < oldest || (stamp == oldest && path < victim) {
				victim, oldest = path, stamp
			}
		}
		if victim != "" {
			for _, snap := range s.history[victim] {
				s.bytes -= int64(len(snap.Text))
			}
			delete(s.history, victim)
			delete(s.recency, victim)
			continue
		}

		hist := s.history[protectedPath]
		if s.bytes <= defaultMaxBytes || len(hist) <= 1 {
			return
		}
		oldestSnapshot := hist[len(hist)-1]
		s.bytes -= int64(len(oldestSnapshot.Text))
		s.history[protectedPath] = hist[:len(hist)-1]
	}
}

// Invalidate borra el historial del path.
func (s *MemSnapshotStore) Invalidate(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, snap := range s.history[path] {
		s.bytes -= int64(len(snap.Text))
	}
	delete(s.history, path)
	delete(s.recency, path)
}

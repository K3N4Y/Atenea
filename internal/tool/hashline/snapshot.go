package hashline

import (
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf16"
)

const (
	defaultMaxPaths    = 30
	defaultMaxVersions = 4
	defaultMaxBytes    = 64 << 20
	maxSnapshotBytes   = 4 << 20
)

// Snapshot es una version capturada de un archivo: su texto completo, el hash de
// esa version y el set de lineas que el modelo ya vio (Seen). El edit rechaza
// anclas a lineas fuera de Seen.
type Snapshot struct {
	Path    string
	Text    string
	Hash    string
	Version uint64
	Seen    map[int]struct{}
}

// SnapshotStore retains collision-aware histories and line provenance.
type SnapshotStore interface {
	Head(path string) *Snapshot
	ByHash(path, hash string) *Snapshot
	Candidates(path, hash string) []*Snapshot
	ByContent(fullText string) []*Snapshot
	FindByHash(hash string) []*Snapshot
	ReadOnly() SnapshotStore
	Record(path, fullText string) (hash string, recorded bool)
	RecordSeenLines(path, hash string, lines []int)
	RecordSeenSnapshot(path string, version uint64, lines []int)
	RecordSeenContent(path, fullText string, lines []int)
	Invalidate(path string)
	Relocate(from, to string)
	Clear()
}

// MemSnapshotStore es un SnapshotStore en memoria, seguro para uso concurrente:
// el runner asienta tools en paralelo. El historial por path guarda el mas
// reciente primero.
type MemSnapshotStore struct {
	mu      sync.Mutex
	history map[string][]*Snapshot
	recency map[string]uint64
	clock   uint64
	version uint64
	bytes   int64
}

// NewMemSnapshotStore crea un MemSnapshotStore vacio.
func NewMemSnapshotStore() *MemSnapshotStore {
	return &MemSnapshotStore{history: make(map[string][]*Snapshot), recency: make(map[string]uint64)}
}

// ReadOnly returns an isolated lookup view. Its mutation methods are no-ops and
// successful lookups do not update LRU recency.
func (s *MemSnapshotStore) ReadOnly() SnapshotStore { return readOnlySnapshotStore{s} }

type readOnlySnapshotStore struct{ store *MemSnapshotStore }

func (v readOnlySnapshotStore) Head(path string) *Snapshot { return v.store.Head(path) }
func (v readOnlySnapshotStore) ByHash(path, hash string) *Snapshot {
	candidates := v.store.candidates(path, hash, false)
	if len(candidates) == 0 {
		return nil
	}
	return candidates[0]
}
func (v readOnlySnapshotStore) Candidates(path, hash string) []*Snapshot {
	return v.store.candidates(path, hash, false)
}
func (v readOnlySnapshotStore) ByContent(text string) []*Snapshot {
	return v.store.ByContent(text)
}
func (v readOnlySnapshotStore) FindByHash(hash string) []*Snapshot {
	return v.store.FindByHash(hash)
}
func (readOnlySnapshotStore) Record(string, string) (string, bool)     { return "", false }
func (readOnlySnapshotStore) RecordSeenLines(string, string, []int)    {}
func (readOnlySnapshotStore) RecordSeenSnapshot(string, uint64, []int) {}
func (readOnlySnapshotStore) RecordSeenContent(string, string, []int)  {}
func (readOnlySnapshotStore) Invalidate(string)                        {}
func (readOnlySnapshotStore) Relocate(string, string)                  {}
func (readOnlySnapshotStore) Clear()                                   {}
func (v readOnlySnapshotStore) ReadOnly() SnapshotStore                { return v }

// Record computes the hash and merges only byte-identical content. Distinct
// texts with the same short tag are deliberately retained.
func (s *MemSnapshotStore) Record(path, fullText string) (string, bool) {
	path = canonicalSnapshotPath(path)
	fullText = normalizeSnapshotText(fullText)
	units := int64(len(utf16.Encode([]rune(fullText))))
	if units > maxSnapshotBytes {
		return "", false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	hash := ComputeFileHash(fullText)
	hist := s.history[path]
	for i, snap := range hist {
		if snap.Text == fullText {
			hist = append([]*Snapshot{snap}, append(hist[:i], hist[i+1:]...)...)
			s.history[path] = hist
			s.touch(path)
			return hash, true
		}
	}
	s.version++
	snap := &Snapshot{Path: path, Text: fullText, Hash: hash, Version: s.version, Seen: map[int]struct{}{}}
	s.history[path] = append([]*Snapshot{snap}, hist...)
	s.bytes += units
	if len(s.history[path]) > defaultMaxVersions {
		for _, old := range s.history[path][defaultMaxVersions:] {
			s.bytes -= snapshotUnits(old.Text)
		}
		s.history[path] = s.history[path][:defaultMaxVersions]
	}
	s.touch(path)
	s.evict(path)
	return hash, true
}

func normalizeSnapshotText(text string) string {
	text = strings.TrimPrefix(text, "\uFEFF")
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
}

func snapshotUnits(text string) int64 { return int64(len(utf16.Encode([]rune(text)))) }

// Existing paths resolve through symlinks. For a not-yet-created destination,
// resolving the nearest existing parent gives it the same canonical namespace.
func canonicalSnapshotPath(path string) string {
	path = filepath.Clean(path)
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	parent, tail := filepath.Dir(path), []string{filepath.Base(path)}
	for parent != filepath.Dir(parent) {
		if real, err := filepath.EvalSymlinks(parent); err == nil {
			parts := append([]string{real}, tail...)
			return filepath.Join(parts...)
		}
		tail = append([]string{filepath.Base(parent)}, tail...)
		parent = filepath.Dir(parent)
	}
	return path
}

// Head devuelve una copia de la version mas reciente del path, o nil. La copia es
// defensiva: Seen se clona bajo el mutex para que el lector (p.ej. el Patcher) no
// itere el map vivo mientras RecordSeenLines lo escribe.
func (s *MemSnapshotStore) Head(path string) *Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	hist := s.history[canonicalSnapshotPath(path)]
	if len(hist) == 0 {
		return nil
	}
	return copySnapshot(hist[0])
}

// ByHash returns the newest retained version with this tag. Callers validate
// its full text/context when recovering a stale edit.
func (s *MemSnapshotStore) ByHash(path, hash string) *Snapshot {
	candidates := s.Candidates(path, hash)
	if len(candidates) == 0 {
		return nil
	}
	return candidates[0]
}

func (s *MemSnapshotStore) Candidates(path, hash string) []*Snapshot {
	return s.candidates(path, hash, true)
}

func (s *MemSnapshotStore) candidates(path, hash string, shouldTouch bool) []*Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	path = canonicalSnapshotPath(path)
	var out []*Snapshot
	for _, snap := range s.history[path] {
		if snap.Hash == hash {
			out = append(out, copySnapshot(snap))
		}
	}
	if shouldTouch && len(out) > 0 {
		s.touch(path)
	}
	return out
}

func (s *MemSnapshotStore) ByContent(fullText string) []*Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	fullText = normalizeSnapshotText(fullText)
	var out []*Snapshot
	for _, hist := range s.history {
		for _, snap := range hist {
			if snap.Text == fullText {
				out = append(out, copySnapshot(snap))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func (s *MemSnapshotStore) FindByHash(hash string) []*Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Snapshot
	for _, hist := range s.history {
		for _, snap := range hist {
			if snap.Hash == hash {
				out = append(out, copySnapshot(snap))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
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

// RecordSeenLines is conservative because a short hash does not identify a
// version when distinct retained contents collide.
func (s *MemSnapshotStore) RecordSeenLines(path, hash string, lines []int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path = canonicalSnapshotPath(path)
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
	s.recordSeenLocked(path, target, lines)
}

func (s *MemSnapshotStore) RecordSeenSnapshot(path string, version uint64, lines []int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path = canonicalSnapshotPath(path)
	for _, snap := range s.history[path] {
		if snap.Version == version {
			s.recordSeenLocked(path, snap, lines)
			return
		}
	}
}

func (s *MemSnapshotStore) RecordSeenContent(path, fullText string, lines []int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, fullText = canonicalSnapshotPath(path), normalizeSnapshotText(fullText)
	for _, snap := range s.history[path] {
		if snap.Text == fullText {
			s.recordSeenLocked(path, snap, lines)
			return
		}
	}
}

func (s *MemSnapshotStore) recordSeenLocked(path string, target *Snapshot, lines []int) {
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
				s.bytes -= snapshotUnits(snap.Text)
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
		s.bytes -= snapshotUnits(oldestSnapshot.Text)
		s.history[protectedPath] = hist[:len(hist)-1]
	}
}

// Invalidate borra el historial del path.
func (s *MemSnapshotStore) Invalidate(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path = canonicalSnapshotPath(path)
	for _, snap := range s.history[path] {
		s.bytes -= snapshotUnits(snap.Text)
	}
	delete(s.history, path)
	delete(s.recency, path)
}

// Relocate merges source and destination histories by exact text, preserving
// newest-first order and provenance from both sides.
func (s *MemSnapshotStore) Relocate(from, to string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	from, to = canonicalSnapshotPath(from), canonicalSnapshotPath(to)
	merged := append(append([]*Snapshot(nil), s.history[from]...), s.history[to]...)
	sort.SliceStable(merged, func(i, j int) bool { return merged[i].Version > merged[j].Version })
	var out []*Snapshot
	for _, snap := range merged {
		var existing *Snapshot
		for _, kept := range out {
			if kept.Text == snap.Text {
				existing = kept
				break
			}
		}
		if existing != nil {
			for n := range snap.Seen {
				existing.Seen[n] = struct{}{}
			}
			continue
		}
		cp := copySnapshot(snap)
		cp.Path = to
		out = append(out, cp)
	}
	for _, snap := range s.history[from] {
		s.bytes -= snapshotUnits(snap.Text)
	}
	for _, snap := range s.history[to] {
		s.bytes -= snapshotUnits(snap.Text)
	}
	delete(s.history, from)
	delete(s.recency, from)
	if len(out) > defaultMaxVersions {
		out = out[:defaultMaxVersions]
	}
	if len(out) == 0 {
		delete(s.history, to)
		delete(s.recency, to)
		return
	}
	s.history[to] = out
	for _, snap := range out {
		s.bytes += snapshotUnits(snap.Text)
	}
	s.touch(to)
	s.evict(to)
}

func (s *MemSnapshotStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = make(map[string][]*Snapshot)
	s.recency = make(map[string]uint64)
	s.bytes, s.clock = 0, 0
}

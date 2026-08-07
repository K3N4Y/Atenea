package learning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Store interface {
	CreateRun(context.Context, Run) (Run, bool, error)
	UpdateRun(context.Context, Run) error
	UpdateRunCAS(context.Context, Status, Run) (bool, error)
	Run(context.Context, string) (Run, error)
	Runs(context.Context, string) ([]Run, error)
	Approve(context.Context, string, Candidate, Lesson) (Lesson, error)
	Reject(context.Context, string) error
	Lessons(context.Context, string) ([]Lesson, error)
	SetLessonEnabled(context.Context, string, bool) (Lesson, error)
	DeleteLesson(context.Context, string) (Lesson, error)
	Workspaces(context.Context) ([]string, error)
}

type RunLease interface {
	Release() error
}

type runLeaseStore interface {
	TryRunLease(string) (RunLease, bool, error)
}

type MemoryStore struct {
	mu      sync.Mutex
	runs    map[string]Run
	lessons map[string]Lesson
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{runs: map[string]Run{}, lessons: map[string]Lesson{}}
}
func (s *MemoryStore) CreateRun(_ context.Context, r Run) (Run, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, old := range s.runs {
		if old.Workspace == r.Workspace && old.SessionID == r.SessionID && old.CutSeq == r.CutSeq {
			if old.Status == Failed || old.Status == Cancelled || old.Status == Interrupted {
				continue
			}
			return old, false, nil
		}
	}
	s.runs[r.ID] = r
	return r, true, nil
}
func (s *MemoryStore) UpdateRun(_ context.Context, r Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runs[r.ID]; !ok {
		return ErrNotFound
	}
	s.runs[r.ID] = r
	return nil
}
func (s *MemoryStore) UpdateRunCAS(_ context.Context, expected Status, r Run) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.runs[r.ID]
	if !ok {
		return false, ErrNotFound
	}
	if old.Status != expected {
		return false, nil
	}
	if !CanTransition(expected, r.Status) {
		return false, ErrInvalidTransition
	}
	s.runs[r.ID] = r
	return true, nil
}
func (s *MemoryStore) Run(_ context.Context, id string) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return Run{}, ErrNotFound
	}
	return r, nil
}
func (s *MemoryStore) Runs(_ context.Context, w string) ([]Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Run
	for _, r := range s.runs {
		if r.Workspace == w {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}
func (s *MemoryStore) Approve(_ context.Context, id string, c Candidate, l Lesson) (Lesson, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return Lesson{}, ErrNotFound
	}
	if r.Status == Approved {
		for _, x := range s.lessons {
			if x.RunID == id {
				return x, nil
			}
		}
	}
	if r.Status != Ready {
		return Lesson{}, ErrInvalidTransition
	}
	if err := ValidateCandidate(c); err != nil {
		return Lesson{}, err
	}
	r.Status = Approved
	now := l.CreatedAt
	r.DecidedAt = &now
	r.Candidate = &c
	l.Candidate = c
	s.runs[id] = r
	s.lessons[l.ID] = l
	return l, nil
}
func (s *MemoryStore) Reject(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return ErrNotFound
	}
	if r.Status == Rejected {
		return nil
	}
	if r.Status != Ready {
		return ErrInvalidTransition
	}
	r.Status = Rejected
	now := time.Now().UTC()
	r.DecidedAt = &now
	s.runs[id] = r
	return nil
}
func (s *MemoryStore) Lessons(_ context.Context, w string) ([]Lesson, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Lesson
	for _, l := range s.lessons {
		if l.Workspace == w {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
func (s *MemoryStore) SetLessonEnabled(_ context.Context, id string, enabled bool) (Lesson, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.lessons[id]
	if !ok {
		return Lesson{}, ErrNotFound
	}
	l.Enabled = enabled
	s.lessons[id] = l
	return l, nil
}
func (s *MemoryStore) DeleteLesson(_ context.Context, id string) (Lesson, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.lessons[id]
	if !ok {
		return Lesson{}, ErrNotFound
	}
	l.Deleted = true
	l.Enabled = false
	s.lessons[id] = l
	return l, nil
}
func (s *MemoryStore) Workspaces(_ context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	for _, r := range s.runs {
		seen[r.Workspace] = true
	}
	out := make([]string, 0, len(seen))
	for w := range seen {
		out = append(out, w)
	}
	sort.Strings(out)
	return out, nil
}

type FileStore struct {
	*MemoryStore
	path string
}

type diskState struct {
	Runs    map[string]Run    `json:"runs"`
	Lessons map[string]Lesson `json:"lessons"`
}

func OpenFileStore(path string) (*FileStore, error) {
	f := &FileStore{MemoryStore: NewMemoryStore(), path: path}
	if err := f.withDiskLock(func() error { return nil }); err != nil {
		return nil, err
	}
	return f, nil
}

func (s *FileStore) withDiskLock(fn func() error) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	lock, err := lockFile(s.path + ".lock")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.unlock()) }()
	if err := s.reloadState(); err != nil {
		return err
	}
	return fn()
}

type fileRunLease struct {
	lock *fileLock
	once sync.Once
	err  error
}

func (l *fileRunLease) Release() error {
	l.once.Do(func() { l.err = l.lock.unlock() })
	return l.err
}

// TryRunLease non-blockingly acquires this store's stable, path-safe lease for id.
func (s *FileStore) TryRunLease(id string) (RunLease, bool, error) {
	dir := s.path + ".leases"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, false, err
	}
	sum := sha256.Sum256([]byte(id))
	lock, acquired, err := tryLockFile(filepath.Join(dir, hex.EncodeToString(sum[:])+".lock"))
	if err != nil || !acquired {
		return nil, acquired, err
	}
	return &fileRunLease{lock: lock}, true, nil
}

func (s *FileStore) reloadState() error {
	usedTmp := false
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		if tmp, tmpErr := os.ReadFile(s.path + ".tmp"); tmpErr == nil {
			b, err, usedTmp = tmp, nil, true
		} else {
			s.runs = map[string]Run{}
			s.lessons = map[string]Lesson{}
			return nil
		}
	}
	if err != nil {
		return err
	}
	var d diskState
	if err = json.Unmarshal(b, &d); err != nil {
		if tmp, tmpErr := os.ReadFile(s.path + ".tmp"); tmpErr == nil && json.Unmarshal(tmp, &d) == nil {
			usedTmp = true
		} else {
			return err
		}
	}
	if d.Runs == nil {
		d.Runs = map[string]Run{}
	}
	if d.Lessons == nil {
		d.Lessons = map[string]Lesson{}
	}
	if _, statErr := os.Stat(s.path + ".tmp"); statErr == nil {
		if usedTmp {
			if err := replaceFile(s.path+".tmp", s.path); err != nil {
				return err
			}
		} else if err := os.Remove(s.path + ".tmp"); err != nil {
			return err
		}
	}
	s.runs, s.lessons = d.Runs, d.Lessons
	return nil
}

func (s *FileStore) persistState(runs map[string]Run, lessons map[string]Lesson) error {
	b, err := json.MarshalIndent(diskState{runs, lessons}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = replaceFile(tmp, s.path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(s.path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *FileStore) CreateRun(_ context.Context, r Run) (out Run, created bool, err error) {
	err = s.withDiskLock(func() error {
		for _, old := range s.runs {
			if old.Workspace == r.Workspace && old.SessionID == r.SessionID && old.CutSeq == r.CutSeq && old.Status != Failed && old.Status != Cancelled && old.Status != Interrupted {
				out = old
				return nil
			}
		}
		runs := cloneRuns(s.runs)
		runs[r.ID] = r
		if err := s.persistState(runs, s.lessons); err != nil {
			return err
		}
		s.runs, out, created = runs, r, true
		return nil
	})
	return
}

func (s *FileStore) UpdateRun(_ context.Context, r Run) error {
	return s.withDiskLock(func() error {
		if _, ok := s.runs[r.ID]; !ok {
			return ErrNotFound
		}
		runs := cloneRuns(s.runs)
		runs[r.ID] = r
		if err := s.persistState(runs, s.lessons); err != nil {
			return err
		}
		s.runs = runs
		return nil
	})
}

func (s *FileStore) UpdateRunCAS(_ context.Context, expected Status, r Run) (updated bool, err error) {
	err = s.withDiskLock(func() error {
		old, ok := s.runs[r.ID]
		if !ok {
			return ErrNotFound
		}
		if old.Status != expected {
			return nil
		}
		if !CanTransition(expected, r.Status) {
			return ErrInvalidTransition
		}
		runs := cloneRuns(s.runs)
		runs[r.ID] = r
		if err := s.persistState(runs, s.lessons); err != nil {
			return err
		}
		s.runs, updated = runs, true
		return nil
	})
	return
}

func (s *FileStore) Run(_ context.Context, id string) (out Run, err error) {
	err = s.withDiskLock(func() error {
		var ok bool
		out, ok = s.runs[id]
		if !ok {
			return ErrNotFound
		}
		return nil
	})
	return
}

func (s *FileStore) Runs(_ context.Context, workspace string) (out []Run, err error) {
	err = s.withDiskLock(func() error {
		for _, r := range s.runs {
			if r.Workspace == workspace {
				out = append(out, r)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
		return nil
	})
	return
}

func (s *FileStore) Approve(_ context.Context, id string, c Candidate, l Lesson) (out Lesson, err error) {
	err = s.withDiskLock(func() error {
		r, ok := s.runs[id]
		if !ok {
			return ErrNotFound
		}
		if r.Status == Approved {
			for _, x := range s.lessons {
				if x.RunID == id {
					out = x
					return nil
				}
			}
		}
		if r.Status != Ready {
			return ErrInvalidTransition
		}
		if err := ValidateCandidate(c); err != nil {
			return err
		}
		runs, lessons := cloneRuns(s.runs), cloneLessons(s.lessons)
		r.Status = Approved
		now := l.CreatedAt
		r.DecidedAt, r.Candidate, l.Candidate = &now, &c, c
		runs[id], lessons[l.ID] = r, l
		if err := s.persistState(runs, lessons); err != nil {
			return err
		}
		s.runs, s.lessons, out = runs, lessons, l
		return nil
	})
	return
}

func (s *FileStore) Reject(_ context.Context, id string) error {
	return s.withDiskLock(func() error {
		r, ok := s.runs[id]
		if !ok {
			return ErrNotFound
		}
		if r.Status == Rejected {
			return nil
		}
		if r.Status != Ready {
			return ErrInvalidTransition
		}
		runs := cloneRuns(s.runs)
		r.Status = Rejected
		now := time.Now().UTC()
		r.DecidedAt, runs[id] = &now, r
		if err := s.persistState(runs, s.lessons); err != nil {
			return err
		}
		s.runs = runs
		return nil
	})
}

func (s *FileStore) Lessons(_ context.Context, workspace string) (out []Lesson, err error) {
	err = s.withDiskLock(func() error {
		for _, l := range s.lessons {
			if l.Workspace == workspace {
				out = append(out, l)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
		return nil
	})
	return
}

func (s *FileStore) SetLessonEnabled(_ context.Context, id string, enabled bool) (out Lesson, err error) {
	err = s.withDiskLock(func() error {
		l, ok := s.lessons[id]
		if !ok {
			return ErrNotFound
		}
		l.Enabled = enabled
		lessons := cloneLessons(s.lessons)
		lessons[id] = l
		if err := s.persistState(s.runs, lessons); err != nil {
			return err
		}
		s.lessons, out = lessons, l
		return nil
	})
	return
}

func (s *FileStore) DeleteLesson(_ context.Context, id string) (out Lesson, err error) {
	err = s.withDiskLock(func() error {
		l, ok := s.lessons[id]
		if !ok {
			return ErrNotFound
		}
		l.Deleted = true
		l.Enabled = false
		lessons := cloneLessons(s.lessons)
		lessons[id] = l
		if err := s.persistState(s.runs, lessons); err != nil {
			return err
		}
		s.lessons, out = lessons, l
		return nil
	})
	return
}

func (s *FileStore) Workspaces(_ context.Context) (out []string, err error) {
	err = s.withDiskLock(func() error {
		seen := map[string]bool{}
		for _, r := range s.runs {
			seen[r.Workspace] = true
		}
		for workspace := range seen {
			out = append(out, workspace)
		}
		sort.Strings(out)
		return nil
	})
	return
}

func cloneRuns(in map[string]Run) map[string]Run {
	out := make(map[string]Run, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneLessons(in map[string]Lesson) map[string]Lesson {
	out := make(map[string]Lesson, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

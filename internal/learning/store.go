package learning

import (
	"context"
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
	UpdateLesson(context.Context, Lesson) error
	Workspaces(context.Context) ([]string, error)
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
func (s *MemoryStore) UpdateLesson(_ context.Context, l Lesson) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.lessons[l.ID]; !ok {
		return ErrNotFound
	}
	s.lessons[l.ID] = l
	return nil
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
	usedTmp := false
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if tmp, tmpErr := os.ReadFile(path + ".tmp"); tmpErr == nil {
			b = tmp
			err = nil
			usedTmp = true
		} else {
			return f, nil
		}
	}
	if err != nil {
		return nil, err
	}
	var d diskState
	if err = json.Unmarshal(b, &d); err != nil {
		if tmp, tmpErr := os.ReadFile(path + ".tmp"); tmpErr == nil && json.Unmarshal(tmp, &d) == nil {
			b = tmp
			usedTmp = true
		} else {
			return nil, err
		}
	}
	f.runs = d.Runs
	f.lessons = d.Lessons
	if f.runs == nil {
		f.runs = map[string]Run{}
	}
	if f.lessons == nil {
		f.lessons = map[string]Lesson{}
	}
	if _, statErr := os.Stat(path + ".tmp"); statErr == nil {
		if usedTmp {
			if renameErr := os.Rename(path+".tmp", path); renameErr != nil {
				return nil, renameErr
			}
		} else if removeErr := os.Remove(path + ".tmp"); removeErr != nil {
			return nil, removeErr
		}
	}
	return f, nil
}
func (s *FileStore) persistState(runs map[string]Run, lessons map[string]Lesson) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(diskState{runs, lessons}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
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
	if err = os.Rename(tmp, s.path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(s.path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (s *FileStore) CreateRun(ctx context.Context, r Run) (Run, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, old := range s.runs {
		if old.Workspace == r.Workspace && old.SessionID == r.SessionID && old.CutSeq == r.CutSeq && old.Status != Failed && old.Status != Cancelled && old.Status != Interrupted {
			return old, false, nil
		}
	}
	runs := cloneRuns(s.runs)
	runs[r.ID] = r
	if err := s.persistState(runs, s.lessons); err != nil {
		return Run{}, false, err
	}
	s.runs = runs
	return r, true, nil
}
func (s *FileStore) UpdateRun(ctx context.Context, r Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
}
func (s *FileStore) UpdateRunCAS(ctx context.Context, expected Status, r Run) (bool, error) {
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
	runs := cloneRuns(s.runs)
	runs[r.ID] = r
	if err := s.persistState(runs, s.lessons); err != nil {
		return false, err
	}
	s.runs = runs
	return true, nil
}
func (s *FileStore) Approve(ctx context.Context, id string, c Candidate, l Lesson) (Lesson, error) {
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
	runs := cloneRuns(s.runs)
	lessons := cloneLessons(s.lessons)
	r.Status = Approved
	now := l.CreatedAt
	r.DecidedAt = &now
	r.Candidate = &c
	l.Candidate = c
	runs[id] = r
	lessons[l.ID] = l
	if err := s.persistState(runs, lessons); err != nil {
		return Lesson{}, err
	}
	s.runs, s.lessons = runs, lessons
	return l, nil
}
func (s *FileStore) Reject(ctx context.Context, id string) error {
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
	runs := cloneRuns(s.runs)
	r.Status = Rejected
	now := time.Now().UTC()
	r.DecidedAt = &now
	runs[id] = r
	if err := s.persistState(runs, s.lessons); err != nil {
		return err
	}
	s.runs = runs
	return nil
}
func (s *FileStore) UpdateLesson(ctx context.Context, l Lesson) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.lessons[l.ID]; !ok {
		return ErrNotFound
	}
	lessons := cloneLessons(s.lessons)
	lessons[l.ID] = l
	if err := s.persistState(s.runs, lessons); err != nil {
		return err
	}
	s.lessons = lessons
	return nil
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

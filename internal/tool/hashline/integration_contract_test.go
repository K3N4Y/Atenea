package hashline

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestParsePatchStrictShape(t *testing.T) {
	bad := []string{
		"", "[a#ABCD]\n", "[a#ABCD]\nBOGUS", "[a#ABCD]\nINS.HEAD:x\n+y",
		"[a#ABCD]\nSWAP 1.=1:", "[a#ABCD]\nDEL 1\n[b#1234]\nDEL 1",
		"[a#ABCD]\nDEL 1\nSWAP 1.=2:\n+x",
	}
	for _, in := range bad {
		if _, err := ParsePatch(in); err == nil {
			t.Errorf("ParsePatch(%q) unexpectedly succeeded", in)
		}
	}
}

func TestPatcherInvalidShapeNeverReadsOrWrites(t *testing.T) {
	fs := &fakePatchFS{files: map[string][]byte{}, writes: map[string][]byte{}}
	for _, p := range []Patch{{}, {Sections: []Section{{}, {}}}} {
		if _, err := NewPatcher(fs, NewMemSnapshotStore()).Apply(p); err == nil {
			t.Fatal("invalid shape succeeded")
		}
	}
	if len(fs.writes) != 0 {
		t.Fatalf("preflight wrote: %v", fs.writes)
	}
}

func TestSnapshotUnknownSeenAndBounds(t *testing.T) {
	s := NewMemSnapshotStore()
	h, _ := s.Record("p", "one")
	s.RecordSeenLines("p", "FFFF", []int{1})
	if _, ok := s.ByHash("p", h).Seen[1]; ok {
		t.Fatal("unknown hash granted provenance")
	}
	for i := 0; i < 5; i++ {
		s.Record("p", strings.Repeat("x", i+1))
	}
	if s.ByHash("p", h) != nil {
		t.Fatal("fifth version did not evict oldest")
	}
	for i := 0; i < 31; i++ {
		s.Record(string(rune('A'+i)), "x")
	}
	if s.Head("p") != nil {
		t.Fatal("path LRU did not evict oldest")
	}
}

func TestSnapshotHashCollisionFailsClosed(t *testing.T) {
	s := NewMemSnapshotStore()
	s.history["p"] = []*Snapshot{
		{Path: "p", Text: "first", Hash: "ABCD", Seen: map[int]struct{}{}},
		{Path: "p", Text: "second", Hash: "ABCD", Seen: map[int]struct{}{}},
	}
	s.recency["p"] = 1
	if got := s.ByHash("p", "ABCD"); got != nil {
		t.Fatalf("ambiguous collision resolved to %+v", got)
	}
	s.RecordSeenLines("p", "ABCD", []int{1})
	if len(s.history["p"][0].Seen) != 0 || len(s.history["p"][1].Seen) != 0 {
		t.Fatal("ambiguous collision granted seen-line provenance")
	}
}

func TestPatcherRecoversUniqueUniformDrift(t *testing.T) {
	const path = "/work/recover"
	old, live := "a\nb\nc\n", "new\na\nb\nc\n"
	s := NewMemSnapshotStore()
	h, _ := s.Record(path, old)
	s.RecordSeenLines(path, h, []int{2})
	fs := &fakePatchFS{files: map[string][]byte{path: []byte(live)}, writes: map[string][]byte{}}
	_, err := NewPatcher(fs, s).Apply(Patch{Sections: []Section{{Path: path, Hash: h, Edits: []Edit{{Kind: Replace, Range: Range{Start: 2, End: 2}, Text: "B"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(fs.writes[path]); got != "new\na\nB\nc\n" {
		t.Fatalf("got %q", got)
	}
}

func TestPatcherAmbiguousDriftFailsClosed(t *testing.T) {
	const path = "/work/ambiguous"
	s := NewMemSnapshotStore()
	h, _ := s.Record(path, "a\nb\n")
	s.RecordSeenLines(path, h, []int{2})
	fs := &fakePatchFS{files: map[string][]byte{path: []byte("b\na\nb\n")}, writes: map[string][]byte{}}
	_, err := NewPatcher(fs, s).Apply(Patch{Sections: []Section{{Path: path, Hash: h, Edits: []Edit{{Kind: Delete, Range: Range{Start: 2, End: 2}}}}}})
	var mismatch *MismatchError
	if !errors.As(err, &mismatch) || len(fs.writes) != 0 {
		t.Fatalf("err=%v writes=%v", err, fs.writes)
	}
}

func TestPatcherRecoveryMultipleAnchorsRequiresUniformOffset(t *testing.T) {
	const path = "/work/multi"
	old := "a\nb\nc\nd\n"
	for _, tc := range []struct {
		name, live string
		succeeds   bool
	}{
		{"uniform", "new\na\nb\nc\nd\n", true},
		{"non-uniform", "a\nb\nnew\nc\nd\n", false},
		{"changed-region", "new\na\nchanged\nc\nd\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewMemSnapshotStore()
			h, _ := s.Record(path, old)
			s.RecordSeenLines(path, h, []int{2, 4})
			fs := &fakePatchFS{files: map[string][]byte{path: []byte(tc.live)}, writes: map[string][]byte{}}
			_, err := NewPatcher(fs, s).Apply(Patch{Sections: []Section{{Path: path, Hash: h, Edits: []Edit{
				{Kind: Replace, Range: Range{Start: 2, End: 2}, Text: "B"},
				{Kind: Replace, Range: Range{Start: 4, End: 4}, Text: "D"},
			}}}})
			if (err == nil) != tc.succeeds {
				t.Fatalf("success=%v error=%v", tc.succeeds, err)
			}
		})
	}
}

func TestPatcherMixedStableAndAnchoredDriftRecoversAnchor(t *testing.T) {
	const path = "/work/mixed"
	s := NewMemSnapshotStore()
	h, _ := s.Record(path, "a\nb\n")
	s.RecordSeenLines(path, h, []int{2})
	fs := &fakePatchFS{files: map[string][]byte{path: []byte("new\na\nb\n")}, writes: map[string][]byte{}}
	_, err := NewPatcher(fs, s).Apply(Patch{Sections: []Section{{Path: path, Hash: h, Edits: []Edit{
		{Kind: Insert, Cursor: BOF, Text: "head"},
		{Kind: Replace, Range: Range{Start: 2, End: 2}, Text: "B"},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(fs.writes[path]); got != "head\nnew\na\nB\n" {
		t.Fatalf("got %q", got)
	}
}

func TestOSFilesystemPreservesBOMCRLFAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "script")
	mode := os.FileMode(0o751)
	if runtime.GOOS != "windows" {
		mode |= os.ModeSetuid
	}
	if err := os.WriteFile(path, []byte("\xEF\xBB\xBFa\r\nb\r\n"), mode.Perm()); err != nil {
		t.Fatal(err)
	}
	if mode&os.ModeSetuid != 0 {
		if err := os.Chmod(path, mode); err != nil {
			t.Skipf("special mode bits unsupported: %v", err)
		}
		if info, _ := os.Stat(path); info.Mode()&os.ModeSetuid == 0 {
			t.Skip("filesystem does not retain setuid bit")
		}
	}
	s := NewMemSnapshotStore()
	h, _ := s.Record(path, "a\nb\n")
	s.RecordSeenLines(path, h, []int{2})
	_, err := NewPatcher(OSFilesystem{}, s).Apply(Patch{Sections: []Section{{Path: path, Hash: h, Edits: []Edit{{Kind: Replace, Range: Range{Start: 2, End: 2}, Text: "B"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "\xEF\xBB\xBFa\r\nB\r\n" {
		t.Fatalf("bytes %q", b)
	}
	info, _ := os.Stat(path)
	mask := os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	if info.Mode()&mask != mode&mask {
		t.Fatalf("mode %v, want %v", info.Mode(), mode)
	}
}

func TestPatcherSerializesSamePathReadThroughCommit(t *testing.T) {
	const path = "/work/race"
	original := "a\nb\n"
	s := NewMemSnapshotStore()
	h, _ := s.Record(path, original)
	s.RecordSeenLines(path, h, []int{2})
	fs := &serializingFS{files: map[string][]byte{path: []byte(original)}, firstRead: make(chan struct{}), release: make(chan struct{})}
	p := NewPatcher(fs, s)
	patch := func(text string) Patch {
		return Patch{Sections: []Section{{Path: path, Hash: h, Edits: []Edit{{Kind: Replace, Range: Range{Start: 2, End: 2}, Text: text}}}}}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	var e1, e2 error
	go func() { defer wg.Done(); _, e1 = p.Apply(patch("first")) }()
	<-fs.firstRead
	go func() { defer wg.Done(); _, e2 = p.Apply(patch("second")) }()
	close(fs.release)
	wg.Wait()
	if e1 != nil {
		t.Fatalf("first apply: %v", e1)
	}
	var mismatch *MismatchError
	if !errors.As(e2, &mismatch) {
		t.Fatalf("second apply should observe committed drift, got %v", e2)
	}
	if got := string(fs.files[path]); got != "a\nfirst\n" {
		t.Fatalf("last rename won: %q", got)
	}
}

type serializingFS struct {
	mu        sync.Mutex
	files     map[string][]byte
	firstRead chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (f *serializingFS) ReadFile(name string) ([]byte, error) {
	f.once.Do(func() { close(f.firstRead); <-f.release })
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.files[name]...), nil
}
func (f *serializingFS) WriteFile(name string, data []byte, _ os.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[name] = append([]byte(nil), data...)
	return nil
}

func TestPatcherPostRenameFailureRecordsCommittedSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uncertain")
	original := "a\nb\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewMemSnapshotStore()
	h, _ := s.Record(path, original)
	s.RecordSeenLines(path, h, []int{2})
	fs := OSFilesystem{ReplaceHook: func(name string, data []byte, perm os.FileMode) error {
		if err := os.WriteFile(name, data, perm); err != nil {
			return err
		}
		return &CommitUncertainError{Err: errors.New("injected directory sync failure")}
	}}
	res, err := NewPatcher(fs, s).Apply(Patch{Sections: []Section{{Path: path, Hash: h, Edits: []Edit{{Kind: Replace, Range: Range{Start: 2, End: 2}, Text: "B"}}}}})
	var committed *CommittedError
	if !errors.As(err, &committed) {
		t.Fatalf("error = %T %v", err, err)
	}
	if res.Header == "" || committed.Result.Header != res.Header {
		t.Fatalf("committed result lost: %+v", committed)
	}
	if head := s.Head(path); head == nil || head.Text != "a\nB\n" {
		t.Fatalf("snapshot stale: %+v", head)
	}
}

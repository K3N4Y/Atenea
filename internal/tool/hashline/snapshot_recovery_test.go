package hashline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Provenance: behavior ported from oh-my-pi packages/hashline/test/snapshots.test.ts
// and recovery-session-chain.test.ts @ 5af71dc9cf132538e072806424f71f43f734d9ae,
// plus the deleted local snapshot_test.go and patcher_test.go baseline.
func collisionFixture(t *testing.T) (string, string) {
	t.Helper()
	seen := make(map[string]string)
	for i := 0; i < 200000; i++ {
		text := fmt.Sprintf("collision-%d", i)
		h := ComputeFileHash(text)
		if prior, ok := seen[h]; ok && prior != text {
			return prior, text
		}
		seen[h] = text
	}
	t.Fatal("deterministic collision search exhausted")
	return "", ""
}

func TestSnapshotStoreContentHistoryCollisionAndCopies(t *testing.T) {
	a, b := collisionFixture(t)
	s := NewMemSnapshotStore()
	h, ok := s.Record("p", "\ufeff"+strings.ReplaceAll(a, "-", "-\r"))
	if !ok || h != ComputeFileHash(strings.ReplaceAll(a, "-", "-\n")) {
		t.Fatalf("normalization hash=%q ok=%v", h, ok)
	}
	// Use the actual deterministic colliders for collision retention.
	s.Clear()
	h, _ = s.Record("p", a)
	s.RecordSeenContent("p", a, []int{1})
	h2, _ := s.Record("p", b)
	s.RecordSeenContent("p", b, []int{2})
	if h != h2 || len(s.Candidates("p", h)) != 2 {
		t.Fatalf("collision lost: %q %q %#v", h, h2, s.Candidates("p", h))
	}
	if got := s.ByHash("p", h); got == nil || got.Text != b {
		t.Fatalf("newest collider=%#v", got)
	}
	byA, byB := s.ByContent(a), s.ByContent(b)
	if len(byA) != 1 || len(byB) != 1 || len(byA[0].Seen) != 1 || len(byB[0].Seen) != 1 {
		t.Fatalf("content/provenance lookup A=%#v B=%#v", byA, byB)
	}
	copyA := byA[0]
	copyA.Seen[99] = struct{}{}
	if _, leaked := s.ByContent(a)[0].Seen[99]; leaked {
		t.Fatal("ByContent leaked mutable Seen map")
	}
	if matches := s.FindByHash(h); len(matches) != 2 {
		t.Fatalf("FindByHash collision matches=%d", len(matches))
	}
	// Re-observation fuses and promotes without crossing provenance.
	s.Record("p", a)
	if s.Head("p").Text != a || len(s.Candidates("p", h)) != 2 {
		t.Fatal("identical read did not fuse/promote")
	}
}

func TestSnapshotStoreBoundsCodeUnitsLRUAndLifecycle(t *testing.T) {
	s := NewMemSnapshotStore()
	if _, ok := s.Record("too-big", strings.Repeat("x", maxSnapshotBytes+1)); ok {
		t.Fatal("accepted >4Mi UTF-16 units")
	}
	if _, ok := s.Record("astral-too-big", strings.Repeat("😀", maxSnapshotBytes/2+1)); ok {
		t.Fatal("astral UTF-16 units undercounted")
	}
	if _, ok := s.Record("at-cap", strings.Repeat("😀", maxSnapshotBytes/2)); !ok {
		t.Fatal("rejected exact 4Mi UTF-16-unit cap")
	}
	s.Clear()
	var oldest string
	for i := 0; i < defaultMaxPaths+1; i++ {
		p := fmt.Sprintf("p%02d", i)
		if i == 0 {
			oldest = p
		}
		s.Record(p, fmt.Sprintf("v%d", i))
	}
	if s.Head(oldest) != nil || s.Head("p30") == nil {
		t.Fatal("30-path LRU bound not enforced")
	}
	for i := 0; i < defaultMaxVersions+1; i++ {
		s.Record("versions", fmt.Sprintf("version-%d", i))
	}
	if len(s.history[canonicalSnapshotPath("versions")]) != defaultMaxVersions {
		t.Fatal("four-version bound not enforced")
	}
	s.Record("keep", "x")
	s.Record("drop", "y")
	s.Invalidate("drop")
	if s.Head("drop") != nil || s.Head("keep") == nil {
		t.Fatal("Invalidate affected wrong path")
	}
	s.Clear()
	if s.Head("keep") != nil || s.bytes != 0 {
		t.Fatal("Clear retained state")
	}
}

func TestSnapshotStoreCanonicalPathsRelocateMergeAndSymlinkFallback(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	s := NewMemSnapshotStore()
	ha, _ := s.Record(filepath.Join(link, "missing", "a.go"), "A")
	if s.ByHash(filepath.Join(realDir, "missing", "a.go"), ha) == nil {
		t.Fatal("nonexistent-parent symlink canonicalization diverged")
	}
	hb, _ := s.Record(filepath.Join(root, "dest"), "B")
	s.RecordSeenLines(filepath.Join(root, "dest"), hb, []int{2})
	s.RecordSeenLines(filepath.Join(realDir, "missing", "a.go"), ha, []int{1})
	s.Relocate(filepath.Join(link, "missing", "a.go"), filepath.Join(root, "dest"))
	if s.Head(filepath.Join(link, "missing", "a.go")) != nil {
		t.Fatal("source survived relocate")
	}
	if s.ByHash(filepath.Join(root, "dest"), ha) == nil || s.ByHash(filepath.Join(root, "dest"), hb) == nil {
		t.Fatal("relocate did not merge histories")
	}
}

func TestSnapshotStoreConcurrentRecordReadSeen(t *testing.T) {
	s := NewMemSnapshotStore()
	h, _ := s.Record("shared", "a\nb\n")
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				s.RecordSeenLines("shared", h, []int{i%2 + 1})
				_ = s.Head("shared")
				_ = s.ByHash("shared", h)
				_ = s.FindByHash(h)
			}
		}(i)
	}
	wg.Wait()
}

func recoveryApply(t *testing.T, old, live string, edits []Edit) (string, error) {
	t.Helper()
	const path = "/work/recovery"
	s := NewMemSnapshotStore()
	h, _ := s.Record(path, old)
	seen := make([]int, len(SplitLines(old)))
	for i := range seen {
		seen[i] = i + 1
	}
	s.RecordSeenLines(path, h, seen)
	fs := &transactionFS{files: map[string][]byte{path: []byte(live)}}
	_, err := NewPatcher(fs, s).Apply(Patch{Sections: []Section{{Path: path, Hash: h, Edits: edits}}})
	return string(fs.files[path]), err
}

func TestRecoveryChangedDeletedDuplicateSplitAndUniformAnchors(t *testing.T) {
	repl := func(n int, text string) Edit { return Edit{Kind: Replace, Range: Range{Start: n, End: n}, Text: text} }
	for _, tc := range []struct {
		name, old, live string
		edits           []Edit
		want            string
		ok              bool
	}{
		{"prepend", "a\nb\nc", "h\na\nb\nc", []Edit{repl(2, "B")}, "h\na\nB\nc", true},
		{"insert interior", "a\nb\nc", "a\nx\nb\nc", []Edit{repl(2, "B")}, "a\nx\nB\nc", true},
		{"changed target", "a\nb\nc", "a\nchanged\nc", []Edit{repl(2, "B")}, "a\nchanged\nc", false},
		{"deleted target", "a\nb\nc", "a\nc", []Edit{repl(2, "B")}, "a\nc", false},
		{"duplicate ambiguous", "b", "b\nb", []Edit{repl(1, "B")}, "b\nb", false},
		{"split range", "a\nb\nc\nd", "a\nb\nx\nc\nd", []Edit{{Kind: Replace, Range: Range{2, 3}, Text: "BC"}}, "a\nb\nx\nc\nd", false},
		{"uniform multiple", "a\nb\nc\nd", "h\na\nb\nc\nd", []Edit{repl(2, "B"), repl(4, "D")}, "h\na\nB\nc\nD", true},
		{"nonuniform multiple", "a\nb\nc\nd", "a\nb\nx\nc\nd", []Edit{repl(2, "B"), repl(4, "D")}, "a\nb\nx\nc\nd", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := recoveryApply(t, tc.old, tc.live, tc.edits)
			if (err == nil) != tc.ok {
				t.Fatalf("err=%v", err)
			}
			if got != tc.want {
				t.Fatalf("disk=%q want %q", got, tc.want)
			}
		})
	}
}

func TestRecoveryInsertCutPasteAndStableHeadTail(t *testing.T) {
	cases := []struct {
		name string
		edit Edit
		want string
	}{
		{"insert anchor", Edit{Kind: Insert, Anchor: 2, Cursor: AfterAnchor, Text: "X"}, "h\na\nb\nX\nc"},
		{"cut", Edit{Kind: Cut, Range: Range{2, 2}, Register: "r"}, "h\na\nc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := recoveryApply(t, "a\nb\nc", "h\na\nb\nc", []Edit{tc.edit})
			if err != nil || got != tc.want {
				t.Fatalf("got=%q err=%v", got, err)
			}
		})
	}
	for _, cursor := range []Cursor{BOF, EOF} {
		got, err := recoveryApply(t, "a", "drift\na", []Edit{{Kind: Insert, Cursor: cursor, Text: "X"}})
		if err != nil || !strings.Contains(got, "X") {
			t.Fatalf("stable %v got=%q err=%v", cursor, got, err)
		}
	}
}

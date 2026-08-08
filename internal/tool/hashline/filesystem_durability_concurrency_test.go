package hashline

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSameFSMoveReportsSourceDirectoryFaultsAfterRemoval(t *testing.T) {
	for _, tc := range []struct {
		name, from, to, fault string
		sameDir               bool
	}{
		{"source directory open", "/s/a", "/d/b", "sourceDirOpen", false},
		{"source directory sync", "/s/a", "/d/b", "sourceDirSync", false},
		{"source directory close", "/s/a", "/d/b", "sourceDirClose", false},
		{"same directory open deduplicated", "/d/a", "/d/b", "dirOpen", true},
		{"same directory sync deduplicated", "/d/a", "/d/b", "dirSync", true},
		{"same directory close deduplicated", "/d/a", "/d/b", "dirClose", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFaultOps(map[string]string{tc.from: "A"})
			f.fault = tc.fault
			err := durableMoveWithOps(f, tc.from, tc.to)
			var uncertain *CommitUncertainError
			if !errors.As(err, &uncertain) || !strings.Contains(err.Error(), "uncertain") {
				t.Fatalf("error = %T %v, want committed-uncertain no-retry state", err, err)
			}
			if _, ok := f.files[tc.from]; ok || string(f.files[tc.to].data) != "A" {
				t.Fatalf("visible bytes after source removal = %#v", f.files)
			}
			calls := strings.Join(f.calls, ",")
			if tc.sameDir && strings.Count(calls, "dirOpen") != 1 {
				t.Fatalf("same-directory durability was not deduplicated: %v", f.calls)
			}
		})
	}
}

func TestUncertainSameFSMoveSnapshotsDiskAndForbidsRetry(t *testing.T) {
	fs := &resultFaultFS{files: map[string][]byte{"a": []byte("A")}, reads: map[string]int{}, readbackAt: map[string]int{}, renameErr: &CommitUncertainError{Err: errors.New("source directory sync")}}
	snaps := NewMemSnapshotStore()
	h, _ := snaps.Record("a", "A")
	results, err := NewPatcher(fs, snaps).ApplyConfiguredResults(Patch{Sections: []Section{{Path: "a", Hash: h, FileOp: FileOp{MoveTo: "b"}}}}, false)
	var committed *CommittedError
	if !errors.As(err, &committed) || len(results) != 1 {
		t.Fatalf("results=%+v error=%T %v", results, err, err)
	}
	if _, exists := fs.files["a"]; exists || string(fs.files["b"]) != "A" {
		t.Fatalf("files=%v", fs.files)
	}
	if snaps.Head("a") != nil || snaps.Head("b") == nil || snaps.Head("b").Text != "A" {
		t.Fatalf("source snapshot=%#v destination snapshot=%#v", snaps.Head("a"), snaps.Head("b"))
	}
}

func TestActualFilesystemMovesAndReplacesAreConcurrentSafe(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c", "loop"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(strings.ToUpper(name)), 0644); err != nil {
			t.Fatal(err)
		}
	}
	fs := OSFilesystem{}
	deadline := time.After(10 * time.Second)
	var wg sync.WaitGroup
	// Inverse and overlapping move sets exercise real links/removes and the same
	// path locks. Existing destinations make failure an intentional valid outcome.
	moves := [][2]string{{"a", "b"}, {"b", "a"}, {"a", "c"}, {"c", "a"}}
	for _, pair := range moves {
		pair := pair
		wg.Add(1)
		go func() { defer wg.Done(); _ = fs.Rename(filepath.Join(root, pair[0]), filepath.Join(root, pair[1])) }()
	}
	for i := 0; i < 40; i++ {
		i := i
		wg.Add(1)
		go func() { defer wg.Done(); _ = fs.WriteFile(filepath.Join(root, "loop"), []byte{byte('0' + i%10)}, 0644) }()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-deadline:
		t.Fatal("actual filesystem operations deadlocked")
	}
	for _, name := range []string{"a", "b", "c"} {
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(got) != strings.ToUpper(name) {
			t.Fatalf("%s final=%q err=%v", name, got, err)
		}
	}
	loop, err := os.ReadFile(filepath.Join(root, "loop"))
	if err != nil || len(loop) != 1 || loop[0] < '0' || loop[0] > '9' {
		t.Fatalf("loop final=%q err=%v", loop, err)
	}
	patchPathLocks.Lock()
	defer patchPathLocks.Unlock()
	if len(patchPathLocks.m) != 0 {
		t.Fatalf("path locks leaked: %v", patchPathLocks.m)
	}
}

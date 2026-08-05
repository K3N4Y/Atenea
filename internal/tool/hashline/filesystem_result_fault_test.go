package hashline

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// Provenance: restored common edit durability/result coverage. These tests use
// the Filesystem seam rather than relying on host-specific filesystem faults.
type resultFaultFS struct {
	files      map[string][]byte
	reads      map[string]int
	writeErr   error
	removeErr  error
	renameErr  error
	readbackAt map[string]int
}

func (f *resultFaultFS) ReadFile(path string) ([]byte, error) {
	f.reads[path]++
	if f.readbackAt[path] > 0 && f.reads[path] >= f.readbackAt[path] {
		return nil, errors.New("injected readback failure")
	}
	b, ok := f.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), b...), nil
}
func (f *resultFaultFS) WriteFile(path string, data []byte, _ os.FileMode) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.files[path] = append([]byte(nil), data...)
	return nil
}
func (f *resultFaultFS) Remove(path string) error {
	if f.removeErr != nil {
		var uncertain *CommitUncertainError
		if errors.As(f.removeErr, &uncertain) {
			delete(f.files, path)
		}
		return f.removeErr
	}
	delete(f.files, path)
	return nil
}
func (f *resultFaultFS) Rename(from, to string) error {
	f.files[to] = f.files[from]
	delete(f.files, from)
	return f.renameErr
}
func TestCommittedWriteReadbackFailureCarriesIntendedResultAndNoRetry(t *testing.T) {
	fs := &resultFaultFS{files: map[string][]byte{"a": []byte("A")}, reads: map[string]int{}, readbackAt: map[string]int{"a": 3}}
	snaps := NewMemSnapshotStore()
	h, _ := snaps.Record("a", "A")
	p := NewPatcher(fs, snaps)
	results, err := p.ApplyConfiguredResults(Patch{Sections: []Section{{Path: "a", Hash: h, Edits: []Edit{{Kind: Replace, Range: Range{1, 1}, Text: "B"}}}}}, false)
	var committed *CommittedError
	if !errors.As(err, &committed) || len(results) != 1 || results[0].OldText != "A" || results[0].NewText != "B" || !strings.Contains(err.Error(), "do not retry") {
		t.Fatalf("results=%+v err=%v", results, err)
	}
	if string(fs.files["a"]) != "B" {
		t.Fatalf("visible committed bytes=%q", fs.files["a"])
	}
}

func TestRemoveInvalidatesSnapshotOnlyAfterVisibleMutation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantExists bool
		wantHead   bool
		committed  bool
	}{
		{name: "before mutation", err: errors.New("remove blocked"), wantExists: true, wantHead: true},
		{name: "directory sync uncertain", err: &CommitUncertainError{Err: errors.New("dir sync")}, wantExists: false, wantHead: false, committed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := &resultFaultFS{files: map[string][]byte{"a": []byte("A")}, reads: map[string]int{}, readbackAt: map[string]int{}, removeErr: tc.err}
			snaps := NewMemSnapshotStore()
			h, _ := snaps.Record("a", "A")
			p := NewPatcher(fs, snaps)
			results, err := p.ApplyConfiguredResults(Patch{Sections: []Section{{Path: "a", Hash: h, FileOp: FileOp{Remove: true}}}}, false)
			_, exists := fs.files["a"]
			if exists != tc.wantExists || (snaps.Head("a") != nil) != tc.wantHead {
				t.Fatalf("exists=%v snapshot=%#v err=%v", exists, snaps.Head("a"), err)
			}
			var committed *CommittedError
			if errors.As(err, &committed) != tc.committed || (tc.committed && len(results) != 1) {
				t.Fatalf("results=%+v err=%T %v", results, err, err)
			}
		})
	}
}

func TestMoveReadbackFailureIsStructuredCommitUncertain(t *testing.T) {
	fs := &resultFaultFS{files: map[string][]byte{"a": []byte("A")}, reads: map[string]int{}, readbackAt: map[string]int{"b": 1}}
	snaps := NewMemSnapshotStore()
	h, _ := snaps.Record("a", "A")
	results, err := NewPatcher(fs, snaps).ApplyConfiguredResults(Patch{Sections: []Section{{Path: "a", Hash: h, FileOp: FileOp{MoveTo: "b"}}}}, false)
	var committed *CommittedError
	if !errors.As(err, &committed) || len(results) != 1 || !strings.Contains(err.Error(), "do not retry") {
		t.Fatalf("results=%+v err=%v", results, err)
	}
	if _, source := fs.files["a"]; source || string(fs.files["b"]) != "A" {
		t.Fatalf("files=%v", fs.files)
	}
}

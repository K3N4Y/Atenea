package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	contract "github.com/K3N4Y/atenea/agentcore/tool"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

func TestCategoryE_SecurityIdentityHardlinkSymlinkAndCanonicalAliasTable(t *testing.T) {
	cases := []struct {
		name        string
		setup       func(t *testing.T, root, outside, source string)
		destination string
	}{
		{"destination hardlinked to source", func(t *testing.T, root, _, source string) {
			if err := os.Link(source, filepath.Join(root, "dest")); err != nil {
				t.Skip(err)
			}
		}, "dest"},
		{"destination hardlinked to third file", func(t *testing.T, root, _, source string) {
			third := filepath.Join(root, "third")
			matrixMustWrite(t, third, "third")
			if err := os.Link(third, filepath.Join(root, "dest")); err != nil {
				t.Skip(err)
			}
		}, "dest"},
		{"source and destination same inode different names", func(t *testing.T, root, _, source string) {
			if err := os.Link(source, filepath.Join(root, "alias")); err != nil {
				t.Skip(err)
			}
		}, "alias"},
		{"source has hardlink alias", func(t *testing.T, root, _, source string) {
			if err := os.Link(source, filepath.Join(root, "alias")); err != nil {
				t.Skip(err)
			}
		}, "new"},
		{"symlink destination component", func(t *testing.T, root, outside, _ string) {
			if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
				t.Skip(err)
			}
		}, "link/stolen"},
		{"symlink destination parent", func(t *testing.T, root, outside, _ string) {
			if err := os.Mkdir(filepath.Join(root, "parent"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(root, "parent", "link")); err != nil {
				t.Skip(err)
			}
		}, "parent/link/stolen"},
		{"canonical existing destination", func(t *testing.T, root, _, _ string) { matrixMustWrite(t, filepath.Join(root, "dest"), "dest") }, "./dest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			source := filepath.Join(root, "source")
			matrixMustWrite(t, source, "secret")
			tc.setup(t, root, outside, source)
			snaps := hashline.NewMemSnapshotStore()
			h, _ := snaps.Record(source, "secret")
			raw, _ := json.Marshal(map[string]string{"input": "[source#" + h + "]\nMV " + tc.destination})
			if _, err := NewEditTool(root, hashline.OSFilesystem{}, snaps).Execute(context.Background(), raw); err == nil {
				t.Fatal("unsafe identity accepted")
			}
			got, err := os.ReadFile(source)
			if err != nil || string(got) != "secret" {
				t.Fatalf("source=%q err=%v", got, err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("outside mutated: %v err=%v", entries, err)
			}
		})
	}
}
func matrixMustWrite(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
}

type matrixFaultFS struct {
	*memoryEditFS
	n, fail int
	variant string
}

func (f *matrixFaultFS) WriteFile(p string, b []byte, m os.FileMode) error {
	f.n++
	if f.variant == "write" && f.n == f.fail {
		return errors.New("injected write")
	}
	return f.memoryEditFS.WriteFile(p, b, m)
}
func (f *matrixFaultFS) Remove(p string) error {
	f.n++
	if f.variant == "remove" && f.n == f.fail {
		return errors.New("injected remove")
	}
	delete(f.files, p)
	return nil
}
func (f *matrixFaultFS) Rename(a, b string) error {
	f.n++
	if f.variant == "move" && f.n == f.fail {
		return errors.New("injected move")
	}
	f.files[b] = f.files[a]
	delete(f.files, a)
	return nil
}

func TestCategoryG_MultiSectionCommitFaultEveryPositionAndVariant(t *testing.T) {
	for _, variant := range []string{"write", "remove", "move"} {
		for _, position := range []int{1, 2, 3} {
			t.Run(variant+string(rune('0'+position)), func(t *testing.T) {
				files := map[string][]byte{"a": []byte("A"), "b": []byte("B"), "c": []byte("C")}
				fs := &matrixFaultFS{memoryEditFS: &memoryEditFS{files: files}, variant: variant, fail: position}
				snaps := hashline.NewMemSnapshotStore()
				ha, _ := snaps.Record("a", "A")
				hb, _ := snaps.Record("b", "B")
				hc, _ := snaps.Record("c", "C")
				sections := []hashline.Section{{Path: "a", Hash: ha, Edits: []hashline.Edit{{Kind: hashline.Cut, Range: hashline.Range{Start: 1, End: 1}, Register: "r1"}}}, {Path: "b", Hash: hb, Edits: []hashline.Edit{{Kind: hashline.Cut, Range: hashline.Range{Start: 1, End: 1}, Register: "r2"}}}, {Path: "c", Hash: hc, Edits: []hashline.Edit{{Kind: hashline.Cut, Range: hashline.Range{Start: 1, End: 1}, Register: "r3"}}}}
				if variant == "remove" {
					for i := range sections {
						sections[i].Edits = nil
						sections[i].FileOp.Remove = true
					}
				}
				if variant == "move" {
					for i := range sections {
						sections[i].Edits = nil
						sections[i].FileOp.MoveTo = string(rune('x' + i))
					}
				}
				results, err := hashline.NewPatcher(fs, snaps).ApplyConfiguredResults(hashline.Patch{Sections: sections}, false)
				if err == nil {
					t.Fatal("fault did not fail")
				}
				if len(results) != position-1 {
					t.Fatalf("landed prefix=%d want=%d results=%+v err=%v", len(results), position-1, results, err)
				}
			})
		}
	}
}

func TestCategoryG_ResultFilesOrderedCommittedFailedSkippedAndRetrySafe(t *testing.T) {
	fs := &nthWriteFS{memoryEditFS: &memoryEditFS{files: map[string][]byte{"a": []byte("A"), "b": []byte("B"), "c": []byte("C")}}, fail: 2}
	snaps := hashline.NewMemSnapshotStore()
	ha, _ := snaps.Record("a", "A")
	hb, _ := snaps.Record("b", "B")
	hc, _ := snaps.Record("c", "C")
	et := NewEditTool(".", fs, snaps)
	patch := "[a#" + ha + "]\nPUT 1:\n+AA\n[b#" + hb + "]\nPUT 1:\n+BB\n[c#" + hc + "]\nPUT 1:\n+CC"
	raw, _ := json.Marshal(map[string]string{"input": patch})
	res, err := et.Execute(context.Background(), raw)
	if err == nil || len(res.Files) != 3 || !res.Files[0].Committed || res.Files[1].Committed || res.Files[2].Committed || res.Files[1].Error == res.Files[2].Error {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	fs.fail = 0
	retry := "[b#" + hb + "]\nPUT 1:\n+BB\n[c#" + hc + "]\nPUT 1:\n+CC"
	raw, _ = json.Marshal(map[string]string{"input": retry})
	res, err = et.Execute(context.Background(), raw)
	if err != nil || len(res.Files) != 2 || string(fs.files["a"]) != "AA" || string(fs.files["b"]) != "BB" || string(fs.files["c"]) != "CC" {
		t.Fatalf("retry result=%+v err=%v files=%v", res, err, fs.files)
	}
}

func TestCategoryH_OutputCapPreservesFileEventMetadataTable(t *testing.T) {
	ops := []contract.FileOperation{contract.FileUpdated, contract.FileCreated, contract.FileDeleted, contract.FileMoved, contract.FileNoop, contract.FileError}
	for _, op := range ops {
		t.Run(string(op), func(t *testing.T) {
			file := contract.FileResult{Path: "p", SourcePath: "s", Destination: "d", Operation: op, OldText: "old", NewText: "new", Diff: "DIFF", Warnings: []string{"warn"}, Diagnostics: []contract.Diagnostic{{Severity: "error", Message: "diag", Line: 2}}, Header: "[p#hash]", FirstChangedLine: 2, Committed: op != contract.FileError, Error: "boom"}
			want := file
			got := NewOutputStore(3).CapResult("c", Result{Output: "model-output", Diff: "AGG", Files: []contract.FileResult{file}, Metadata: map[string]any{"partial": true}})
			if got.Output != "mod" || !got.Truncated || got.Diff != "AGG" || !reflect.DeepEqual(got.Files[0], want) || !reflect.DeepEqual(got.Metadata, map[string]any{"partial": true}) {
				t.Fatalf("capped result=%+v", got)
			}
		})
	}
	large := strings.Repeat("x", contract.SnapshotTextBudget+1)
	got := NewOutputStore(2).CapResult("large", Result{Output: "long", Files: []contract.FileResult{{OldText: large, NewText: "n", Diff: "D", Diagnostics: []contract.Diagnostic{{Message: "keep"}}}}})
	if !got.Files[0].SnapshotsPruned || got.Files[0].Diff != "D" || len(got.Files[0].Diagnostics) != 1 || got.Metadata["snapshot_text_pruned"] != true {
		t.Fatalf("pruning=%+v", got)
	}
}

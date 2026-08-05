package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	contract "github.com/K3N4Y/atenea/agentcore/tool"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

// Provenance: restored deleted local edit integration/security coverage against
// the current public hashline EditTool contract.
func TestEditMoveRejectsAliasAndOutsideDestinationsWithoutDataLoss(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	snaps := hashline.NewMemSnapshotStore()
	h, _ := snaps.Record(source, "secret")
	et := NewEditTool(root, hashline.OSFilesystem{}, snaps)

	t.Run("symlinked destination parent", func(t *testing.T) {
		if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
			t.Skip(err)
		}
		raw, _ := json.Marshal(map[string]string{"input": "[source#" + h + "]\nMV escape/stolen"})
		if _, err := et.Execute(context.Background(), raw); err == nil {
			t.Fatal("accepted destination parent escape")
		}
		if _, err := os.Stat(filepath.Join(outside, "stolen")); !os.IsNotExist(err) {
			t.Fatalf("external destination exists: %v", err)
		}
	})
	t.Run("source symlink component", func(t *testing.T) {
		if err := os.Mkdir(filepath.Join(root, "real"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "real", "x"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "alias")); err != nil {
			t.Skip(err)
		}
		ha, _ := snaps.Record(filepath.Join(root, "alias", "x"), "x")
		raw, _ := json.Marshal(map[string]string{"input": "[alias/x#" + ha + "]\nMV moved"})
		if _, err := et.Execute(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("hardlinked source", func(t *testing.T) {
		alias := filepath.Join(root, "hard")
		if err := os.Link(source, alias); err != nil {
			t.Skip(err)
		}
		raw, _ := json.Marshal(map[string]string{"input": "[source#" + h + "]\nMV moved"})
		if _, err := et.Execute(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "hardlink") {
			t.Fatalf("err=%v", err)
		}
	})
	got, _ := os.ReadFile(source)
	if string(got) != "secret" {
		t.Fatalf("source data lost: %q", got)
	}
}

func TestEditMoveRejectsSameExistingAndDuplicateCanonicalTargets(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(strings.ToUpper(name)), 0644); err != nil {
			t.Fatal(err)
		}
	}
	snaps := hashline.NewMemSnapshotStore()
	ha, _ := snaps.Record(filepath.Join(root, "a"), "A")
	hb, _ := snaps.Record(filepath.Join(root, "b"), "B")
	et := NewEditTool(root, hashline.OSFilesystem{}, snaps)
	for name, patch := range map[string]string{
		"same":                  "[a#" + ha + "]\nMV a",
		"existing":              "[a#" + ha + "]\nMV b",
		"lexical duplicate":     "[a#" + ha + "]\nMV c\n[b#" + hb + "]\nMV ./c",
		"canonical duplicate":   "[a#" + ha + "]\nMV c\n[b#" + hb + "]\nMV c",
		"outside parent":        "[a#" + ha + "]\nMV ../outside",
		"second source remains": "[b#" + hb + "]\nMV b",
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]string{"input": patch})
			if _, err := et.Execute(context.Background(), raw); err == nil {
				t.Fatal("accepted unsafe/duplicate move")
			}
		})
	}
	for name, want := range map[string]string{"a": "A", "b": "B"} {
		got, _ := os.ReadFile(filepath.Join(root, name))
		if string(got) != want {
			t.Fatalf("%s=%q", name, got)
		}
	}
}

type nthWriteFS struct {
	*memoryEditFS
	mu      sync.Mutex
	n, fail int
}
type memoryEditFS struct{ files map[string][]byte }

func (f *memoryEditFS) ReadFile(p string) ([]byte, error) {
	b, ok := f.files[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), b...), nil
}
func (f *memoryEditFS) WriteFile(p string, b []byte, _ os.FileMode) error {
	f.files[p] = append([]byte(nil), b...)
	return nil
}
func (f *nthWriteFS) WriteFile(p string, b []byte, m os.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	if f.n == f.fail {
		return errors.New("injected commit N")
	}
	return f.memoryEditFS.WriteFile(p, b, m)
}

func TestHashlinePartialCommitReturnsOrderedStructuredPrefixFailureAndSkipped(t *testing.T) {
	fs := &nthWriteFS{memoryEditFS: &memoryEditFS{files: map[string][]byte{"a": []byte("A"), "b": []byte("B"), "c": []byte("C")}}, fail: 2}
	snaps := hashline.NewMemSnapshotStore()
	ha, _ := snaps.Record("a", "A")
	hb, _ := snaps.Record("b", "B")
	hc, _ := snaps.Record("c", "C")
	et := NewEditTool(".", fs, snaps)
	patch := "[a#" + ha + "]\nPUT 1:\n+AA\n[b#" + hb + "]\nPUT 1:\n+BB\n[c#" + hc + "]\nPUT 1:\n+CC"
	raw, _ := json.Marshal(map[string]string{"input": patch})
	res, err := et.Execute(context.Background(), raw)
	if err == nil || len(res.Files) != 3 || res.Files[0].Operation != contract.FileUpdated || !res.Files[0].Committed || res.Files[1].Operation != contract.FileError || res.Files[1].Committed || res.Files[2].Operation != contract.FileError {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	if string(fs.files["a"]) != "AA" || string(fs.files["b"]) != "B" || string(fs.files["c"]) != "C" {
		t.Fatalf("files=%q/%q/%q", fs.files["a"], fs.files["b"], fs.files["c"])
	}
}

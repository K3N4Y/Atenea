package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contract "github.com/K3N4Y/atenea/agentcore/tool"
	"github.com/K3N4Y/atenea/internal/tool/editmode"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

func patchTool(t *testing.T) (*EditTool, string) {
	t.Helper()
	root := t.TempDir()
	et := NewEditTool(root, hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
	et.Mode = editmode.Patch
	return et, root
}

func TestEditToolPatchCreateOverExistingDoesNotOverwrite(t *testing.T) {
	et, root := patchTool(t)
	path := filepath.Join(root, "x.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	res, err := et.Execute(context.Background(), json.RawMessage(`{"path":"x.txt","edits":[{"op":"create","diff":"new"}]}`))
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "old\n" {
		t.Fatalf("existing bytes changed: %q", got)
	}
}

func TestPatchCreateUsesExactAuthoredPathAndUpdateMaySuffixResolve(t *testing.T) {
	for _, mode := range []editmode.Mode{editmode.Patch, editmode.ApplyPatch} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			nested := filepath.Join(root, "nested", "foo.txt")
			if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(nested, []byte("nested\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			et := NewEditTool(root, hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
			et.Mode = mode
			createInput := json.RawMessage(`{"path":"foo.txt","edits":[{"op":"create","diff":"root"}]}`)
			updateInput := json.RawMessage(`{"path":"foo.txt","edits":[{"op":"update","diff":"@@\n-nested\n+updated"}]}`)
			if mode == editmode.ApplyPatch {
				createInput = json.RawMessage(`{"input":"*** Begin Patch\n*** Add File: foo.txt\n+root\n*** End Patch"}`)
				updateInput = json.RawMessage(`{"input":"*** Begin Patch\n*** Update File: foo.txt\n@@\n-nested\n+updated\n*** End Patch"}`)
			}

			preview := et.Preview(context.Background(), createInput)
			if preview.Error != "" || len(preview.Files) != 1 || preview.Files[0].Operation != contract.FileCreated {
				t.Fatalf("create preview=%+v", preview)
			}
			if _, err := et.Execute(context.Background(), createInput); err != nil {
				t.Fatalf("create exact path: %v", err)
			}
			if got, _ := os.ReadFile(filepath.Join(root, "foo.txt")); string(got) != "root\n" {
				t.Fatalf("root bytes=%q", got)
			}
			if got, _ := os.ReadFile(nested); string(got) != "nested\n" {
				t.Fatalf("nested touched=%q", got)
			}
			if _, err := et.Execute(context.Background(), createInput); err == nil || !strings.Contains(err.Error(), "already exists") {
				t.Fatalf("existing exact create err=%v", err)
			}

			if err := os.Remove(filepath.Join(root, "foo.txt")); err != nil {
				t.Fatal(err)
			}
			preview = et.Preview(context.Background(), updateInput)
			if preview.Error != "" || len(preview.Files) != 1 || preview.Files[0].NewText != "updated\n" {
				t.Fatalf("suffix update preview=%+v", preview)
			}
			if _, err := et.Execute(context.Background(), updateInput); err != nil {
				t.Fatalf("suffix update: %v", err)
			}
			if got, _ := os.ReadFile(nested); string(got) != "updated\n" {
				t.Fatalf("suffix update bytes=%q", got)
			}
		})
	}
}

// Provenance: edit-per-file-diff-content.test.ts aggregation.
func TestEditToolPatchMultipleSequentialEntriesOnePath(t *testing.T) {
	et, root := patchTool(t)
	path := filepath.Join(root, "x.txt")
	os.WriteFile(path, []byte("a\nb\n"), 0644)
	res, err := et.Execute(context.Background(), json.RawMessage(`{"path":"x.txt","edits":[{"diff":"@@\n-a\n+A"},{"diff":"@@\n-b\n+B"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "A\nB\n" || len(res.Files) != 1 || res.Files[0].OldText != "a\nb\n" || res.Files[0].NewText != "A\nB\n" || res.Files[0].FirstChangedLine != 1 {
		t.Fatalf("got=%q result=%+v", got, res)
	}
}

// Provenance: core/apply-patch.test.ts "partial success: earlier ops stay applied when a later op fails".
func TestEditToolPatchFailureAfterFirstEntryReportsAppliedAndUnapplied(t *testing.T) {
	et, root := patchTool(t)
	path := filepath.Join(root, "x.txt")
	os.WriteFile(path, []byte("a\n"), 0644)
	res, err := et.Execute(context.Background(), json.RawMessage(`{"path":"x.txt","edits":[{"diff":"@@\n-a\n+A"},{"diff":"@@\n-missing\n+x"}]}`))
	got, _ := os.ReadFile(path)
	if string(got) != "A\n" || err == nil || !strings.Contains(err.Error(), "1 applied, 1 unapplied") || len(res.Files) != 1 || res.Files[0].Operation != contract.FileUpdated || !res.Files[0].Committed || res.Files[0].Error == "" {
		t.Fatalf("got=%q err=%v result=%+v", got, err, res)
	}
}

func TestEditToolPatchInvalidFirstEntryDoesNotMutate(t *testing.T) {
	et, root := patchTool(t)
	path := filepath.Join(root, "x.txt")
	os.WriteFile(path, []byte("a\n"), 0644)
	res, err := et.Execute(context.Background(), json.RawMessage(`{"path":"x.txt","edits":[{"diff":"@@\n-missing\n+x"},{"op":"delete"}]}`))
	got, _ := os.ReadFile(path)
	if string(got) != "a\n" || err == nil || !strings.Contains(err.Error(), "0 applied, 2 unapplied") || len(res.Files) != 1 || res.Files[0].Operation != contract.FileError {
		t.Fatalf("got=%q err=%v result=%+v", got, err, res)
	}
}

// Provenance: apply-patch adversarial rename destination and serialization tests.
func TestEditToolPatchRenameMetadataAndDestinationOverwriteGuard(t *testing.T) {
	et, root := patchTool(t)
	src := filepath.Join(root, "x.txt")
	os.WriteFile(src, []byte("a\n"), 0600)
	res, err := et.Execute(context.Background(), json.RawMessage(`{"path":"x.txt","edits":[{"rename":"sub/y.txt","diff":"@@\n-a\n+b"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	f := res.Files[0]
	if f.Operation != contract.FileMoved || f.Path != filepath.Join(root, "sub/y.txt") || f.OldText != "a\n" || f.NewText != "b\n" {
		t.Fatalf("file=%+v", f)
	}
	os.WriteFile(src, []byte("again\n"), 0644)
	_, err = et.Execute(context.Background(), json.RawMessage(`{"path":"x.txt","edits":[{"rename":"sub/y.txt","diff":"@@\n-again\n+x"}]}`))
	if err == nil {
		t.Fatal("expected destination error")
	}
	got, _ := os.ReadFile(src)
	if string(got) != "again\n" {
		t.Fatalf("source changed: %q", got)
	}
}

func TestEditToolPatchMoveThenFailureUsesMovedPostState(t *testing.T) {
	et, root := patchTool(t)
	os.WriteFile(filepath.Join(root, "x.txt"), []byte("a\n"), 0644)
	res, err := et.Execute(context.Background(), json.RawMessage(`{"path":"x.txt","edits":[{"rename":"moved.txt","diff":"@@\n-a\n+A"},{"diff":"@@\n-missing\n+x"},{"op":"delete"}]}`))
	if err == nil || !strings.Contains(err.Error(), "1 applied, 2 unapplied") || len(res.Files) != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "moved.txt")); string(got) != "A\n" {
		t.Fatalf("moved post-state=%q", got)
	}
	if res.Files[0].Path != filepath.Join(root, "moved.txt") || !res.Files[0].Committed || res.Files[0].Header == "" || res.Files[0].Error == "" {
		t.Fatalf("files=%+v", res.Files)
	}
}

func TestEditToolPatchCancellationSchemaAndPrompt(t *testing.T) {
	et, root := patchTool(t)
	path := filepath.Join(root, "x")
	os.WriteFile(path, []byte("a\n"), 0644)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := et.Execute(ctx, json.RawMessage(`{"path":"x","edits":[{"diff":"@@\n-a\n+b"}]}`))
	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("err=%v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "a\n" {
		t.Fatalf("changed=%q", got)
	}
	if !strings.Contains(et.Description(), "@@ $ANCHOR") || !strings.Contains(string(et.Schema()), `"required":["path","edits"]`) {
		t.Fatalf("definition=%+v", et.Definition())
	}
}

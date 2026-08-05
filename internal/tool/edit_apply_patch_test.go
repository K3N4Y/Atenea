package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/K3N4Y/atenea/internal/tool/editmode"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

func applyPatchTool(t *testing.T) (*EditTool, string) {
	t.Helper()
	root := t.TempDir()
	et := NewEditTool(root, hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
	et.Mode = editmode.ApplyPatch
	return et, root
}

// Provenance: oh-my-pi@5af71dc9 apply-patch multi-file and fixture scenarios
// 001, 003, 004, 020. Exercises the public EditTool facade.
func TestEditToolApplyPatchMultiFileMoveDelete(t *testing.T) {
	et, root := applyPatchTool(t)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\ntwo\nthree\n"), 0644)
	os.WriteFile(filepath.Join(root, "old.txt"), []byte("obsolete\n"), 0644)
	input := "*** Begin Patch\n*** Add File: new/新.txt\n+hello\n*** Update File: a.txt\n*** Move to: moved/a.txt\n@@\n-one\n+ONE\n@@\n-three\n+THREE\n*** Delete File: old.txt\n*** End Patch"
	body, _ := json.Marshal(map[string]string{"input": input})
	res, err := et.Execute(context.Background(), body)
	if err != nil {
		t.Fatal(err)
	}
	moved, _ := os.ReadFile(filepath.Join(root, "moved/a.txt"))
	created, _ := os.ReadFile(filepath.Join(root, "new/新.txt"))
	if string(moved) != "ONE\ntwo\nTHREE\n" || string(created) != "hello\n" || len(res.Files) != 3 {
		t.Fatalf("moved=%q created=%q result=%+v", moved, created, res)
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("source remains")
	}
	if _, err := os.Stat(filepath.Join(root, "old.txt")); !os.IsNotExist(err) {
		t.Fatal("delete failed")
	}
}

// Provenance: scenarios 005-013 and partial-success contract.
func TestEditToolApplyPatchValidationAndPartialSuccess(t *testing.T) {
	et, root := applyPatchTool(t)
	os.WriteFile(filepath.Join(root, "exists"), []byte("old\n"), 0644)
	cases := []struct{ patch, want string }{
		{"*** Begin Patch\n*** End Patch", "No files were modified"},
		{"*** Begin Patch\n*** Add File: exists\n+x\n*** End Patch", "file already exists"},
		{"*** Begin Patch\n*** Update File: missing\n@@\n-x\n+y\n*** End Patch", "File not found"},
		{"*** Begin Patch\n*** Delete File: missing\n*** End Patch", "File not found"},
	}
	for _, tc := range cases {
		body, _ := json.Marshal(map[string]string{"input": tc.patch})
		_, err := et.Execute(context.Background(), body)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("err=%v want=%q", err, tc.want)
		}
	}
	patch := "*** Begin Patch\n*** Add File: first\n+ok\n*** Update File: missing\n@@\n-x\n+y\n*** End Patch"
	body, _ := json.Marshal(map[string]string{"input": patch})
	res, err := et.Execute(context.Background(), body)
	if err == nil || !strings.Contains(err.Error(), "1 applied, 1 unapplied") || len(res.Files) != 2 || res.Files[0].Operation != "create" || res.Files[1].Operation != "error" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "first")); string(got) != "ok\n" {
		t.Fatalf("first=%q", got)
	}
}

// Provenance: apply-patch-freeform and strategy definition tests.
func TestEditToolApplyPatchDefinition(t *testing.T) {
	et, _ := applyPatchTool(t)
	def := et.Definition()
	if def.WireName != "apply_patch" || def.CustomFormat == nil || def.CustomFormat.Syntax != "lark" || !strings.Contains(def.CustomFormat.Definition, "add_hunk") || !strings.Contains(string(def.Schema), `"required":["input"]`) {
		t.Fatalf("definition=%+v", def)
	}
}

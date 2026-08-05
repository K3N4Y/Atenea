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
	"github.com/K3N4Y/atenea/internal/tool/editmode"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

func TestRound2HashlinePermissiveProviderFields(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "x.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snaps := hashline.NewMemSnapshotStore()
	h, _ := snaps.Record(path, "old\n")
	edit := NewEditTool(root, hashline.OSFilesystem{}, snaps)

	var schema map[string]any
	if err := json.Unmarshal(edit.Definition().Schema, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["additionalProperties"] != true {
		t.Fatalf("hashline schema must permit provider fields: %s", edit.Schema())
	}
	input := "[x.txt#" + h + "]\nPUT 1:\n+new"
	raw, _ := json.Marshal(map[string]any{"input": input, "path": "provider-only", "metadata": map[string]any{"id": 1}, "_input": "ignored"})
	result, err := edit.Execute(context.Background(), raw)
	if err != nil || len(result.Files) != 1 || !result.Files[0].Committed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if got, _ := os.ReadFile(path); string(got) != "new\n" {
		t.Fatalf("disk=%q", got)
	}
}

func TestRound2HashlineInvalidJSONDoesNotMutate(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"underscore input only", `{"_input":"patch"}`},
		{"missing input", `{}`},
		{"non-string input", `{"input":42}`},
		{"trailing object", `{"input":"patch"} {}`},
		{"trailing garbage", `{"input":"patch"} garbage`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "x.txt")
			if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			edit := NewEditTool(root, hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
			if _, err := edit.Execute(context.Background(), json.RawMessage(tc.raw)); err == nil {
				t.Fatal("invalid hashline JSON was accepted")
			}
			if got, _ := os.ReadFile(path); string(got) != "old\n" {
				t.Fatalf("mutated disk=%q", got)
			}
		})
	}
}

func TestRound2DeleteContinuesOrderedPatchModes(t *testing.T) {
	modes := []editmode.Mode{editmode.Patch, editmode.ApplyPatch}
	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			for name, text := range map[string]string{"delete.txt": "gone\n", "update.txt": "old\n", "move.txt": "move\n", "delete2.txt": "gone2\n"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			edit := NewEditTool(root, hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
			edit.Mode = mode
			var raw json.RawMessage
			if mode == editmode.Patch {
				raw = json.RawMessage(`{"path":"delete.txt","edits":[{"op":"delete"},{"op":"create","diff":"old"},{"op":"update","diff":"@@\n-old\n+updated"},{"op":"update","rename":"moved.txt","diff":"@@\n-updated\n+moved"},{"op":"delete"}]}`)
			} else {
				input := "*** Begin Patch\n*** Delete File: delete.txt\n*** Add File: created.txt\n+created\n*** Update File: update.txt\n@@\n-old\n+updated\n*** Update File: move.txt\n*** Move to: moved.txt\n@@\n-move\n+moved\n*** Delete File: delete2.txt\n*** End Patch"
				raw, _ = json.Marshal(map[string]string{"input": input})
			}
			result, err := edit.Execute(context.Background(), raw)
			if err != nil {
				t.Fatal(err)
			}
			if mode == editmode.Patch {
				if _, err := os.Stat(filepath.Join(root, "delete.txt")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("source remains: %v", err)
				}
				if _, err := os.Stat(filepath.Join(root, "moved.txt")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("moved target remains: %v", err)
				}
				if len(result.Files) != 1 || result.Files[0].Operation != contract.FileDeleted || result.Files[0].OldText != "gone\n" || result.Files[0].NewText != "" || result.Output != "Deleted delete.txt\nCreated delete.txt\nUpdated delete.txt\nUpdated and moved delete.txt to moved.txt\nDeleted delete.txt" {
					t.Fatalf("ordered result=%+v", result)
				}
				return
			}
			wantOps := []contract.FileOperation{contract.FileDeleted, contract.FileCreated, contract.FileUpdated, contract.FileMoved, contract.FileDeleted}
			if len(result.Files) != len(wantOps) {
				t.Fatalf("files=%+v", result.Files)
			}
			for i, op := range wantOps {
				if result.Files[i].Operation != op || !result.Files[i].Committed {
					t.Fatalf("file %d=%+v", i, result.Files[i])
				}
			}
			if result.Output != "Deleted delete.txt\nCreated created.txt\nUpdated update.txt\nUpdated and moved move.txt to moved.txt\nDeleted delete2.txt" {
				t.Fatalf("output=%q", result.Output)
			}
			checks := map[string]string{"created.txt": "created\n", "update.txt": "updated\n", "moved.txt": "moved\n"}
			for name, want := range checks {
				if got, _ := os.ReadFile(filepath.Join(root, name)); string(got) != want {
					t.Fatalf("%s=%q", name, got)
				}
			}
			for _, name := range []string{"delete.txt", "delete2.txt", "move.txt"} {
				if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s remains: %v", name, err)
				}
			}
		})
	}
}

func TestRound2UncertainDeleteStopsWithoutRetry(t *testing.T) {
	root := filepath.Clean("/work")
	fs := &finalBFS{files: map[string][]byte{filepath.Join(root, "a"): []byte("old\n")}, fault: "readback", mutated: map[string]bool{}}
	edit := NewEditTool(root, fs, hashline.NewMemSnapshotStore())
	edit.Mode = editmode.Patch
	result, err := edit.Execute(context.Background(), json.RawMessage(`{"path":"a","edits":[{"op":"delete"},{"op":"create","diff":"new"}]}`))
	if err != nil || len(result.Files) != 1 || !result.Files[0].Committed || !strings.Contains(result.Files[0].DisplayError, "do not retry") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, exists := fs.files[filepath.Join(root, "a")]; exists {
		t.Fatal("later create ran after uncertain delete")
	}
}

func TestRound2OtherModesRemainStrict(t *testing.T) {
	for _, mode := range []editmode.Mode{editmode.Replace, editmode.Patch, editmode.ApplyPatch} {
		edit := NewEditTool(t.TempDir(), hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
		edit.Mode = mode
		var schema map[string]any
		if err := json.Unmarshal(edit.Schema(), &schema); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(schema["additionalProperties"], false) {
			t.Fatalf("mode %s schema became permissive", mode)
		}
	}
}

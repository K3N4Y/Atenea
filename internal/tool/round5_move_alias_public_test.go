package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/K3N4Y/atenea/internal/tool/editmode"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

func TestRound5_PublicMoveDestinationsRejectInWorkspaceSymlinkParents(t *testing.T) {
	modes := []struct {
		name  string
		mode  editmode.Mode
		input func(string) json.RawMessage
	}{
		{
			name: "hashline MV",
			mode: editmode.Hashline,
			input: func(hash string) json.RawMessage {
				raw, _ := json.Marshal(map[string]string{"input": "[source.txt#" + hash + "]\nCUT 1 @must-not-publish\nMV alias/destination.txt"})
				return raw
			},
		},
		{
			name: "patch update rename",
			mode: editmode.Patch,
			input: func(string) json.RawMessage {
				return json.RawMessage(`{"path":"source.txt","edits":[{"op":"update","rename":"alias/destination.txt","diff":"@@\n-source\n+changed"}]}`)
			},
		},
		{
			name: "apply_patch Move to",
			mode: editmode.ApplyPatch,
			input: func(string) json.RawMessage {
				return json.RawMessage(`{"input":"*** Begin Patch\n*** Update File: source.txt\n*** Move to: alias/destination.txt\n@@\n-source\n+changed\n*** End Patch"}`)
			},
		},
	}
	for _, tc := range modes {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			realParent := filepath.Join(root, "real")
			if err := os.Mkdir(realParent, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(realParent, filepath.Join(root, "alias")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			source := filepath.Join(root, "source.txt")
			if err := os.WriteFile(source, []byte("source\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			snapshots := hashline.NewMemSnapshotStore()
			hash, _ := snapshots.Record(source, "source\n")
			edit := NewEditTool(root, hashline.OSFilesystem{}, snapshots)
			edit.Mode = tc.mode
			before := edit.patcher(context.Background()).ForkClipboard()

			result, err := edit.Execute(context.Background(), tc.input(hash))
			if err == nil || len(result.Files) != 1 || result.Files[0].Committed || result.Files[0].Error == "" {
				t.Fatalf("expected structured uncommitted error: result=%+v err=%v", result, err)
			}
			if got, readErr := os.ReadFile(source); readErr != nil || string(got) != "source\n" {
				t.Fatalf("source changed: bytes=%q err=%v", got, readErr)
			}
			if _, statErr := os.Stat(filepath.Join(realParent, "destination.txt")); !os.IsNotExist(statErr) {
				t.Fatalf("real target was published: %v", statErr)
			}
			if head := snapshots.Head(source); head == nil || head.Hash != hash || head.Text != "source\n" {
				t.Fatalf("source snapshot changed: %+v", head)
			}
			if snapshots.Head(filepath.Join(realParent, "destination.txt")) != nil {
				t.Fatal("destination snapshot was published")
			}
			after := edit.patcher(context.Background()).ForkClipboard()
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("register state changed: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestRound5_PublicMoveDestinationNestedAliasAndRealParentBehavior(t *testing.T) {
	for _, tc := range []struct {
		name        string
		destination string
		wantSuccess bool
	}{
		{name: "nested symlink component", destination: "parent/alias/nested/destination.txt"},
		{name: "nonexistent children under real parent", destination: "parent/real/nested/destination.txt", wantSuccess: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			real := filepath.Join(root, "parent", "real")
			if err := os.MkdirAll(real, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, filepath.Join(root, "parent", "alias")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			source := filepath.Join(root, "source.txt")
			if err := os.WriteFile(source, []byte("source\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			snapshots := hashline.NewMemSnapshotStore()
			hash, _ := snapshots.Record(source, "source\n")
			edit := NewEditTool(root, hashline.OSFilesystem{}, snapshots)
			raw, _ := json.Marshal(map[string]string{"input": "[source.txt#" + hash + "]\nMV " + tc.destination})
			result, err := edit.Execute(context.Background(), raw)
			if tc.wantSuccess {
				if err != nil || len(result.Files) != 1 || !result.Files[0].Committed {
					t.Fatalf("real parent move failed: result=%+v err=%v", result, err)
				}
				if got, readErr := os.ReadFile(filepath.Join(root, tc.destination)); readErr != nil || string(got) != "source\n" {
					t.Fatalf("destination bytes=%q err=%v", got, readErr)
				}
				return
			}
			if err == nil || len(result.Files) != 1 || result.Files[0].Committed {
				t.Fatalf("nested alias accepted: result=%+v err=%v", result, err)
			}
			if got, _ := os.ReadFile(source); string(got) != "source\n" {
				t.Fatalf("source changed: %q", got)
			}
			if _, statErr := os.Stat(filepath.Join(real, "nested", "destination.txt")); !os.IsNotExist(statErr) {
				t.Fatalf("target created through nested alias: %v", statErr)
			}
		})
	}
}

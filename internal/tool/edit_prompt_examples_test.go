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

// TestEditDefinitionsRepresentativeExamplesExecute keeps every advertised edit
// strategy tied to an executable public EditTool example rather than validating
// prompt text independently from behavior.
func TestEditDefinitionsRepresentativeExamplesExecute(t *testing.T) {
	tests := []struct {
		name   string
		mode   editmode.Mode
		input  func(string) json.RawMessage
		claims []string
	}{
		{"hashline", editmode.Hashline, func(old string) json.RawMessage {
			body, _ := json.Marshal(map[string]string{"input": "[x.txt#" + hashline.ComputeFileHash(old) + "]\nPUT 1.=1:\n+new"})
			return body
		}, []string{"[PATH#TAG]", "PUT N.=M:"}},
		{"apply_patch", editmode.ApplyPatch, func(string) json.RawMessage {
			return json.RawMessage(`{"input":"*** Begin Patch\n*** Update File: x.txt\n@@\n-old\n+new\n*** End Patch\n"}`)
		}, []string{"*** Begin Patch", "*** Update File:"}},
		{"patch", editmode.Patch, func(string) json.RawMessage {
			return json.RawMessage(`{"path":"x.txt","edits":[{"op":"update","diff":"@@\n-old\n+new"}]}`)
		}, []string{"@@", `op: "update"`}},
		{"replace", editmode.Replace, func(string) json.RawMessage {
			return json.RawMessage(`{"path":"x.txt","old_string":"old","new_string":"new"}`)
		}, []string{"old_string", "replace_all"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			const old = "old\n"
			path := filepath.Join(root, "x.txt")
			if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
				t.Fatal(err)
			}
			edit := NewEditTool(root, hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
			edit.Mode = tc.mode
			if tc.mode == editmode.Hashline {
				edit.Patcher.Snapshots.Record(path, old)
			}
			def := edit.Definition()
			for _, claim := range tc.claims {
				if !strings.Contains(def.Description, claim) {
					t.Fatalf("description lacks executable claim %q", claim)
				}
			}
			if def.CustomFormat != nil && (def.CustomFormat.Syntax != "lark" || !strings.Contains(def.CustomFormat.Definition, "start:")) {
				t.Fatalf("invalid custom grammar: %+v", def.CustomFormat)
			}
			res, err := edit.Execute(context.Background(), tc.input(old))
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != "new\n" || len(res.Files) != 1 || !res.Files[0].Committed {
				t.Fatalf("bytes=%q result=%+v err=%v", got, res, err)
			}
		})
	}
}

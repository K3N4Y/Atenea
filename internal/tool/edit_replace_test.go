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

// Execution coverage ported from executeReplace in oh-my-pi@5af71dc9.
// https://github.com/can1357/oh-my-pi/blob/5af71dc9cf132538e072806424f71f43f734d9ae/packages/coding-agent/src/edit/modes/replace.ts
func TestEditToolReplaceExecutionPreservesSerializationAndMetadata(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "x.txt")
	original := "\ufeffalpha\r\nbeta\r\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	et := NewEditTool(root, hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
	et.Mode = editmode.Replace
	res, err := et.Execute(context.Background(), json.RawMessage(`{"path":"x.txt","old_string":"alpha\nbeta","new_string":"ALPHA\nbeta"}`))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "\ufeffALPHA\r\nbeta\r\n" {
		t.Fatalf("bytes=%q", data)
	}
	if res.Output != "Successfully replaced text in x.txt." || res.Diff == "" || len(res.Files) != 1 {
		t.Fatalf("result=%+v", res)
	}
	f := res.Files[0]
	if f.Path != path || f.Operation != contract.FileEdited || f.OldText != original || f.NewText != string(data) || f.FirstChangedLine != 1 || !f.Committed {
		t.Fatalf("file=%+v", f)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%v", info.Mode())
	}
}

func TestEditToolReplaceNegativeBoundaries(t *testing.T) {
	root := t.TempDir()
	et := NewEditTool(root, hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
	et.Mode = editmode.Replace
	cases := map[string]struct{ input, want string }{
		"extra property": {`{"path":"x","old_string":"a","new_string":"b","extra":1}`, "unknown field"},
		"missing file":   {`{"path":"missing","old_string":"a","new_string":"b"}`, "File not found: missing"},
		"outside":        {`{"path":"../x","old_string":"a","new_string":"b"}`, "fuera del workspace"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := et.Execute(context.Background(), json.RawMessage(tc.input))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	path := filepath.Join(root, "x")
	if err := os.WriteFile(path, []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := et.Execute(ctx, json.RawMessage(`{"path":"x","old_string":"a","new_string":"b"}`))
	if err != context.Canceled {
		t.Fatalf("err=%v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "a\n" {
		t.Fatalf("cancelled write changed file: %q", data)
	}
	_, err = et.Execute(context.Background(), json.RawMessage(`{"path":"x","old_string":"a","new_string":"a"}`))
	if err == nil || err.Error() != "Edits to x resulted in no changes being made." {
		t.Fatalf("err=%v", err)
	}
}

// Ported from read-edit-out-of-cwd.test.ts: a unique workspace suffix resolves.
func TestEditToolReplaceResolvesUniqueWorkspaceSuffix(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "src", "x.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0644); err != nil {
		t.Fatal(err)
	}
	et := NewEditTool(root, hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
	et.Mode = editmode.Replace
	_, err := et.Execute(context.Background(), json.RawMessage(`{"path":"x.txt","old_string":"alpha","new_string":"ALPHA"}`))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "ALPHA\nbeta\n" {
		t.Fatalf("data=%q", data)
	}
}

func TestEditToolReplaceSchemaAndProjection(t *testing.T) {
	et := &EditTool{Mode: editmode.Replace}
	var schema map[string]any
	if err := json.Unmarshal(et.Schema(), &schema); err != nil {
		t.Fatal(err)
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("schema=%s", et.Schema())
	}
	required := schema["required"].([]any)
	if len(required) != 3 {
		t.Fatalf("required=%v", required)
	}
	o := editmode.FindMatch("a\nb", "b", false, .95, nil)
	if o.Match == nil || o.Match.StartLine != 2 {
		t.Fatalf("projection matcher=%+v", o)
	}
}

package tool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireASTGrep(t *testing.T) {
	t.Helper()
	if _, err := astGrepCommand(); err != nil {
		t.Skip(err)
	}
}

func astCall(t *testing.T, tool *ASTTool, input map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(context.Background(), raw)
	return result.Output, err
}

func TestASTSearchIsStructural(t *testing.T) {
	requireASTGrep(t)
	root := t.TempDir()
	content := "package p\n\n// println(ignored)\nfunc f() { println(\"live\") }\n"
	if err := os.WriteFile(filepath.Join(root, "x.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := astCall(t, NewASTTool(root), map[string]any{"operation": "search", "path": ".", "pattern": "println($X)", "language": "go"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "x.go:4:") != 1 || strings.Contains(out, "ignored") {
		t.Fatalf("not a structural result:\n%s", out)
	}
}

func TestASTRewritePreviewsThenApplies(t *testing.T) {
	requireASTGrep(t)
	root := t.TempDir()
	path := filepath.Join(root, "x.go")
	original := "package p\nfunc f() { println(\"x\") }\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewASTTool(root)
	call := map[string]any{"operation": "rewrite", "path": "x.go", "pattern": "println($X)", "replacement": "fmt.Println($X)", "language": "go"}
	out, err := astCall(t, tool, call)
	if err != nil || !strings.Contains(out, "dry run") || !strings.Contains(out, "fmt.Println") {
		t.Fatalf("preview=%q err=%v", out, err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Fatalf("preview mutated file: %s", got)
	}
	call["apply"] = true
	out, err = astCall(t, tool, call)
	if err != nil || !strings.Contains(out, "Applied 1") {
		t.Fatalf("apply=%q err=%v", out, err)
	}
	got, _ = os.ReadFile(path)
	if !strings.Contains(string(got), `fmt.Println("x")`) {
		t.Fatalf("rewrite missing: %s", got)
	}
}

func TestASTEffectsTraversalAndMissingBinary(t *testing.T) {
	tool := NewASTTool(t.TempDir())
	preview := Call{Input: json.RawMessage(`{"operation":"rewrite","path":"x.go","pattern":"a()","replacement":"b()"}`)}
	apply := Call{Input: json.RawMessage(`{"operation":"rewrite","path":"x.go","pattern":"a()","replacement":"b()","apply":true}`)}
	if tool.CallEffects(preview) != NoEffects || tool.CallEffects(apply) != WritesFiles {
		t.Fatal("rewrite effects do not distinguish preview and apply")
	}
	if rule, ok := tool.GrantRule(apply); !ok || rule.Tool != "ast" || rule.Prefix != "rewrite" {
		t.Fatalf("grant=%+v ok=%v", rule, ok)
	}
	_, err := astCall(t, tool, map[string]any{"operation": "search", "path": "../outside.go", "pattern": "f()"})
	if err == nil || !strings.Contains(err.Error(), "fuera del workspace") {
		t.Fatalf("traversal error=%v", err)
	}
	inside := filepath.Join(tool.root, "x.go")
	if err := os.WriteFile(inside, []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool.commandFor = func() (string, error) { return "", exec.ErrNotFound }
	_, err = astCall(t, tool, map[string]any{"operation": "search", "path": "x.go", "pattern": "package p", "language": "go"})
	if err == nil || !strings.Contains(err.Error(), "executable file not found") {
		t.Fatalf("missing binary error=%v", err)
	}
}

func TestASTHonorsCancellation(t *testing.T) {
	tool := NewASTTool(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tool.Execute(ctx, json.RawMessage(`{"operation":"search","path":".","pattern":"f()"}`))
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("error=%v", err)
	}
}

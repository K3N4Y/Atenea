package tool

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLSPServerProcess(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), "--lsp-test-server") {
		return
	}
	r := bufio.NewReader(os.Stdin)
	for {
		body, err := readLSPMessage(r)
		if err != nil {
			os.Exit(0)
		}
		var req lspResponse
		if json.Unmarshal(body, &req) != nil || len(req.ID) == 0 {
			continue
		}
		result := any(nil)
		switch req.Method {
		case "initialize":
			result = map[string]any{"capabilities": map[string]any{"diagnosticProvider": map[string]any{}}}
		case "textDocument/diagnostic":
			if strings.Contains(strings.Join(os.Args, " "), "--with-diagnostic") {
				result = map[string]any{"kind": "full", "items": []any{map[string]any{"range": map[string]any{"start": map[string]int{"line": 0, "character": 0}, "end": map[string]int{"line": 0, "character": 1}}, "severity": 1, "message": "broken declaration"}}}
			} else {
				result = map[string]any{"kind": "full", "items": []any{}}
			}
		default:
			result = []any{}
		}
		out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": result})
		fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n%s", len(out), out)
	}
}

func testLSPTool(t *testing.T, root string) *LSPTool {
	t.Helper()
	tool := NewLSPTool(root)
	tool.commandFor = func(string) ([]string, error) {
		return []string{os.Args[0], "-test.run=TestLSPServerProcess", "--", "--lsp-test-server"}, nil
	}
	t.Cleanup(func() { _ = tool.Close() })
	return tool
}

func TestLSPTool_DiagnosticsUsesPersistentServer(t *testing.T) {
	root := workspaceWithFile(t, "main.go", "package main\n")
	tool := testLSPTool(t, root)
	for i := 0; i < 2; i++ {
		out, err := tool.DiagnosticsForPath(context.Background(), "main.go")
		if err != nil {
			t.Fatal(err)
		}
		if out != "No diagnostics." {
			t.Fatalf("output = %q", out)
		}
	}
	tool.mu.Lock()
	defer tool.mu.Unlock()
	if len(tool.servers) != 1 {
		t.Fatalf("servers = %d, want 1", len(tool.servers))
	}
}

func TestLSPClient_DiagnosticsAfterChangeWaitsForFreshPublication(t *testing.T) {
	root := workspaceWithFile(t, "main.go", "old\n")
	path := filepath.Join(root, "main.go")
	uri := pathURI(path)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	go func() { _, _ = bufio.NewReader(reader).ReadString('\n') }()
	c := &lspClient{
		in:                writer,
		opened:            map[string]string{uri: "old\n"},
		diagnosticsByURI:  map[string][]lspDiagnostic{uri: {{Message: "stale"}}},
		diagnosticWaiters: make(map[string][]chan struct{}),
	}
	if err := os.WriteFile(path, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.syncFile(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	result := make(chan []lspDiagnostic, 1)
	go func() {
		diagnostics, _ := c.diagnostics(context.Background(), uri)
		result <- diagnostics
	}()
	deadline := time.Now().Add(time.Second)
	for {
		c.stateMu.Lock()
		waiting := len(c.diagnosticWaiters[uri]) == 1
		c.stateMu.Unlock()
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("diagnostics did not wait for a fresh publication")
		}
		time.Sleep(time.Millisecond)
	}
	c.stateMu.Lock()
	c.diagnosticsByURI[uri] = []lspDiagnostic{{Message: "fresh"}}
	waiters := c.diagnosticWaiters[uri]
	delete(c.diagnosticWaiters, uri)
	c.stateMu.Unlock()
	for _, waiter := range waiters {
		close(waiter)
	}
	got := <-result
	if len(got) != 1 || got[0].Message != "fresh" {
		t.Fatalf("diagnostics = %+v, want fresh publication", got)
	}
}

func TestLSPClient_DiagnosticsTimeoutRemovesWaiter(t *testing.T) {
	c := &lspClient{diagnosticsByURI: make(map[string][]lspDiagnostic), diagnosticWaiters: make(map[string][]chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.diagnostics(ctx, "file:///missing.go"); !errors.Is(err, context.Canceled) {
		t.Fatalf("diagnostics error = %v, want canceled", err)
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if len(c.diagnosticWaiters) != 0 {
		t.Fatalf("diagnostic waiters retained after cancellation: %v", c.diagnosticWaiters)
	}
}

func TestLSPTool_CallEffectsAndGrantRule(t *testing.T) {
	tool := NewLSPTool(t.TempDir())
	read := Call{Name: "lsp", Input: json.RawMessage(`{"operation":"definition","path":"x.go","line":1,"column":1}`)}
	if got := tool.CallEffects(read); got != NoEffects {
		t.Fatalf("definition effects = %v", got)
	}
	rename := Call{Name: "lsp", Input: json.RawMessage(`{"operation":"rename","path":"x.go","line":1,"column":1,"new_name":"y"}`)}
	if got := tool.CallEffects(rename); got != WritesFiles {
		t.Fatalf("rename effects = %v", got)
	}
	rule, ok := tool.GrantRule(rename)
	if !ok || rule.Tool != "lsp" || rule.Prefix != "rename" {
		t.Fatalf("rule = %+v, %v", rule, ok)
	}
	if _, ok := tool.GrantRule(read); ok {
		t.Fatal("read-only operation was grantable")
	}
}

func TestLSPTool_RenameAppliesUTF16WorkspaceEdit(t *testing.T) {
	root := workspaceWithFile(t, "unicode.go", "package p\nvar 😀name = 1\n")
	tool := NewLSPTool(root)
	path := filepath.Join(root, "unicode.go")
	edit := workspaceEdit{Changes: map[string][]textEdit{pathURI(path): {{Range: lspRange{Start: lspPosition{Line: 1, Character: 6}, End: lspPosition{Line: 1, Character: 10}}, NewText: "value"}}}}
	res, err := tool.applyWorkspaceEdit(edit)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "package p\nvar 😀value = 1\n" {
		t.Fatalf("content = %q", got)
	}
	if !strings.Contains(res.Diff, "-var 😀name") || !strings.Contains(res.Diff, "+var 😀value") {
		t.Fatalf("diff:\n%s", res.Diff)
	}
}

func TestLSPTool_RenameValidatesAllEditsBeforeWriting(t *testing.T) {
	root := workspaceWithFile(t, "a.go", "old\n")
	tool := NewLSPTool(root)
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := textEdit{Range: lspRange{Start: lspPosition{}, End: lspPosition{Character: 3}}, NewText: "new"}
	_, err := tool.applyWorkspaceEdit(workspaceEdit{Changes: map[string][]textEdit{pathURI(filepath.Join(root, "a.go")): {e}, pathURI(outside): {e}}})
	if err == nil || !strings.Contains(err.Error(), "outside workspace") {
		t.Fatalf("error = %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "a.go"))
	if string(got) != "old\n" {
		t.Fatalf("first file changed: %q", got)
	}
}

func TestLSPTool_RenameRollsBackWhenSecondCommitFails(t *testing.T) {
	root := workspaceWithFile(t, "a.go", "old\n")
	if err := os.WriteFile(filepath.Join(root, "b.go"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewLSPTool(root)
	var mu sync.Mutex
	commits := 0
	tool.commitRename = func(oldPath, newPath string) error {
		mu.Lock()
		defer mu.Unlock()
		commits++
		if commits == 2 {
			return errors.New("injected commit failure")
		}
		return os.Rename(oldPath, newPath)
	}
	e := textEdit{Range: lspRange{Start: lspPosition{}, End: lspPosition{Character: 3}}, NewText: "new"}
	_, err := tool.applyWorkspaceEdit(workspaceEdit{Changes: map[string][]textEdit{
		pathURI(filepath.Join(root, "a.go")): {e},
		pathURI(filepath.Join(root, "b.go")): {e},
	}})
	if err == nil || !strings.Contains(err.Error(), "injected commit failure") {
		t.Fatalf("error = %v", err)
	}
	for _, name := range []string{"a.go", "b.go"} {
		got, readErr := os.ReadFile(filepath.Join(root, name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(got) != "old\n" {
			t.Fatalf("%s changed after failed commit: %q", name, got)
		}
	}
}

func TestLSPTool_FormatDefinitionLocationsAndLinks(t *testing.T) {
	root := workspaceWithFile(t, "target.go", "package p\n")
	tool := NewLSPTool(root)
	uri := pathURI(filepath.Join(root, "target.go"))
	tests := []struct {
		name, raw, want string
	}{
		{"location", fmt.Sprintf(`{"uri":%q,"range":{"start":{"line":1,"character":2},"end":{"line":1,"character":3}}}`, uri), "target.go:2:3"},
		{"locations", fmt.Sprintf(`[{"uri":%q,"range":{"start":{"line":2,"character":3},"end":{"line":2,"character":4}}}]`, uri), "target.go:3:4"},
		{"link selection", fmt.Sprintf(`[{"targetUri":%q,"targetRange":{"start":{"line":4,"character":5}},"targetSelectionRange":{"start":{"line":6,"character":7}}}]`, uri), "target.go:7:8"},
		{"link range fallback", fmt.Sprintf(`[{"targetUri":%q,"targetRange":{"start":{"line":8,"character":9}}}]`, uri), "target.go:9:10"},
		{"null", `null`, "No locations found."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tool.formatLocations(json.RawMessage(tt.raw)); got != tt.want {
				t.Fatalf("formatLocations() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLSPTool_FormatSymbolsIncludesChildrenAndSkipsExternalLocations(t *testing.T) {
	root := workspaceWithFile(t, "target.go", "package p\n")
	tool := NewLSPTool(root)
	inside := pathURI(filepath.Join(root, "target.go"))
	outside := pathURI(filepath.Join(t.TempDir(), "dependency.go"))
	raw := json.RawMessage(fmt.Sprintf(`[
		{"name":"Parent","selectionRange":{"start":{"line":1,"character":2}},"children":[{"name":"Child","selectionRange":{"start":{"line":3,"character":4}}}]},
		{"name":"Inside","location":{"uri":%q,"range":{"start":{"line":5,"character":6}}}},
		{"name":"Outside","location":{"uri":%q,"range":{"start":{"line":7,"character":8}}}}
	]`, inside, outside))

	got := tool.formatSymbols(raw, "target.go")
	want := "target.go:2:3: Parent\ntarget.go:4:5: Child\ntarget.go:6:7: Inside"
	if got != want {
		t.Fatalf("formatSymbols() = %q, want %q", got, want)
	}
}

func TestLSPTool_AutodetectsRequiredServers(t *testing.T) {
	cases := map[string]string{"x.go": "gopls", "x.rs": "rust-analyzer", "x.ts": "typescript-language-server --stdio", "x.js": "typescript-language-server --stdio", "x.py": "pyright-langserver --stdio", "x.cpp": "clangd"}
	for path, want := range cases {
		got, err := serverCommand(path)
		if err != nil || strings.Join(got, " ") != want {
			t.Errorf("%s: %v, %v; want %s", path, got, err, want)
		}
	}
}

func TestLSPTool_GoplsEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls is not installed")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module smoke\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const source = "package smoke\n\nfunc target() {}\nfunc caller() { target() }\n"
	if err := os.WriteFile(filepath.Join(root, "smoke.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewLSPTool(root)
	t.Cleanup(func() { _ = tool.Close() })

	definition, err := tool.Execute(context.Background(), json.RawMessage(`{"operation":"definition","path":"smoke.go","line":4,"column":17}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(definition.Output, "smoke.go:3:6") {
		t.Fatalf("definition = %q", definition.Output)
	}
	rename, err := tool.Execute(context.Background(), json.RawMessage(`{"operation":"rename","path":"smoke.go","line":3,"column":6,"new_name":"renamed"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "smoke.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != strings.ReplaceAll(source, "target", "renamed") {
		t.Fatalf("renamed source = %q", got)
	}
	if !strings.Contains(rename.Diff, "+func renamed()") || !strings.Contains(rename.Diff, "renamed()") {
		t.Fatalf("rename diff = %q", rename.Diff)
	}
}

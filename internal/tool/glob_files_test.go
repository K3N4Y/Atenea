package tool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// TestGlobTool_FilesReturnsWorkspaceRelativePaths: Files devuelve las rutas
// relativas al workspace (no el texto formateado de Execute) junto con el flag
// de truncado, para que el binding del @-menu de archivos las consuma directo.
func TestGlobTool_FilesReturnsWorkspaceRelativePaths(t *testing.T) {
	searcher := &fakeGlobSearcher{result: GlobSearchResult{Entries: []GlobEntry{
		{Path: "app.go"},
		{Path: "internal/tool/read.go"},
	}}}
	gt := &GlobTool{Root: "/work", Searcher: searcher, DefaultLimit: defaultGlobLimit, MaxLimit: maxGlobLimit}

	paths, truncated, err := gt.Files(context.Background(), "", ".", 0)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	want := []string{"app.go", "internal/tool/read.go"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	if truncated {
		t.Fatal("truncated = true, want false")
	}
}

// TestGlobTool_FilesPropagatesTruncation: Files refleja el truncado del searcher
// (limite alcanzado) para que el binding pueda avisar o acotar el @-menu.
func TestGlobTool_FilesPropagatesTruncation(t *testing.T) {
	searcher := &fakeGlobSearcher{result: GlobSearchResult{
		Entries:   []GlobEntry{{Path: "a.go"}},
		Truncated: true,
	}}
	gt := &GlobTool{Root: "/work", Searcher: searcher, DefaultLimit: defaultGlobLimit, MaxLimit: maxGlobLimit}

	_, truncated, err := gt.Files(context.Background(), "", ".", 0)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if !truncated {
		t.Fatal("truncated = false, want true")
	}
}

// TestRipgrepGlobSearcher_EmptyPatternListsAllFiles: con Pattern vacio el
// searcher omite el include --glob, de modo que lista todos los archivos
// respetando .gitignore (un include como '*' des-ignoraria node_modules). El
// .git sigue excluido. Es la base de ListProjectFiles.
func TestRipgrepGlobSearcher_EmptyPatternListsAllFiles(t *testing.T) {
	runner := &fakeLineRunner{lines: []string{"app.go"}}
	searcher := &RipgrepGlobSearcher{Binary: "rg-test", Runner: runner}

	_, err := searcher.Glob(context.Background(), GlobSearch{Cwd: "/work", Pattern: "", Limit: 10})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	wantArgs := []string{"--no-config", "--files", "--glob=!**/.git/**", "--glob=!**/node_modules/**", "."}
	if !reflect.DeepEqual(runner.calls[0].args, wantArgs) {
		t.Fatalf("args\nwant %v\ngot  %v", wantArgs, runner.calls[0].args)
	}
}

// TestRipgrepGlobSearcher_ExcludesNodeModules: con rg real, un archivo dentro de
// node_modules no aparece aunque no haya .gitignore que lo excluya.
func TestRipgrepGlobSearcher_ExcludesNodeModules(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skipf("rg unavailable: %v", err)
	}
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "app.go"), "package main")
	mustWrite(t, filepath.Join(root, "node_modules", "pkg", "b.go"), "package pkg")

	res, err := (&RipgrepGlobSearcher{}).Glob(context.Background(), GlobSearch{Cwd: root, Pattern: "**/*.go", Limit: 10})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	for _, e := range res.Entries {
		if filepath.Base(filepath.Dir(filepath.Dir(e.Path))) == "node_modules" || e.Path == "node_modules/pkg/b.go" {
			t.Fatalf("node_modules no fue excluido: %+v", res.Entries)
		}
	}
	if len(res.Entries) != 1 || res.Entries[0].Path != "app.go" {
		t.Fatalf("Entries = %+v, want [app.go]", res.Entries)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

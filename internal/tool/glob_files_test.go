package tool

import (
	"context"
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
	wantArgs := []string{"--no-config", "--files", "--glob=!**/.git/**", "."}
	if !reflect.DeepEqual(runner.calls[0].args, wantArgs) {
		t.Fatalf("args\nwant %v\ngot  %v", wantArgs, runner.calls[0].args)
	}
}

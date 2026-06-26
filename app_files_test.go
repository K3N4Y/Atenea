package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"atenea/internal/tool"
)

// fakeAppGlobSearcher implementa tool.GlobSearcher con entradas fijas: deja
// probar el binding ListProjectFiles sin depender de rg ni del arbol real.
type fakeAppGlobSearcher struct{ entries []string }

func (f *fakeAppGlobSearcher) Glob(context.Context, tool.GlobSearch) (tool.GlobSearchResult, error) {
	es := make([]tool.GlobEntry, len(f.entries))
	for i, p := range f.entries {
		es[i] = tool.GlobEntry{Path: p}
	}
	return tool.GlobSearchResult{Entries: es}, nil
}

// TestApp_ListProjectFilesReturnsWorkspaceFiles: el binding devuelve las rutas
// de archivo del workspace (para el @-menu del composer), relativas a la raiz.
// Inyecta un searcher fake en el glob del binding para no tocar rg ni el disco.
func TestApp_ListProjectFilesReturnsWorkspaceFiles(t *testing.T) {
	app := newApp(demoProvider(), func(string, ...interface{}) {})
	app.glob.Searcher = &fakeAppGlobSearcher{entries: []string{"app.go", "internal/tool/glob.go"}}

	files, err := app.ListProjectFiles()
	if err != nil {
		t.Fatalf("ListProjectFiles: %v", err)
	}
	want := []string{"app.go", "internal/tool/glob.go"}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("files = %v, want %v", files, want)
	}
}

// TestApp_ListProjectFilesRespectsGitignoreAndExcludesGit: con el searcher real
// (rg), ListProjectFiles lista los archivos versionables del workspace: incluye
// los visibles, excluye lo de .gitignore y no asoma el .git. Triangula el camino
// end-to-end del @-menu sin GUI (newAppWithStore ancla el root en os.Getwd()).
func TestApp_ListProjectFilesRespectsGitignoreAndExcludesGit(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "visible.go"), "package x\n")
	writeFile(t, filepath.Join(root, ".gitignore"), "ignored.txt\n")
	writeFile(t, filepath.Join(root, "ignored.txt"), "secreto\n")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	writeFile(t, filepath.Join(root, ".git", "config"), "[core]\n")
	t.Chdir(root)

	app := newApp(demoProvider(), func(string, ...interface{}) {})
	files, err := app.ListProjectFiles()
	if err != nil {
		t.Fatalf("ListProjectFiles: %v", err)
	}

	set := map[string]bool{}
	for _, f := range files {
		set[f] = true
		if strings.HasPrefix(f, ".git/") {
			t.Errorf("no debe listarse contenido de .git: %v", files)
		}
	}
	if !set["visible.go"] {
		t.Errorf("falta visible.go en %v", files)
	}
	if set["ignored.txt"] {
		t.Errorf("ignored.txt no debe listarse (esta en .gitignore): %v", files)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

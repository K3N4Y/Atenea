package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestApp_ListProjectFilesReturnsWorkspaceFiles: el binding devuelve las rutas
// de archivo del workspace (para el @-menu del composer), relativas a la raiz.
// Usa un workspace temporal para cruzar el seam completo sin depender del repo.
func TestApp_ListProjectFilesReturnsWorkspaceFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app.go"), "package main\n")
	if err := os.MkdirAll(filepath.Join(root, "internal", "tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "internal", "tool", "glob.go"), "package tool\n")
	t.Chdir(root)
	app := newApp(demoProvider(), func(string, ...interface{}) {})

	files, err := app.ListProjectFiles()
	if err != nil {
		t.Fatalf("ListProjectFiles: %v", err)
	}
	want := []string{"app.go", "internal/tool/glob.go"}
	set := map[string]bool{}
	for _, file := range files {
		set[file] = true
	}
	for _, file := range want {
		if !set[file] {
			t.Fatalf("files = %v, missing %q", files, file)
		}
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

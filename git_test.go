package main

import (
	"os"
	"path/filepath"
	"testing"

	"atenea/internal/llm"
)

// setupRepo arma un repo git temporal con identidad de commit, para los tests de
// las funciones de git sin tocar el repo real.
func setupRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@atenea"},
		{"config", "user.name", "atenea test"},
	} {
		if _, err := runGit(root, args...); err != nil {
			t.Fatalf("setup git %v: %v", args, err)
		}
	}
	return root
}

func writeRepoFile(t *testing.T, root, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// TestGitStatus_SeparatesStagedAndUntracked: gitStatus reparte las lineas del
// porcelain en staged (en el index) y untracked (??).
func TestGitStatus_SeparatesStagedAndUntracked(t *testing.T) {
	root := setupRepo(t)
	writeRepoFile(t, root, "staged.txt", "hola")
	writeRepoFile(t, root, "nuevo.txt", "sin trackear")
	if _, err := runGit(root, "add", "staged.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}

	st, err := gitStatus(root)
	if err != nil {
		t.Fatalf("gitStatus: %v", err)
	}
	if len(st.Staged) != 1 || st.Staged[0].Path != "staged.txt" {
		t.Fatalf("staged: got %+v, want [staged.txt]", st.Staged)
	}
	if len(st.Untracked) != 1 || st.Untracked[0].Path != "nuevo.txt" {
		t.Fatalf("untracked: got %+v, want [nuevo.txt]", st.Untracked)
	}
}

// TestGitStatus_CleanRepoIsEmpty: sin cambios, ambas listas quedan vacias.
func TestGitStatus_CleanRepoIsEmpty(t *testing.T) {
	root := setupRepo(t)
	st, err := gitStatus(root)
	if err != nil {
		t.Fatalf("gitStatus: %v", err)
	}
	if len(st.Staged) != 0 || len(st.Untracked) != 0 {
		t.Fatalf("repo limpio: got %+v", st)
	}
}

// TestGitStatus_RepoReportsIsRepo: dentro de un repo, gitStatus marca IsRepo.
func TestGitStatus_RepoReportsIsRepo(t *testing.T) {
	root := setupRepo(t)
	st, err := gitStatus(root)
	if err != nil {
		t.Fatalf("gitStatus: %v", err)
	}
	if !st.IsRepo {
		t.Fatalf("repo inicializado deberia reportar IsRepo true: %+v", st)
	}
}

// TestGitStatus_NonRepoReportsNotRepo: en un directorio sin repo, gitStatus no
// falla; devuelve IsRepo false para que el panel ofrezca iniciar uno.
func TestGitStatus_NonRepoReportsNotRepo(t *testing.T) {
	root := t.TempDir()
	st, err := gitStatus(root)
	if err != nil {
		t.Fatalf("gitStatus en dir sin repo no deberia fallar: %v", err)
	}
	if st.IsRepo {
		t.Fatalf("dir sin repo deberia reportar IsRepo false: %+v", st)
	}
}

// TestGitInit_CreatesRepo: gitInit inicializa un repo en un directorio sin uno,
// y a partir de ahi gitStatus lo reconoce.
func TestGitInit_CreatesRepo(t *testing.T) {
	root := t.TempDir()
	if err := gitInit(root); err != nil {
		t.Fatalf("gitInit: %v", err)
	}
	st, err := gitStatus(root)
	if err != nil {
		t.Fatalf("gitStatus tras init: %v", err)
	}
	if !st.IsRepo {
		t.Fatalf("tras gitInit deberia ser repo: %+v", st)
	}
}

// TestGitCommit_CommitsStagedChanges: gitCommit confirma lo staged y deja el
// repo limpio.
func TestGitCommit_CommitsStagedChanges(t *testing.T) {
	root := setupRepo(t)
	writeRepoFile(t, root, "a.txt", "x")
	if _, err := runGit(root, "add", "a.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := gitCommit(root, "primer commit"); err != nil {
		t.Fatalf("gitCommit: %v", err)
	}
	st, _ := gitStatus(root)
	if len(st.Staged) != 0 {
		t.Fatalf("tras commit deberia quedar limpio: %+v", st)
	}
}

// TestGitCommit_RejectsEmptyMessage: un mensaje vacio no commitea.
func TestGitCommit_RejectsEmptyMessage(t *testing.T) {
	root := setupRepo(t)
	if err := gitCommit(root, "  "); err == nil {
		t.Fatal("gitCommit con mensaje vacio deberia fallar")
	}
}

// TestCommitMessageFromProvider_AccumulatesStreamText: el helper abre un turno
// aislado contra el provider y concatena los Text.Delta como mensaje.
func TestCommitMessageFromProvider_AccumulatesStreamText(t *testing.T) {
	prov := llm.NewFakeProvider(
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "feat: "},
		llm.Event{Kind: llm.TextDelta, Text: "agregar panel"},
		llm.Event{Kind: llm.TextEnded},
	)
	if got := commitMessageFromProvider(prov, "modelo", "diff"); got != "feat: agregar panel" {
		t.Fatalf("commitMessageFromProvider: got %q", got)
	}
}

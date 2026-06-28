package main

import (
	"os"
	"path/filepath"
	"strings"
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

// TestGitStatus_CapturesUnstagedWorktreeChanges: un archivo commiteado y luego
// modificado SIN stage cae en Unstaged (columna del working tree del porcelain),
// no se pierde. Es el bug que dejaba a Atenea mostrando menos cambios que VSCode.
func TestGitStatus_CapturesUnstagedWorktreeChanges(t *testing.T) {
	root := setupRepo(t)
	writeRepoFile(t, root, "a.txt", "uno\n")
	if _, err := runGit(root, "add", "a.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := runGit(root, "commit", "-m", "base"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	writeRepoFile(t, root, "a.txt", "UNO\n") // modificado sin add

	st, err := gitStatus(root)
	if err != nil {
		t.Fatalf("gitStatus: %v", err)
	}
	if len(st.Staged) != 0 {
		t.Fatalf("nada deberia estar staged: %+v", st.Staged)
	}
	if len(st.Unstaged) != 1 || st.Unstaged[0].Path != "a.txt" || st.Unstaged[0].Status != "M" {
		t.Fatalf("unstaged: got %+v, want [a.txt M]", st.Unstaged)
	}
}

// TestGitStatus_StagedThenModifiedAgainAppearsInBoth: un archivo con cambio en el
// index y OTRO cambio encima sin stage (porcelain "MM") aparece en Staged y en
// Unstaged a la vez, como en VSCode. Es la prueba que distingue el parseo de las
// dos columnas de uno que solo mira una.
func TestGitStatus_StagedThenModifiedAgainAppearsInBoth(t *testing.T) {
	root := setupRepo(t)
	writeRepoFile(t, root, "a.txt", "uno\n")
	if _, err := runGit(root, "add", "a.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := runGit(root, "commit", "-m", "base"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	writeRepoFile(t, root, "a.txt", "DOS\n") // cambio staged
	if _, err := runGit(root, "add", "a.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	writeRepoFile(t, root, "a.txt", "TRES\n") // otro cambio encima, sin add

	st, err := gitStatus(root)
	if err != nil {
		t.Fatalf("gitStatus: %v", err)
	}
	if len(st.Staged) != 1 || st.Staged[0].Path != "a.txt" {
		t.Fatalf("staged: got %+v, want [a.txt]", st.Staged)
	}
	if len(st.Unstaged) != 1 || st.Unstaged[0].Path != "a.txt" {
		t.Fatalf("unstaged: got %+v, want [a.txt]", st.Unstaged)
	}
}

// TestGitStatus_DeletedUnstaged: un archivo trackeado borrado del working tree sin
// stage (" D") va a Unstaged, no se pierde.
func TestGitStatus_DeletedUnstaged(t *testing.T) {
	root := setupRepo(t)
	writeRepoFile(t, root, "a.txt", "uno\n")
	if _, err := runGit(root, "add", "a.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := runGit(root, "commit", "-m", "base"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "a.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	st, err := gitStatus(root)
	if err != nil {
		t.Fatalf("gitStatus: %v", err)
	}
	if len(st.Unstaged) != 1 || st.Unstaged[0].Status != "D" {
		t.Fatalf("unstaged: got %+v, want [a.txt D]", st.Unstaged)
	}
}

// TestGitStatus_CleanRepoIsEmpty: sin cambios, las tres listas quedan vacias.
func TestGitStatus_CleanRepoIsEmpty(t *testing.T) {
	root := setupRepo(t)
	st, err := gitStatus(root)
	if err != nil {
		t.Fatalf("gitStatus: %v", err)
	}
	if len(st.Staged) != 0 || len(st.Unstaged) != 0 || len(st.Untracked) != 0 {
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

// TestGitFileDiff_StagedModification: para un archivo trackeado y modificado,
// gitFileDiff devuelve el diff unificado con la linea vieja (-) y la nueva (+).
func TestGitFileDiff_StagedModification(t *testing.T) {
	root := setupRepo(t)
	writeRepoFile(t, root, "a.txt", "uno\ndos\ntres\n")
	if _, err := runGit(root, "add", "a.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := runGit(root, "commit", "-m", "base"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// Cambio staged: "dos" -> "DOS".
	writeRepoFile(t, root, "a.txt", "uno\nDOS\ntres\n")
	if _, err := runGit(root, "add", "a.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}

	diff, err := gitFileDiff(root, "a.txt")
	if err != nil {
		t.Fatalf("gitFileDiff: %v", err)
	}
	if !strings.Contains(diff, "-dos") || !strings.Contains(diff, "+DOS") {
		t.Fatalf("diff deberia mostrar -dos/+DOS, got:\n%s", diff)
	}
	if !strings.Contains(diff, "a.txt") {
		t.Fatalf("diff deberia nombrar el archivo, got:\n%s", diff)
	}
}

// TestGitFileDiff_UntrackedNewFile: para un archivo sin trackear, gitFileDiff
// sintetiza un diff con todas las lineas como adiciones (+) y cabecera
// /dev/null -> b/<path>, asi el front lo renderiza igual que cualquier diff.
func TestGitFileDiff_UntrackedNewFile(t *testing.T) {
	root := setupRepo(t)
	writeRepoFile(t, root, "nuevo.txt", "linea uno\nlinea dos\n")

	diff, err := gitFileDiff(root, "nuevo.txt")
	if err != nil {
		t.Fatalf("gitFileDiff: %v", err)
	}
	if !strings.Contains(diff, "+++ b/nuevo.txt") {
		t.Fatalf("diff de archivo nuevo deberia tener cabecera +++ b/nuevo.txt, got:\n%s", diff)
	}
	if !strings.Contains(diff, "+linea uno") || !strings.Contains(diff, "+linea dos") {
		t.Fatalf("diff de archivo nuevo deberia tener las lineas como adiciones, got:\n%s", diff)
	}
}

// TestGitFileDiff_UnstagedWorktreeModification: un archivo commiteado y luego
// modificado SIN stage cae en la segunda rama (`git diff` del working tree).
func TestGitFileDiff_UnstagedWorktreeModification(t *testing.T) {
	root := setupRepo(t)
	writeRepoFile(t, root, "a.txt", "uno\n")
	if _, err := runGit(root, "add", "a.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := runGit(root, "commit", "-m", "base"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	writeRepoFile(t, root, "a.txt", "UNO\n") // sin add

	diff, err := gitFileDiff(root, "a.txt")
	if err != nil {
		t.Fatalf("gitFileDiff: %v", err)
	}
	if !strings.Contains(diff, "-uno") || !strings.Contains(diff, "+UNO") {
		t.Fatalf("diff del working tree deberia mostrar -uno/+UNO, got:\n%s", diff)
	}
}

// TestGitFileDiff_EmptyUntrackedFile: un archivo nuevo vacio no rompe el
// sintetizado: cabecera presente y hunk con 0 lineas, sin cuerpo de adiciones.
func TestGitFileDiff_EmptyUntrackedFile(t *testing.T) {
	root := setupRepo(t)
	writeRepoFile(t, root, "vacio.txt", "")

	diff, err := gitFileDiff(root, "vacio.txt")
	if err != nil {
		t.Fatalf("gitFileDiff: %v", err)
	}
	if !strings.Contains(diff, "+++ b/vacio.txt") {
		t.Fatalf("diff de archivo vacio deberia tener cabecera, got:\n%s", diff)
	}
	// El hunk de 0 lineas es lo ultimo: no hay cuerpo de adiciones detras.
	if !strings.HasSuffix(diff, "@@ -0,0 +1,0 @@\n") {
		t.Fatalf("diff de archivo vacio deberia terminar en el hunk de 0 lineas, got:\n%s", diff)
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

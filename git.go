package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"atenea/internal/llm"
)

// GitChange es un archivo con su estado de git (codigo del porcelain corto).
type GitChange struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

// GitStatus reparte los cambios en staged (en el index) y untracked, que es lo
// que el panel de git muestra en el MVP. IsRepo es false cuando root no es un
// repo git: el panel lo usa para ofrecer iniciar uno en vez de listar cambios.
type GitStatus struct {
	IsRepo    bool        `json:"isRepo"`
	Staged    []GitChange `json:"staged"`
	Untracked []GitChange `json:"untracked"`
}

// commitSystemPrompt instruye al modelo a devolver SOLO el mensaje de commit.
const commitSystemPrompt = "Genera un mensaje de commit conciso (una linea, estilo conventional commits si aplica) para el diff staged que te paso el usuario. Responde SOLO con el mensaje, sin comillas, sin explicaciones."

// maxDiffRunes acota el diff que se manda al modelo para no reventar el contexto.
// ponytail: corte fijo; si hace falta resumir diffs enormes, se hace aca.
const maxDiffRunes = 12000

// runGit corre git en root y devuelve su stdout; en error usa stderr como mensaje.
func runGit(root string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}

// gitStatus parsea `git status --porcelain`: cada linea es "XY path", X = estado
// en el index. "??" => untracked; X distinto de espacio => staged.
// ponytail: en renames el path es "viejo -> nuevo" tal cual; suficiente para el MVP.
func gitStatus(root string) (GitStatus, error) {
	out, err := runGit(root, "status", "--porcelain")
	if err != nil {
		// Sin repo no es un error a mostrar: el panel ofrece iniciar uno. Solo
		// propagamos fallos reales (status roto dentro de un repo).
		if !isGitRepo(root) {
			return GitStatus{IsRepo: false}, nil
		}
		return GitStatus{}, err
	}
	st := GitStatus{IsRepo: true}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if line[:2] == "??" {
			st.Untracked = append(st.Untracked, GitChange{Path: path, Status: "??"})
			continue
		}
		if line[0] != ' ' {
			st.Staged = append(st.Staged, GitChange{Path: path, Status: string(line[0])})
		}
	}
	return st, nil
}

// isGitRepo dice si root esta dentro de un work tree de git. Se apoya en el
// codigo de salida de rev-parse, asi no depende del idioma del mensaje de error.
func isGitRepo(root string) bool {
	_, err := runGit(root, "rev-parse", "--is-inside-work-tree")
	return err == nil
}

// gitInit inicializa un repo git en root (`git init`), para el boton del panel
// cuando el proyecto todavia no tiene repo.
func gitInit(root string) error {
	_, err := runGit(root, "init")
	return err
}

// gitCommit confirma lo staged con message. Rechaza un mensaje vacio antes de
// llamar a git.
func gitCommit(root, message string) error {
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("git commit: el mensaje no puede estar vacio")
	}
	_, err := runGit(root, "commit", "-m", message)
	return err
}

// commitMessageFromProvider abre un turno aislado (system de commit + el diff) y
// concatena el texto del stream como mensaje. "" si el stream falla o no produce.
func commitMessageFromProvider(p llm.Provider, model, diff string) string {
	out, err := p.Stream(context.Background(), llm.Request{
		Model:    model,
		System:   commitSystemPrompt,
		Messages: []llm.Message{{Role: "user", Text: diff}},
	})
	if err != nil {
		return ""
	}
	var b strings.Builder
	for ev := range out {
		if ev.Kind == llm.TextDelta {
			b.WriteString(ev.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

// GitStatus expone al frontend los cambios staged + untracked del workspace.
func (a *App) GitStatus() (GitStatus, error) { return gitStatus(a.root) }

// InitRepo inicializa un repo git en el proyecto (boton del panel cuando no hay
// repo). Tras llamarlo el frontend recarga GitStatus.
func (a *App) InitRepo() error { return gitInit(a.root) }

// Commit confirma lo staged con el mensaje que arma el usuario en el panel.
func (a *App) Commit(message string) error { return gitCommit(a.root, message) }

// GenerateCommitMessage genera un mensaje de commit a partir del diff staged.
// Falla si no hay nada staged (no hay diff que resumir).
func (a *App) GenerateCommitMessage() (string, error) {
	diff, err := runGit(a.root, "diff", "--cached")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(diff) == "" {
		return "", fmt.Errorf("no hay cambios staged para generar el mensaje")
	}
	if r := []rune(diff); len(r) > maxDiffRunes {
		diff = string(r[:maxDiffRunes])
	}
	return commitMessageFromProvider(a.provider, resolveModel(), diff), nil
}

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"atenea/internal/llm"
)

// GitChange es un archivo con su estado de git (codigo del porcelain corto).
type GitChange struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

// GitStatus reparte los cambios en tres listas, igual que la vista de control de
// fuentes de VSCode: Staged (en el index), Unstaged (modificados en el working
// tree pero sin add) y Untracked (sin trackear). Un mismo archivo puede estar en
// Staged y Unstaged a la vez (porcelain "MM": cambio en el index + cambio nuevo
// encima sin stage). IsRepo es false cuando root no es un repo git: el panel lo
// usa para ofrecer iniciar uno en vez de listar cambios.
type GitStatus struct {
	IsRepo    bool        `json:"isRepo"`
	Staged    []GitChange `json:"staged"`
	Unstaged  []GitChange `json:"unstaged"`
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

// gitStatus parsea `git status --porcelain`: cada linea es "XY path", donde X es
// el estado en el index (staged) e Y el del working tree (sin stage). "??" =>
// untracked. Si X no es espacio el archivo va a Staged; si Y no es espacio va a
// Unstaged; un "MM" cae en ambas. Antes solo se miraba X, asi que los archivos
// modificados sin add (" M") se perdian y el panel mostraba menos que VSCode.
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
		x, y := line[0], line[1]
		path := strings.TrimSpace(line[3:])
		if x == '?' && y == '?' {
			st.Untracked = append(st.Untracked, GitChange{Path: path, Status: "??"})
			continue
		}
		if x != ' ' {
			st.Staged = append(st.Staged, GitChange{Path: path, Status: string(x)})
		}
		if y != ' ' {
			st.Unstaged = append(st.Unstaged, GitChange{Path: path, Status: string(y)})
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

// gitFileDiff devuelve el diff unificado de un solo archivo, listo para
// renderizar en la pantalla de diff. Prueba en orden: lo staged contra HEAD
// (`diff --cached`, que es lo que el panel lista en "Staged"); si no hay nada
// staged, el cambio en el working tree (`diff`); y si tampoco (archivo nuevo sin
// trackear, que git diff ignora) sintetiza un diff con todo el contenido como
// adiciones. Asi el front consume siempre el mismo formato sin saber el estado.
func gitFileDiff(root, path string) (string, error) {
	if out, err := runGit(root, "diff", "--cached", "--", path); err == nil && strings.TrimSpace(out) != "" {
		return out, nil
	}
	if out, err := runGit(root, "diff", "--", path); err == nil && strings.TrimSpace(out) != "" {
		return out, nil
	}
	return newFileDiff(root, path)
}

// newFileDiff arma un diff unificado para un archivo nuevo (sin trackear): toda
// su contenido como adiciones, con cabecera /dev/null -> b/<path> y un unico
// hunk @@ -0,0 +1,N @@. Es lo que `git diff --no-index` produciria, pero sin el
// codigo de salida 1 que ese comando devuelve cuando hay diferencias.
func newFileDiff(root, path string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		return "", err
	}
	content := string(data)
	// Un archivo vacio son 0 lineas (strings.Split("", ...) daria [""], una de
	// mas). Con contenido, el "" final que deja Split tras un \n terminal se quita.
	var lines []string
	if content != "" {
		lines = strings.Split(content, "\n")
		if strings.HasSuffix(content, "\n") {
			lines = lines[:len(lines)-1]
		}
	}
	var b strings.Builder
	b.WriteString("--- /dev/null\n")
	b.WriteString("+++ b/" + path + "\n")
	fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", len(lines))
	for _, l := range lines {
		b.WriteString("+" + l + "\n")
	}
	return b.String(), nil
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
	ctx, cancel := context.WithTimeout(context.Background(), auxTurnTimeout)
	defer cancel()
	out, err := p.Stream(ctx, llm.Request{
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
func (a *App) GitStatus() (GitStatus, error) { return gitStatus(a.workspaceRoot()) }

// FileDiff expone al frontend el diff unificado de un archivo del workspace,
// para abrir la pantalla de diff desde el panel de git.
func (a *App) FileDiff(path string) (string, error) { return gitFileDiff(a.workspaceRoot(), path) }

// InitRepo inicializa un repo git en el proyecto (boton del panel cuando no hay
// repo). Tras llamarlo el frontend recarga GitStatus.
func (a *App) InitRepo() error { return gitInit(a.workspaceRoot()) }

// Commit confirma lo staged con el mensaje que arma el usuario en el panel.
func (a *App) Commit(message string) error { return gitCommit(a.workspaceRoot(), message) }

// GenerateCommitMessage genera un mensaje de commit a partir del diff staged.
// Falla si no hay nada staged (no hay diff que resumir).
func (a *App) GenerateCommitMessage() (string, error) {
	diff, err := runGit(a.workspaceRoot(), "diff", "--cached")
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

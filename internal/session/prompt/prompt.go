package prompt

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Prompts base embebidos. Uno por familia de modelo mas un fallback.
//
//go:embed anthropic.txt
var anthropicPrompt string

//go:embed default.txt
var defaultPrompt string

// Env son los datos de runtime que van en el bloque <env>. Se inyectan por
// parametro (no se leen del reloj ni del SO aqui) para que Build sea pura.
type Env struct {
	WorkingDir   string
	WorktreeRoot string
	IsGitRepo    bool
	Platform     string
	Date         string
}

// Select elige el prompt base segun el id del modelo (substring case-insensitive).
// Hoy solo distingue Anthropic; cualquier otro id cae al fallback.
func Select(modelID string) string {
	if strings.Contains(strings.ToLower(modelID), "claude") {
		return anthropicPrompt
	}
	return defaultPrompt
}

// Build concatena el prompt base, el bloque <env> y las instrucciones del repo.
// Si instructions esta vacio, se omite. El separador entre piezas es "\n\n".
func Build(modelID string, env Env, instructions string) string {
	parts := []string{
		Select(modelID),
		renderEnv(env),
	}
	if instructions != "" {
		parts = append(parts, instructions)
	}
	return strings.Join(parts, "\n\n")
}

// renderEnv arma el bloque <env> literal con dos espacios de indentacion.
func renderEnv(env Env) string {
	return fmt.Sprintf("<env>\n"+
		"  Working directory: %s\n"+
		"  Workspace root folder: %s\n"+
		"  Is directory a git repo: %s\n"+
		"  Platform: %s\n"+
		"  Today's date: %s\n"+
		"</env>",
		env.WorkingDir,
		env.WorktreeRoot,
		yesNo(env.IsGitRepo),
		env.Platform,
		env.Date,
	)
}

// yesNo mapea el flag de git al texto que espera el bloque <env>.
func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// LoadInstructions sube desde dir hasta root inclusive y devuelve el primer
// AGENTS.md o CLAUDE.md hallado, formateado con su ruta absoluta. Si no halla
// ninguno devuelve "" y nil.
func LoadInstructions(dir, root string) (string, error) {
	candidates := []string{"AGENTS.md", "CLAUDE.md"}
	current := dir
	for {
		for _, name := range candidates {
			path := filepath.Join(current, name)
			if _, err := os.Stat(path); err == nil {
				content, err := os.ReadFile(path)
				if err != nil {
					return "", err
				}
				return "Instructions from: " + path + "\n" + string(content), nil
			}
		}
		// Paramos tras procesar root (inclusive).
		if current == root {
			break
		}
		current = filepath.Dir(current)
	}
	return "", nil
}

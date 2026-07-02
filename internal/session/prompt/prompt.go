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

// Prompt base de los endpoints locales (LM Studio, Ollama). A diferencia del default
// (persona code-gen), establece el loop agentico y el protocolo de tools por
// function-calling, sin el patron de salida "code-first / skipped:" que hacia que un
// modelo local narrara la tool call como texto en vez de ejecutarla.
//
//go:embed local.txt
var localPrompt string

// Bloque de instrucciones para el modo plan, embebido. Se agrega al final de
// la salida normal de Build.
//
//go:embed plan.txt
var planInstructions string

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

// Build concatena el prompt base (elegido por familia de modelo), el bloque <env>,
// las instrucciones del repo y el bloque de skills disponibles. instructions y skills
// se omiten si vienen vacios. El separador entre piezas es "\n\n". El bloque de skills
// (metadatos de las skills, ver internal/skill.Format) va al final, como en opencode.
func Build(modelID string, env Env, instructions, skills string) string {
	return assemble(Select(modelID), env, instructions, skills)
}

// BuildLocal arma el prompt de un endpoint local (LM Studio, Ollama) sobre el prompt
// base local en vez de elegir por familia de modelo: los ids locales son arbitrarios,
// asi que la familia no sirve para enrutar. Mismo bloque <env>+instrucciones+skills
// que Build.
func BuildLocal(env Env, instructions, skills string) string {
	return assemble(localPrompt, env, instructions, skills)
}

// assemble concatena el prompt base con el bloque <env>, las instrucciones del repo y
// el bloque de skills, omitiendo los dos ultimos si vienen vacios. Lo comparten Build
// (base por familia) y BuildLocal (base local).
func assemble(base string, env Env, instructions, skills string) string {
	parts := []string{base, renderEnv(env)}
	if instructions != "" {
		parts = append(parts, instructions)
	}
	if skills != "" {
		parts = append(parts, skills)
	}
	return strings.Join(parts, "\n\n")
}

// BuildPlan devuelve la salida normal de Build mas el bloque de instrucciones
// del modo plan, separado por "\n\n".
func BuildPlan(modelID string, env Env, instructions, skills string) string {
	return Build(modelID, env, instructions, skills) + "\n\n" + planInstructions
}

// BuildLocalPlan es a BuildLocal lo que BuildPlan a Build: el prompt local mas el
// contrato del modo plan.
func BuildLocalPlan(env Env, instructions, skills string) string {
	return BuildLocal(env, instructions, skills) + "\n\n" + planInstructions
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

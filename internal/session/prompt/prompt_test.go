package prompt

// Tests white-box (mismo package prompt) para poder referenciar simbolos no
// exportados como anthropicPrompt. Happy-path, uno por funcion publica de la
// capa de system prompt: Select, Build y LoadInstructions.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Select es puro: si el id (case-insensitive) contiene "claude" devuelve el
// prompt base de Anthropic embebido.
func TestSelect_PicksAnthropicForClaude(t *testing.T) {
	got := Select("claude-opus-4-8")
	if got != anthropicPrompt {
		t.Fatalf("Select(claude) = %q, quiero el anthropicPrompt embebido", got)
	}
}

// Build es puro: concatena el prompt base elegido por Select con el bloque de
// entorno literal. Con IsGitRepo true el bloque dice "yes".
func TestBuild_RendersEnvBlockAndBase(t *testing.T) {
	env := Env{
		WorkingDir:   "/home/u/dev/atenea",
		WorktreeRoot: "/home/u/dev/atenea",
		IsGitRepo:    true,
		Platform:     "linux",
		Date:         "Mon Jun 22 2026",
	}
	got := Build("claude-opus-4-8", env, "", "")

	if !strings.Contains(got, anthropicPrompt) {
		t.Fatalf("Build no contiene el anthropicPrompt base; got:\n%s", got)
	}

	wantBlock := "<env>\n" +
		"  Working directory: /home/u/dev/atenea\n" +
		"  Workspace root folder: /home/u/dev/atenea\n" +
		"  Is directory a git repo: yes\n" +
		"  Platform: linux\n" +
		"  Today's date: Mon Jun 22 2026\n" +
		"</env>"
	if !strings.Contains(got, wantBlock) {
		t.Fatalf("Build no contiene el bloque <env> literal esperado.\nquiero contener:\n%s\ngot:\n%s", wantBlock, got)
	}
}

// Build anexa el bloque de skills (metadatos para el disclosure progresivo) al
// final de la salida cuando no viene vacio. RED: hoy Build ignora el parametro.
func TestBuild_AppendsSkillsBlock(t *testing.T) {
	env := Env{
		WorkingDir:   "/r",
		WorktreeRoot: "/r",
		IsGitRepo:    true,
		Platform:     "linux",
		Date:         "Mon Jun 22 2026",
	}
	skills := "<available_skills>\n  <skill>\n    <name>demo</name>\n  </skill>\n</available_skills>"
	got := Build("claude-x", env, "", skills)
	if !strings.Contains(got, skills) {
		t.Fatalf("Build no anexa el bloque de skills.\nquiero contener:\n%s\ngot:\n%s", skills, got)
	}
}

// Con skills vacio no se agrega un bloque vacio ni saltos extra (mismo contrato
// que instructions vacio).
func TestBuild_OmitsEmptySkills(t *testing.T) {
	env := Env{
		WorkingDir:   "/r",
		WorktreeRoot: "/r",
		IsGitRepo:    true,
		Platform:     "linux",
		Date:         "Mon Jun 22 2026",
	}
	got := Build("claude-x", env, "", "")
	if strings.Contains(got, "<available_skills>") {
		t.Fatalf("Build con skills vacio no debe contener <available_skills>; got:\n%s", got)
	}
	if strings.HasSuffix(got, "\n") {
		t.Fatalf("Build con skills vacio no debe terminar con saltos extra; got:\n%q", got)
	}
}

// TRIANGULATE: con instructions y skills presentes, ambos aparecen y el bloque de
// skills va DESPUES de las instrucciones (orden estable: base, env, instructions,
// skills). Tumba una implementacion que los intercale o ignore uno.
func TestBuild_SkillsAfterInstructions(t *testing.T) {
	env := Env{
		WorkingDir:   "/r",
		WorktreeRoot: "/r",
		IsGitRepo:    true,
		Platform:     "linux",
		Date:         "Mon Jun 22 2026",
	}
	instructions := "Instructions from: /r/AGENTS.md\nreglas"
	skills := "<available_skills></available_skills>"
	got := Build("claude-x", env, instructions, skills)
	if !strings.Contains(got, instructions) || !strings.Contains(got, skills) {
		t.Fatalf("Build debe incluir instructions y skills; got:\n%s", got)
	}
	if strings.Index(got, instructions) > strings.Index(got, skills) {
		t.Fatalf("el bloque de skills debe ir despues de las instrucciones; got:\n%s", got)
	}
}

// LoadInstructions sube desde dir hasta root inclusive y devuelve el primer
// AGENTS.md o CLAUDE.md hallado, formateado con su ruta absoluta.
func TestLoadInstructions_FindsAgentsMd(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	agents := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(agents, []byte("repo rules"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	got, err := LoadInstructions(dir, root)
	if err != nil {
		t.Fatalf("LoadInstructions err = %v, quiero nil", err)
	}
	want := "Instructions from: " + agents + "\nrepo rules"
	if got != want {
		t.Fatalf("LoadInstructions = %q, quiero %q", got, want)
	}
}

// TRIANGULATE: casos adicionales (happy-path extra + edge cases) que tumbarian
// una implementacion debil o hardcodeada.

// Un id sin "claude" debe caer al fallback default, no devolver siempre el de
// Anthropic. El sub-caso en mayusculas confirma que la comparacion es
// case-insensitive (no un Contains crudo sobre el original).
func TestSelect_FallsBackToDefaultForNonClaude(t *testing.T) {
	got := Select("gpt-4o")
	if got != defaultPrompt {
		t.Fatalf("Select(gpt-4o) = %q, quiero el defaultPrompt embebido", got)
	}
	if got == anthropicPrompt {
		t.Fatalf("Select(gpt-4o) devolvio el anthropicPrompt; debe ser el fallback")
	}

	upper := Select("CLAUDE-OPUS")
	if upper != anthropicPrompt {
		t.Fatalf("Select(CLAUDE-OPUS) = %q, quiero el anthropicPrompt (match case-insensitive)", upper)
	}
}

// Con IsGitRepo false el bloque <env> debe decir "no", no un "yes" hardcodeado.
func TestBuild_GitRepoNoWhenFalse(t *testing.T) {
	env := Env{
		WorkingDir:   "/tmp/x",
		WorktreeRoot: "/tmp/x",
		IsGitRepo:    false,
		Platform:     "linux",
		Date:         "Mon Jun 22 2026",
	}
	got := Build("claude-opus-4-8", env, "", "")
	if !strings.Contains(got, "Is directory a git repo: no") {
		t.Fatalf("Build con IsGitRepo=false no contiene \"Is directory a git repo: no\"; got:\n%s", got)
	}
}

// Cuando hay instrucciones, su texto debe aparecer integro en la salida.
func TestBuild_IncludesInstructionsWhenPresent(t *testing.T) {
	env := Env{
		WorkingDir:   "/r",
		WorktreeRoot: "/r",
		IsGitRepo:    true,
		Platform:     "linux",
		Date:         "Mon Jun 22 2026",
	}
	instructions := "Instructions from: /r/AGENTS.md\nreglas"
	got := Build("claude-x", env, instructions, "")
	if !strings.Contains(got, instructions) {
		t.Fatalf("Build no incluye el bloque de instrucciones.\nquiero contener:\n%s\ngot:\n%s", instructions, got)
	}
}

// Con instructions vacio no se agrega un bloque vacio: la salida no debe
// contener "Instructions from:" ni terminar con saltos de linea extra.
func TestBuild_OmitsInstructionsWhenEmpty(t *testing.T) {
	env := Env{
		WorkingDir:   "/r",
		WorktreeRoot: "/r",
		IsGitRepo:    true,
		Platform:     "linux",
		Date:         "Mon Jun 22 2026",
	}
	got := Build("claude-x", env, "", "")
	if strings.Contains(got, "Instructions from:") {
		t.Fatalf("Build con instructions vacio no debe contener \"Instructions from:\"; got:\n%s", got)
	}
	if strings.HasSuffix(got, "\n") {
		t.Fatalf("Build con instructions vacio no debe terminar con saltos extra; got termina en newline:\n%q", got)
	}
}

// En el mismo dir, AGENTS.md tiene prioridad de nombre sobre CLAUDE.md.
func TestLoadInstructions_PrefersAgentsOverClaudeSameDir(t *testing.T) {
	dir := t.TempDir()
	agents := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(agents, []byte("A"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("C"), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}

	got, err := LoadInstructions(dir, dir)
	if err != nil {
		t.Fatalf("LoadInstructions err = %v, quiero nil", err)
	}
	want := "Instructions from: " + agents + "\nA"
	if got != want {
		t.Fatalf("LoadInstructions = %q, quiero el de AGENTS.md %q", got, want)
	}
}

// Gana el archivo mas cercano al dir, no el del ancestro root.
func TestLoadInstructions_NearestDirWins(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root"), 0o644); err != nil {
		t.Fatalf("write root AGENTS.md: %v", err)
	}
	sub := filepath.Join(root, "a")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	nearAgents := filepath.Join(sub, "AGENTS.md")
	if err := os.WriteFile(nearAgents, []byte("near"), 0o644); err != nil {
		t.Fatalf("write near AGENTS.md: %v", err)
	}

	got, err := LoadInstructions(sub, root)
	if err != nil {
		t.Fatalf("LoadInstructions err = %v, quiero nil", err)
	}
	want := "Instructions from: " + nearAgents + "\nnear"
	if got != want {
		t.Fatalf("LoadInstructions = %q, quiero el mas cercano %q", got, want)
	}
}

// Sin ningun archivo entre dir y root, devuelve "" y nil sin loop infinito.
func TestLoadInstructions_NoneFoundReturnsEmpty(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got, err := LoadInstructions(dir, root)
	if err != nil {
		t.Fatalf("LoadInstructions err = %v, quiero nil", err)
	}
	if got != "" {
		t.Fatalf("LoadInstructions sin archivos = %q, quiero cadena vacia", got)
	}
}

// BuildPlan agrega el contrato de modo plan despues de la salida normal de
// Build. RED: BuildPlan y plan.txt aun no existen.
func TestBuildPlan_AppendsPlanContractToBase(t *testing.T) {
	env := Env{
		WorkingDir:   "/home/u/dev/atenea",
		WorktreeRoot: "/home/u/dev/atenea",
		IsGitRepo:    true,
		Platform:     "linux",
		Date:         "Mon Jun 22 2026",
	}
	got := BuildPlan("claude-opus-4-8", env, "", "")

	if !strings.Contains(got, Build("claude-opus-4-8", env, "", "")) {
		t.Fatalf("BuildPlan no contiene la salida normal de Build; got:\n%s", got)
	}
	if !strings.Contains(got, "present_plan") {
		t.Fatalf("BuildPlan no menciona la herramienta present_plan; got:\n%s", got)
	}
}

// TRIANGULATE: con instrucciones, su texto debe aparecer en la salida de
// BuildPlan, probando que se apoya en Build y no es una cadena hardcodeada.
func TestBuildPlan_IncludesInstructionsWhenPresent(t *testing.T) {
	env := Env{
		WorkingDir:   "/r",
		WorktreeRoot: "/r",
		IsGitRepo:    true,
		Platform:     "linux",
		Date:         "Mon Jun 22 2026",
	}
	instructions := "Instructions from: /r/AGENTS.md\nreglas"
	got := BuildPlan("claude-x", env, instructions, "")
	if !strings.Contains(got, instructions) {
		t.Fatalf("BuildPlan no incluye el bloque de instrucciones.\nquiero contener:\n%s\ngot:\n%s", instructions, got)
	}
}

// Cableado del //go:embed de plan.txt: no vacio y distinto de los prompts base.
func TestPlanInstructions_EmbeddedNonEmpty(t *testing.T) {
	if strings.TrimSpace(planInstructions) == "" {
		t.Errorf("planInstructions vacio (embed mal cableado o plan.txt vacio)")
	}
	if planInstructions == anthropicPrompt {
		t.Errorf("planInstructions igual a anthropicPrompt (embed apunta al .txt equivocado)")
	}
	if planInstructions == defaultPrompt {
		t.Errorf("planInstructions igual a defaultPrompt (embed apunta al .txt equivocado)")
	}
}

// Cableado de los //go:embed: ambos prompts base deben ser no vacios y
// distintos entre si. Si ambos //go:embed apuntan al mismo .txt, o un .txt
// queda vacio, este test los tumba (analogo a descriptions_test.go en tool).
func TestPromptsEmbedded_WiredAndDistinct(t *testing.T) {
	if strings.TrimSpace(anthropicPrompt) == "" {
		t.Errorf("anthropicPrompt vacio (embed mal cableado o .txt vacio)")
	}
	if strings.TrimSpace(defaultPrompt) == "" {
		t.Errorf("defaultPrompt vacio (embed mal cableado o .txt vacio)")
	}
	if anthropicPrompt == defaultPrompt {
		t.Errorf("anthropicPrompt y defaultPrompt son iguales (ambos //go:embed apuntan al mismo .txt)")
	}
}

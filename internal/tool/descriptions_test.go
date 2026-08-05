package tool

import (
	"strings"
	"testing"

	"github.com/K3N4Y/atenea/internal/tool/editmode"
)

// TestBuiltinDescriptions_WiredAndDistinct protege el cableado de las
// descripciones embebidas con //go:embed: cada builtin debe devolver una
// descripcion no vacia y todas deben ser distintas entre si. Si un //go:embed
// apunta al .txt equivocado (copiar/pegar entre tools) dos descripciones
// coincidirian; si un .txt queda vacio la descripcion seria "".
func TestBuiltinDescriptions_WiredAndDistinct(t *testing.T) {
	builtins := []Tool{
		&ReadTool{},
		&WriteTool{},
		&EditTool{},
		&GrepTool{},
		&GlobTool{},
		&BashTool{},
		&PresentPlanTool{},
		&SkillTool{},
		TodoWriteTool{},
		Echo{},
		NewLSPTool(t.TempDir()),
		NewASTTool(t.TempDir()),
		NewDebugTool(t.TempDir()),
	}

	seen := make(map[string]string, len(builtins))
	for _, b := range builtins {
		desc := strings.TrimSpace(b.Description())
		if desc == "" {
			t.Errorf("%s: descripcion vacia", b.Name())
			continue
		}
		if other, dup := seen[desc]; dup {
			t.Errorf("%s y %s comparten la misma descripcion (embed mal cableado)", other, b.Name())
		}
		seen[desc] = b.Name()
	}
}

func TestBuiltinDescriptionsFollowStandardEnglishFormat(t *testing.T) {
	for _, builtin := range builtinDescriptionTools(t) {
		t.Run(builtin.Name(), func(t *testing.T) {
			assertStandardDescriptionFormat(t, builtin.Description())
		})
	}
}

func builtinDescriptionTools(t *testing.T) []Tool {
	t.Helper()
	return []Tool{
		&ReadTool{},
		&WriteTool{},
		&EditTool{},
		&GrepTool{},
		&GlobTool{},
		&BashTool{},
		&PresentPlanTool{},
		&SkillTool{},
		TodoWriteTool{},
		Echo{},
		NewWebFetchTool(nil),
		NewRetainMemoryTool("", nil),
		NewRecallMemoryTool("", nil),
		NewCheckpointTool(nil),
		NewRewindTool(nil),
		NewLSPTool(t.TempDir()),
		NewASTTool(t.TempDir()),
		NewDebugTool(t.TempDir()),
	}
}

func assertStandardDescriptionFormat(t *testing.T, description string) {
	t.Helper()
	previous := -1
	for _, heading := range []string{"## Input grammar", "## Examples", "## Recoverable failures", "## Anti-patterns", "<critical>", "</critical>"} {
		at := strings.Index(description, heading)
		if at < 0 {
			t.Errorf("description is missing %q", heading)
			continue
		}
		if at <= previous {
			t.Errorf("%q is out of order", heading)
		}
		previous = at
	}
	for _, marker := range []string{
		"\nuso:", "\nnotas:", " archivo", " directorio", " comando", " tarea",
		" patrón", " patron ", " página", " pagina", " sesión", " sesion",
		" requerido", " requerida", " máximo", " maximo", " buscar ",
		" encuentra ", " carga ", " devuelve ", " presenta ", " relativo al ",
		" por ejemplo",
	} {
		if strings.Contains(strings.ToLower(description), marker) {
			t.Errorf("description contains Spanish marker %q", marker)
		}
	}
	for _, marker := range []rune("áéíóúñ¿¡") {
		if strings.ContainsRune(strings.ToLower(description), marker) {
			t.Errorf("description contains Spanish rune %q", marker)
		}
	}
}

func TestEditModeDescriptionsFollowStandardFormat(t *testing.T) {
	for _, mode := range []editmode.Mode{editmode.Hashline, editmode.Patch, editmode.Replace, editmode.ApplyPatch} {
		t.Run(string(mode), func(t *testing.T) {
			edit := &EditTool{Mode: mode}
			assertStandardDescriptionFormat(t, edit.Description())
		})
	}
}

func TestHighLeverageDescriptionsTeachToolDecisions(t *testing.T) {
	highLeverage := []struct {
		tool  Tool
		wants []string
	}{
		{tool: &ReadTool{}, wants: []string{"path", ":N-M", "without a header"}},
		{tool: &EditTool{}, wants: []string{"[PATH#TAG]", "PUT N.=M:", "re-read"}},
		{tool: &BashTool{}, wants: []string{"command", "slow_ok", "requires user approval"}},
		{tool: NewLSPTool(t.TempDir()), wants: []string{"1-based", "new_name", "no symbol"}},
		{tool: NewASTTool(t.TempDir()), wants: []string{"pattern", "apply=true", "does not bound applied replacements"}},
	}

	for _, subject := range highLeverage {
		t.Run(subject.tool.Name(), func(t *testing.T) {
			description := subject.tool.Description()
			for _, required := range append([]string{"## Input grammar", "## Examples", "## Recoverable failures", "WRONG", "RIGHT", "<critical>", "</critical>"}, subject.wants...) {
				if !strings.Contains(description, required) {
					t.Errorf("description does not teach %q", required)
				}
			}
			if examples := descriptionSectionBulletCount(description, "## Examples"); examples < 2 || examples > 4 {
				t.Errorf("description has %d canonical examples, want 2-4", examples)
			}
		})
	}
}

func descriptionSectionBulletCount(description, heading string) int {
	section := strings.SplitN(description, heading+"\n", 2)
	if len(section) != 2 {
		return 0
	}
	body := strings.SplitN(section[1], "\n## ", 2)[0]
	count := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "- ") {
			count++
		}
	}
	return count
}

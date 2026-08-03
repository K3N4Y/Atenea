package tool

import (
	"strings"
	"testing"
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

func TestHighLeverageDescriptionsTeachToolDecisions(t *testing.T) {
	highLeverage := []struct {
		tool  Tool
		wants []string
	}{
		{tool: &ReadTool{}, wants: []string{"path", ":N-M", "without a header"}},
		{tool: &EditTool{}, wants: []string{"[path#HASH]", "SWAP a.=b:", "re-read"}},
		{tool: &BashTool{}, wants: []string{"command", "slow_ok", "requires user approval"}},
		{tool: NewLSPTool(t.TempDir()), wants: []string{"1-based", "new_name", "no symbol"}},
		{tool: NewASTTool(t.TempDir()), wants: []string{"pattern", "apply=true", "does not bound applied replacements"}},
	}

	for _, subject := range highLeverage {
		t.Run(subject.tool.Name(), func(t *testing.T) {
			description := subject.tool.Description()
			for _, required := range append([]string{"## Input grammar", "## Examples", "## Recoverable failures", "WRONG:", "RIGHT:", "<critical>", "</critical>"}, subject.wants...) {
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

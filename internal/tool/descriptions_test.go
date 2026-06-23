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
		Echo{},
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

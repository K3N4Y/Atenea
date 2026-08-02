package agent

import (
	"fmt"
	"io/fs"

	packaged "github.com/K3N4Y/atenea/agents"
)

// Builtins returns the canonical subagent definitions shipped with Atenea.
// Catalog merges user definitions before these, so users can override any
// built-in by name without changing the packaged manifests.
func Builtins() []Def {
	defs, err := builtins(packaged.Manifests)
	if err != nil {
		panic(fmt.Sprintf("agent: invalid packaged subagent: %v", err))
	}
	return defs
}

func builtins(manifests fs.FS) ([]Def, error) {
	entries, err := fs.Glob(manifests, "*.md")
	if err != nil {
		return nil, err
	}
	defs := make([]Def, 0, len(entries)+2)
	for _, path := range entries {
		raw, err := fs.ReadFile(manifests, path)
		if err != nil {
			return nil, err
		}
		def, err := Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		defs = append(defs, def)
	}
	return append(defs, legacyBuiltins()...), nil
}

// legacyBuiltins remain in code until their canonical manifests are migrated.
// They deliberately omit task: nested delegation is opt-in.
func legacyBuiltins() []Def {
	return []Def{
		{
			Name:        "explore",
			Description: "Explora el codigo en modo solo lectura y devuelve un informe.",
			Tools:       []string{"read", "grep", "glob"},
			Prompt:      "Eres un subagente de exploracion de solo lectura. Investiga el codigo del workspace y devuelve un informe conciso. No modificas archivos ni ejecutas comandos.",
		},
		{
			Name:        "general",
			Description: "Subagente de proposito general con acceso completo a las tools.",
			Tools:       []string{"read", "grep", "glob", "edit", "write", "bash"},
			Prompt:      "Eres un subagente de proposito general. Investiga y resuelve la tarea del workspace usando las tools disponibles y devuelve un informe conciso.",
		},
	}
}

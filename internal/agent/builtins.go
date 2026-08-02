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
	defs := make([]Def, 0, len(entries))
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
	return defs, nil
}

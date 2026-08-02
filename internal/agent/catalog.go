package agent

// Catalog merges subagent definitions discovered from dirs with the packaged
// built-ins. Workspace definitions win by name, so a user can redefine
// explorer without losing the rest of the canonical catalog. Missing
// directories contribute no definitions.
func Catalog(dirs ...string) ([]Def, error) {
	discovered, err := Discover(dirs...)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(discovered))
	out := make([]Def, 0, len(discovered))
	for _, d := range discovered {
		if !seen[d.Name] {
			seen[d.Name] = true
			out = append(out, d)
		}
	}
	for _, d := range Builtins() {
		if !seen[d.Name] {
			seen[d.Name] = true
			out = append(out, d)
		}
	}
	return out, nil
}

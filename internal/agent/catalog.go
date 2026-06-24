package agent

// Catalog fusiona las defs de subagente descubiertas en dirs con las built-in:
// las del workspace GANAN sobre un built-in homonimo (override), y los built-in
// no sobreescritos se conservan. Asi un usuario puede redefinir 'explore' con un
// .md propio sin perder el resto del catalogo canonico. Un dir inexistente no
// aporta defs (ver Discover).
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

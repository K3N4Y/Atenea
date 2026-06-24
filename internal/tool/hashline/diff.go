package hashline

import "github.com/pmezard/go-difflib/difflib"

// UnifiedDiff arma un diff unificado (estilo git) entre oldText y newText con
// `context` lineas de contexto. Los headers usan "a/path" y "b/path" para que el
// frontend saque la ruta y el lenguaje. Textos iguales devuelven "".
//
// No usa difflib.SplitLines: ese helper hace SplitAfter y fuerza un "\n" al
// ultimo segmento, lo que con texto terminado en "\n" deja una linea fantasma en
// blanco al final del diff. Reusamos SplitLines (que descarta el segmento vacio
// final) y le devolvemos el "\n" a cada linea, que es lo que difflib espera.
func UnifiedDiff(path, oldText, newText string, context int) string {
	out, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        diffLines(oldText),
		B:        diffLines(newText),
		FromFile: "a/" + path,
		ToFile:   "b/" + path,
		Context:  context,
	})
	if err != nil {
		return ""
	}
	return out
}

// diffLines parte el texto en lineas terminadas en "\n", sin el segmento vacio
// fantasma. Texto vacio -> 0 lineas (caso write: archivo nuevo, todo adicion).
func diffLines(text string) []string {
	parts := SplitLines(text)
	lines := make([]string, len(parts))
	for i, p := range parts {
		lines[i] = p + "\n"
	}
	return lines
}

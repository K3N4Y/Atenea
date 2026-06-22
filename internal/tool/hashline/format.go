package hashline

import (
	"fmt"
	"strings"
)

// FormatHeader arma el header "[path#HASH]" que el read emite y el edit re-parsea
// para sacar path y hash esperado.
func FormatHeader(path, hash string) string {
	return "[" + path + "#" + hash + "]"
}

// SplitLines parte el texto por "\n". Si el texto termina en "\n", descarta el
// segmento vacio final; un texto vacio devuelve 0 lineas. Asi "a\nb\n" y "a\nb"
// dan ambos ["a","b"].
func SplitLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// NumberLines numera las lineas desde from hasta to inclusive (1-indexed sobre el
// slice), con formato "%d:%s", unidas por "\n" y sin "\n" final.
func NumberLines(lines []string, from, to int) string {
	var b strings.Builder
	for i := from; i <= to; i++ {
		if i > from {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%d:%s", i, lines[i-1])
	}
	return b.String()
}

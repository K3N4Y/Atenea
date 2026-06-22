package hashline

import "testing"

// TestFormatHeader_WrapsPathAndHash afirma que el header es "[path#HASH]": el
// edit lo re-parsea para sacar path y hash esperado.
func TestFormatHeader_WrapsPathAndHash(t *testing.T) {
	got := FormatHeader("internal/foo.go", "1A2B")
	if want := "[internal/foo.go#1A2B]"; got != want {
		t.Fatalf("FormatHeader: se esperaba %q, se obtuvo %q", want, got)
	}
}

// TestSplitLines_DropsTrailingNewlineSegment afirma que el segmento vacio final
// de un texto terminado en "\n" NO cuenta como linea: "a\nb\n" y "a\nb" tienen
// ambos 2 lineas, ["a","b"].
func TestSplitLines_DropsTrailingNewlineSegment(t *testing.T) {
	for _, text := range []string{"a\nb\n", "a\nb"} {
		got := SplitLines(text)
		if len(got) != 2 {
			t.Fatalf("SplitLines(%q): se esperaba longitud 2, se obtuvo %d (%v)", text, len(got), got)
		}
		if got[0] != "a" || got[1] != "b" {
			t.Fatalf("SplitLines(%q): se esperaba [a b], se obtuvo %v", text, got)
		}
	}
}

// TestNumberLines_UsesRealPositions afirma que NumberLines usa los numeros reales
// 1-indexed sobre el slice completo, separador ":", lineas unidas por "\n" y sin
// "\n" final.
func TestNumberLines_UsesRealPositions(t *testing.T) {
	lines := []string{"package main", "", "func main(){}"}

	if got, want := NumberLines(lines, 2, 3), "2:\n3:func main(){}"; got != want {
		t.Fatalf("NumberLines(lines, 2, 3): se esperaba %q, se obtuvo %q", want, got)
	}

	if got, want := NumberLines(lines, 1, 3), "1:package main\n2:\n3:func main(){}"; got != want {
		t.Fatalf("NumberLines(lines, 1, 3): se esperaba %q, se obtuvo %q", want, got)
	}
}

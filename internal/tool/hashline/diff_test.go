package hashline

import (
	"strings"
	"testing"
)

// TestUnifiedDiff_BasicChange: un cambio de una linea produce el `-` viejo, el
// `+` nuevo, contexto alrededor y el header de hunk con los rangos correctos. El
// rango "@@ -1,3 +1,3 @@" (no -1,4) prueba que NO hay linea fantasma en blanco
// (el bug de difflib.SplitLines que fuerza un "\n" extra al final).
func TestUnifiedDiff_BasicChange(t *testing.T) {
	out := UnifiedDiff("foo.go", "a\nb\nc\n", "a\nB\nc\n", 3)

	for _, want := range []string{
		"--- a/foo.go",
		"+++ b/foo.go",
		"@@ -1,3 +1,3 @@",
		"\n a\n",
		"\n-b\n",
		"\n+B\n",
		"\n c\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("diff no contiene %q\n--- diff ---\n%s", want, out)
		}
	}
}

// TestUnifiedDiff_NewFile: con old vacio (caso write) todo es adicion y el rango
// arranca en -0,0.
func TestUnifiedDiff_NewFile(t *testing.T) {
	out := UnifiedDiff("new.txt", "", "x\ny\n", 3)

	if !strings.Contains(out, "@@ -0,0 +1,2 @@") {
		t.Fatalf("falta hunk de archivo nuevo\n%s", out)
	}
	for _, want := range []string{"\n+x\n", "\n+y\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("diff no contiene %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "\n-") {
		t.Fatalf("archivo nuevo no debe tener lineas borradas\n%s", out)
	}
}

// TestUnifiedDiff_NoTrailingNewline: si los textos no terminan en "\n" el diff
// sigue siendo coherente (una linea cambiada) y sin linea fantasma.
func TestUnifiedDiff_NoTrailingNewline(t *testing.T) {
	out := UnifiedDiff("foo.go", "a\nb", "a\nB", 3)

	if !strings.Contains(out, "@@ -1,2 +1,2 @@") {
		t.Fatalf("rango inesperado (linea fantasma?)\n%s", out)
	}
	if !strings.Contains(out, "\n-b\n") || !strings.Contains(out, "\n+B\n") {
		t.Fatalf("falta el cambio b->B\n%s", out)
	}
}

// TestUnifiedDiff_NoChange: textos iguales -> diff vacio (el patcher no escribe en
// no-op, pero el helper debe ser robusto).
func TestUnifiedDiff_NoChange(t *testing.T) {
	if out := UnifiedDiff("foo.go", "a\nb\n", "a\nb\n", 3); out != "" {
		t.Fatalf("se esperaba diff vacio, se obtuvo:\n%s", out)
	}
}

// TestUnifiedDiff_MultiHunk: dos cambios distantes producen dos headers de hunk.
func TestUnifiedDiff_MultiHunk(t *testing.T) {
	old := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"
	neu := "1\nX\n3\n4\n5\n6\n7\n8\nY\n10\n"

	out := UnifiedDiff("foo.go", old, neu, 1)

	if got := strings.Count(out, "@@ "); got != 2 {
		t.Fatalf("se esperaban 2 hunks, hubo %d\n%s", got, out)
	}
}

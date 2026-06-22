package hashline

import (
	"regexp"
	"testing"
)

// TestComputeFileHash_StableAcrossCRLFAndTrailingWhitespace afirma que la
// normalizacion (trailing whitespace + CR) hace que dos textos que solo difieren
// en eso hasheen igual: es la base de la read-fusion y de que CRLF/LF no
// invaliden el tag.
func TestComputeFileHash_StableAcrossCRLFAndTrailingWhitespace(t *testing.T) {
	got := ComputeFileHash("a \nb\r\n")
	want := ComputeFileHash("a\nb\n")
	if got != want {
		t.Fatalf("ComputeFileHash: se esperaba el mismo tag tras normalizar, se obtuvo %q vs %q", got, want)
	}
}

// TestComputeFileHash_FourUppercaseHex afirma que el tag de un texto cualquiera
// son 4 hex mayusculas: el formato del header [path#HASH].
func TestComputeFileHash_FourUppercaseHex(t *testing.T) {
	tag := ComputeFileHash("contenido cualquiera\n")
	if !regexp.MustCompile(`^[0-9A-F]{4}$`).MatchString(tag) {
		t.Fatalf("ComputeFileHash: se esperaba ^[0-9A-F]{4}$, se obtuvo %q", tag)
	}
}

// TestComputeFileHash_ChangesOnContentChange afirma que dos contenidos claramente
// distintos producen tags distintos: si el hash fuera constante (o ignorara el
// contenido) el edit no podria detectar que el archivo cambio bajo sus pies. Este
// par no colisiona en 16 bits (3B2D vs B30D), verificado fuera del test.
func TestComputeFileHash_ChangesOnContentChange(t *testing.T) {
	a := ComputeFileHash("hello world\n")
	b := ComputeFileHash("goodbye world\n")
	if a == b {
		t.Fatalf("ComputeFileHash: se esperaban tags distintos para contenidos distintos, ambos %q", a)
	}
}

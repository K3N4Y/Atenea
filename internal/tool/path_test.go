package tool

import (
	"strings"
	"testing"
)

// TestSandboxJoin_AcceptsAbsolutePathInsideRoot afirma que una ruta absoluta que
// cae dentro del root se acepta y resuelve al MISMO abs que su equivalente
// relativa. El modelo conoce el root por el system prompt y usa rutas absolutas
// de forma natural; rechazarlas era un falso negativo.
func TestSandboxJoin_AcceptsAbsolutePathInsideRoot(t *testing.T) {
	absGot, err := sandboxJoin("/work", "/work/sub/foo.go", "read")
	if err != nil {
		t.Fatalf("sandboxJoin absoluta dentro del root: error inesperado: %v", err)
	}
	relGot, err := sandboxJoin("/work", "sub/foo.go", "read")
	if err != nil {
		t.Fatalf("sandboxJoin relativa: error inesperado: %v", err)
	}
	if absGot != relGot {
		t.Fatalf("sandboxJoin: absoluta resolvio %q, la relativa equivalente %q", absGot, relGot)
	}
}

// TestSandboxJoin_AcceptsRootItself afirma el caso borde: la ruta absoluta que ES
// el root no debe decir "fuera del workspace" (si luego es un read de directorio,
// fallara mas adelante con su propio error de lectura, no aqui).
func TestSandboxJoin_AcceptsRootItself(t *testing.T) {
	got, err := sandboxJoin("/work", "/work", "read")
	if err != nil {
		t.Fatalf("sandboxJoin con el root mismo: error inesperado: %v", err)
	}
	if got != "/work" {
		t.Fatalf("sandboxJoin = %q, quiero %q", got, "/work")
	}
}

// TestSandboxJoin_RejectsAbsolutePathOutsideRoot afirma que las absolutas FUERA
// del root se siguen rechazando con el mismo error, incluida una que escapa con
// ".." aunque su prefijo textual sea el root.
func TestSandboxJoin_RejectsAbsolutePathOutsideRoot(t *testing.T) {
	for _, path := range []string{"/etc/passwd", "/work/../otro", "/workspace/foo.go"} {
		_, err := sandboxJoin("/work", path, "read")
		if err == nil {
			t.Fatalf("sandboxJoin(%q): se esperaba error por ruta fuera del workspace", path)
		}
		if !strings.Contains(err.Error(), "fuera del workspace") {
			t.Fatalf("sandboxJoin(%q): error %q, quiero que mencione 'fuera del workspace'", path, err)
		}
	}
}

package tool

import (
	"strings"
	"testing"

	"atenea/internal/tool/hashline"
)

// TestEditDescription_ExampleParses protege el ejemplo embebido en edit.txt: el
// patch que la descripcion le muestra al modelo tiene que ser sintaxis valida para
// el parser real. Si el ejemplo deriva a una forma que ParsePatch rechaza (un ":"
// de menos, un rango "a=b" sin el punto, un INS.* con rango), el modelo copiaria
// algo que falla. Extrae el bloque indentado del ejemplo y lo pasa por el parser.
func TestEditDescription_ExampleParses(t *testing.T) {
	example := indentedExample((&EditTool{}).Description())
	if !strings.Contains(example, "SWAP") {
		t.Fatalf("no se pudo extraer el ejemplo de edit.txt; bloque = %q", example)
	}

	patch, err := hashline.ParsePatch(example)
	if err != nil {
		t.Fatalf("el ejemplo de edit.txt no parsea con el parser real: %v\n---\n%s", err, example)
	}
	if got := len(patch.Sections[0].Edits); got != 2 {
		t.Errorf("ejemplo de edit.txt: %d edits, quiero 2 (SWAP + INS.TAIL)", got)
	}
}

// indentedExample devuelve el primer bloque de lineas indentadas con 4 espacios de
// la descripcion, sin esa indentacion. En edit.txt ese bloque es el patch de
// ejemplo (el resto del texto no esta indentado), asi que reconstruye el patch tal
// como lo veria el modelo al copiarlo.
func indentedExample(desc string) string {
	var block []string
	inBlock := false
	for _, ln := range strings.Split(desc, "\n") {
		switch {
		case strings.HasPrefix(ln, "    "):
			block = append(block, strings.TrimPrefix(ln, "    "))
			inBlock = true
		case inBlock:
			return strings.Join(block, "\n")
		}
	}
	return strings.Join(block, "\n")
}

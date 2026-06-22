package hashline

import (
	"errors"
	"testing"
)

// TestParsePatch_HeaderAndSwap afirma que ParsePatch lee el header [ruta#HASH] y
// un hunk SWAP start.=end con su payload a una sola seccion con un Edit Replace:
// el rango es 1-indexed inclusive y el Text son las lineas de payload sin el '+',
// unidas por "\n" (con el tab real de la segunda linea).
func TestParsePatch_HeaderAndSwap(t *testing.T) {
	patch := `[internal/foo.go#1A2B]
SWAP 3.=5:
+func main() {
+	fmt.Println("hola")
+}
`

	p, err := ParsePatch(patch)
	if err != nil {
		t.Fatalf("ParsePatch: no se esperaba error, se obtuvo %v", err)
	}

	if len(p.Sections) != 1 {
		t.Fatalf("ParsePatch: se esperaba 1 seccion, se obtuvo %d", len(p.Sections))
	}

	s := p.Sections[0]
	if s.Path != "internal/foo.go" {
		t.Fatalf("Section.Path: se esperaba %q, se obtuvo %q", "internal/foo.go", s.Path)
	}
	if s.Hash != "1A2B" {
		t.Fatalf("Section.Hash: se esperaba %q, se obtuvo %q", "1A2B", s.Hash)
	}
	if len(s.Edits) != 1 {
		t.Fatalf("Section.Edits: se esperaba 1 edit, se obtuvo %d", len(s.Edits))
	}

	e := s.Edits[0]
	if e.Kind != Replace {
		t.Fatalf("Edit.Kind: se esperaba Replace, se obtuvo %v", e.Kind)
	}
	if want := (Range{Start: 3, End: 5}); e.Range != want {
		t.Fatalf("Edit.Range: se esperaba %+v, se obtuvo %+v", want, e.Range)
	}
	if want := "func main() {\n\tfmt.Println(\"hola\")\n}"; e.Text != want {
		t.Fatalf("Edit.Text: se esperaba %q, se obtuvo %q", want, e.Text)
	}
}

// TestParsePatch_MissingHeaderErrors afirma que un patch cuya primera linea no es
// el header [ruta#HASH] (aqui empieza con un hunk SWAP) falla con *MissingTagError.
func TestParsePatch_MissingHeaderErrors(t *testing.T) {
	patch := `SWAP 1:
+x
`

	_, err := ParsePatch(patch)
	if err == nil {
		t.Fatalf("ParsePatch: se esperaba error por header faltante, se obtuvo nil")
	}

	var mte *MissingTagError
	if !errors.As(err, &mte) {
		t.Fatalf("ParsePatch: se esperaba *MissingTagError, se obtuvo %T (%v)", err, err)
	}
}

// TestParsePatch_DeleteAndInsertVariants afirma que ParsePatch entiende los hunks
// DEL (linea suelta y rango) e INS.* (PRE/POST con ancla, HEAD/TAIL sin ancla) bajo
// un solo header, devolviendo los 6 edits en orden con sus campos.
func TestParsePatch_DeleteAndInsertVariants(t *testing.T) {
	patch := `[internal/foo.go#1A2B]
DEL 4
DEL 6.=8
INS.PRE 10:
+a
INS.POST 12:
+b
INS.HEAD:
+top
INS.TAIL:
+bottom
`

	p, err := ParsePatch(patch)
	if err != nil {
		t.Fatalf("ParsePatch: no se esperaba error, se obtuvo %v", err)
	}
	if len(p.Sections) != 1 {
		t.Fatalf("ParsePatch: se esperaba 1 seccion, se obtuvo %d", len(p.Sections))
	}

	got := p.Sections[0].Edits
	want := []Edit{
		{Kind: Delete, Range: Range{Start: 4, End: 4}},
		{Kind: Delete, Range: Range{Start: 6, End: 8}},
		{Kind: Insert, Cursor: BeforeAnchor, Anchor: 10, Text: "a"},
		{Kind: Insert, Cursor: AfterAnchor, Anchor: 12, Text: "b"},
		{Kind: Insert, Cursor: BOF, Text: "top"},
		{Kind: Insert, Cursor: EOF, Text: "bottom"},
	}

	if len(got) != len(want) {
		t.Fatalf("Section.Edits: se esperaba %d edits, se obtuvo %d (%+v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Edit[%d]: se esperaba %+v, se obtuvo %+v", i, want[i], got[i])
		}
	}
}

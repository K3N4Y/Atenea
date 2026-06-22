package hashline

import "testing"

// TestApplyEdits_ReplaceRange afirma que ApplyEdits reemplaza el rango 1-indexed
// inclusive [2,3] por la unica linea de payload: ["a","b","c","d"] con un Replace
// de "X" da "a\nX\nd" (lineas unidas por "\n", sin "\n" final).
func TestApplyEdits_ReplaceRange(t *testing.T) {
	lines := []string{"a", "b", "c", "d"}
	edits := []Edit{{Kind: Replace, Range: Range{Start: 2, End: 3}, Text: "X"}}

	res, err := ApplyEdits(lines, edits)
	if err != nil {
		t.Fatalf("ApplyEdits: no se esperaba error, se obtuvo %v", err)
	}
	if want := "a\nX\nd"; res.Text != want {
		t.Fatalf("ApplyResult.Text: se esperaba %q, se obtuvo %q", want, res.Text)
	}
}

// TestApplyEdits_DeleteAndInsertKeepLineNumbers afirma que varios edits cuyos
// numeros refieren al MISMO archivo original no se corren entre si: borrar la
// linea 2 ("b") e insertar "X" despues de la linea 4 ("d") da "a\nc\nd\nX\ne".
func TestApplyEdits_DeleteAndInsertKeepLineNumbers(t *testing.T) {
	lines := []string{"a", "b", "c", "d", "e"}
	edits := []Edit{
		{Kind: Delete, Range: Range{Start: 2, End: 2}},
		{Kind: Insert, Cursor: AfterAnchor, Anchor: 4, Text: "X"},
	}

	res, err := ApplyEdits(lines, edits)
	if err != nil {
		t.Fatalf("ApplyEdits: no se esperaba error, se obtuvo %v", err)
	}
	if want := "a\nc\nd\nX\ne"; res.Text != want {
		t.Fatalf("ApplyResult.Text: se esperaba %q, se obtuvo %q", want, res.Text)
	}
}

// TestApplyEdits_InsertHeadAndTail afirma que INS.HEAD/INS.TAIL insertan al inicio
// y al final del archivo respectivamente: sobre ["x"] dan "top\nx\nbottom".
func TestApplyEdits_InsertHeadAndTail(t *testing.T) {
	lines := []string{"x"}
	edits := []Edit{
		{Kind: Insert, Cursor: BOF, Text: "top"},
		{Kind: Insert, Cursor: EOF, Text: "bottom"},
	}

	res, err := ApplyEdits(lines, edits)
	if err != nil {
		t.Fatalf("ApplyEdits: no se esperaba error, se obtuvo %v", err)
	}
	if want := "top\nx\nbottom"; res.Text != want {
		t.Fatalf("ApplyResult.Text: se esperaba %q, se obtuvo %q", want, res.Text)
	}
}

// TestApplyEdits_NoOpErrors afirma que un edit que no cambia nada (reemplazar la
// linea 2 por su mismo texto) es un error explicito: el patcher no debe escribir.
func TestApplyEdits_NoOpErrors(t *testing.T) {
	lines := []string{"a", "b", "c"}
	edits := []Edit{{Kind: Replace, Range: Range{Start: 2, End: 2}, Text: "b"}}

	_, err := ApplyEdits(lines, edits)
	if err == nil {
		t.Fatalf("ApplyEdits: se esperaba error por no-op, se obtuvo nil")
	}
}

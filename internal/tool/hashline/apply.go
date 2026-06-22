package hashline

import (
	"errors"
	"strings"
)

// ApplyEdits aplica los edits a las lineas (1-indexed sobre el archivo original).
// Todos los numeros de los edits refieren al MISMO archivo original: para que un
// splice no corra los indices de otro, se construye el resultado en una sola
// pasada sobre las posiciones 0..len(lines), insertando los Insert en su ancla y
// reemplazando/borrando los rangos. Un patch que no cambia nada (no-op) devuelve
// error: el patcher no debe escribir.
func ApplyEdits(lines []string, edits []Edit) (ApplyResult, error) {
	// Inserciones agrupadas por la posicion (en el archivo original) donde caen.
	// pos = cuantas lineas originales van antes de la insercion: BOF=0, EOF=len,
	// BeforeAnchor n = n-1, AfterAnchor n = n.
	insertsAt := make(map[int][]string)
	// Borrados/reemplazos por linea original (1-indexed): deleted marca lineas a
	// omitir; replaced sustituye la primera linea del rango por el payload.
	deleted := make(map[int]bool)
	replaced := make(map[int][]string)

	firstChanged := len(lines) + 1

	for _, e := range edits {
		switch e.Kind {
		case Replace:
			payload := strings.Split(e.Text, "\n")
			for n := e.Range.Start; n <= e.Range.End; n++ {
				deleted[n] = true
			}
			replaced[e.Range.Start] = payload
			if e.Range.Start < firstChanged {
				firstChanged = e.Range.Start
			}
		case Delete:
			for n := e.Range.Start; n <= e.Range.End; n++ {
				deleted[n] = true
			}
			if e.Range.Start < firstChanged {
				firstChanged = e.Range.Start
			}
		case Insert:
			payload := strings.Split(e.Text, "\n")
			var pos, changed int
			switch e.Cursor {
			case BOF:
				pos, changed = 0, 1
			case EOF:
				pos, changed = len(lines), len(lines)+1
			case BeforeAnchor:
				pos, changed = e.Anchor-1, e.Anchor
			case AfterAnchor:
				pos, changed = e.Anchor, e.Anchor+1
			}
			insertsAt[pos] = append(insertsAt[pos], payload...)
			if changed < firstChanged {
				firstChanged = changed
			}
		}
	}

	var out []string
	// Insercion antes de cualquier linea original (posicion 0).
	out = append(out, insertsAt[0]...)
	for n := 1; n <= len(lines); n++ {
		if rep, ok := replaced[n]; ok {
			out = append(out, rep...)
		} else if !deleted[n] {
			out = append(out, lines[n-1])
		}
		// Inserciones que caen despues de la linea original n.
		out = append(out, insertsAt[n]...)
	}

	result := strings.Join(out, "\n")
	if result == strings.Join(lines, "\n") {
		return ApplyResult{}, errors.New("el patch no cambia nada (no-op)")
	}

	if firstChanged > len(lines)+1 {
		firstChanged = 0
	}

	return ApplyResult{
		Text:             result,
		FirstChangedLine: firstChanged,
	}, nil
}

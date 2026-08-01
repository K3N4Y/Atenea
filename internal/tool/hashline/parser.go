package hashline

import (
	"fmt"
	"strconv"
	"strings"
)

// ParsePatch parses Atenea's single-file hashline grammar. It deliberately
// rejects every unconsumed line: accepting typos as comments would turn a
// malformed, apparently guarded patch into a different write.
func ParsePatch(text string) (Patch, error) {
	lines := strings.Split(text, "\n")

	// Ubicar el header: primera linea no vacia.
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}

	if i >= len(lines) {
		return Patch{}, &MissingTagError{Detail: "el patch esta vacio"}
	}

	header := strings.TrimSpace(lines[i])
	if !strings.HasPrefix(header, "[") || !strings.HasSuffix(header, "]") {
		return Patch{}, &MissingTagError{Detail: "la primera linea no es [ruta#HASH], es " + strconv.Quote(header)}
	}
	header = strings.TrimSuffix(strings.TrimPrefix(header, "["), "]")
	idx := strings.LastIndex(header, "#")
	if idx <= 0 || idx == len(header)-1 || strings.ContainsAny(header[:idx], "[]\r\n") || !validHash(header[idx+1:]) {
		return Patch{}, &MissingTagError{Detail: "el header debe ser exactamente [ruta#HASH] con ruta no vacia y HASH hexadecimal de 4 digitos"}
	}
	path, hash := header[:idx], strings.ToUpper(header[idx+1:])
	i++

	var edits []Edit
	for i < len(lines) {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "SWAP "):
			if !strings.HasSuffix(line, ":") {
				return Patch{}, malformedOperation(line)
			}
			body := strings.TrimSuffix(strings.TrimPrefix(line, "SWAP "), ":")
			start, end, err := parseRange(body)
			if err != nil {
				return Patch{}, malformedOperation(line)
			}
			i++
			payload := readPayload(lines, &i)
			if len(payload) == 0 {
				return Patch{}, malformedOperation(line)
			}
			edits = append(edits, Edit{
				Kind:  Replace,
				Range: Range{Start: start, End: end},
				Text:  strings.Join(payload, "\n"),
			})
		case strings.HasPrefix(line, "DEL "):
			body := strings.TrimSpace(strings.TrimPrefix(line, "DEL "))
			start, end, err := parseRange(body)
			if err != nil {
				return Patch{}, malformedOperation(line)
			}
			i++
			edits = append(edits, Edit{
				Kind:  Delete,
				Range: Range{Start: start, End: end},
			})
		case strings.HasPrefix(line, "INS.PRE "):
			if !strings.HasSuffix(line, ":") {
				return Patch{}, malformedOperation(line)
			}
			body := strings.TrimSuffix(strings.TrimPrefix(line, "INS.PRE "), ":")
			anchor, err := parseAnchor(body)
			if err != nil {
				return Patch{}, malformedOperation(line)
			}
			i++
			payload := readPayload(lines, &i)
			if len(payload) == 0 {
				return Patch{}, malformedOperation(line)
			}
			edits = append(edits, Edit{
				Kind:   Insert,
				Cursor: BeforeAnchor,
				Anchor: anchor,
				Text:   strings.Join(payload, "\n"),
			})
		case strings.HasPrefix(line, "INS.POST "):
			if !strings.HasSuffix(line, ":") {
				return Patch{}, malformedOperation(line)
			}
			body := strings.TrimSuffix(strings.TrimPrefix(line, "INS.POST "), ":")
			anchor, err := parseAnchor(body)
			if err != nil {
				return Patch{}, malformedOperation(line)
			}
			i++
			payload := readPayload(lines, &i)
			if len(payload) == 0 {
				return Patch{}, malformedOperation(line)
			}
			edits = append(edits, Edit{
				Kind:   Insert,
				Cursor: AfterAnchor,
				Anchor: anchor,
				Text:   strings.Join(payload, "\n"),
			})
		case line == "INS.HEAD:":
			i++
			payload := readPayload(lines, &i)
			if len(payload) == 0 {
				return Patch{}, malformedOperation(line)
			}
			edits = append(edits, Edit{
				Kind:   Insert,
				Cursor: BOF,
				Text:   strings.Join(payload, "\n"),
			})
		case line == "INS.TAIL:":
			i++
			payload := readPayload(lines, &i)
			if len(payload) == 0 {
				return Patch{}, malformedOperation(line)
			}
			edits = append(edits, Edit{
				Kind:   Insert,
				Cursor: EOF,
				Text:   strings.Join(payload, "\n"),
			})
		default:
			if strings.TrimSpace(line) == "" && i == len(lines)-1 {
				i++
				continue
			}
			return Patch{}, fmt.Errorf("patch: linea desconocida o huerfana: %s", strconv.Quote(line))
		}
	}
	if len(edits) == 0 {
		return Patch{}, fmt.Errorf("patch: la seccion no contiene operaciones")
	}
	if err := validateEditConflicts(edits); err != nil {
		return Patch{}, err
	}

	return Patch{Sections: []Section{{Path: path, Hash: hash, Edits: edits}}}, nil
}

func validHash(s string) bool {
	if len(s) != 4 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

func validateEditConflicts(edits []Edit) error {
	occupied := map[int]bool{}
	inserted := map[int]bool{}
	boundaries := map[Cursor]bool{}
	for _, e := range edits {
		if e.Kind == Replace || e.Kind == Delete {
			for n := e.Range.Start; n <= e.Range.End; n++ {
				if occupied[n] {
					return fmt.Errorf("patch: operaciones solapadas en linea %d", n)
				}
				occupied[n] = true
			}
			continue
		}
		if e.Cursor == BOF || e.Cursor == EOF {
			if boundaries[e.Cursor] {
				return fmt.Errorf("patch: inserciones de borde repetidas")
			}
			boundaries[e.Cursor] = true
			continue
		}
		// Any pair of PRE/POST operations at one anchor is order-sensitive.
		if inserted[e.Anchor] {
			return fmt.Errorf("patch: inserciones en conflicto en linea %d", e.Anchor)
		}
		inserted[e.Anchor] = true
	}
	for anchor := range inserted {
		if occupied[anchor] {
			return fmt.Errorf("patch: insercion anclada dentro de rango reemplazado o borrado en linea %d", anchor)
		}
	}
	return nil
}

// parseRange interpreta el cuerpo de un rango: "n" -> [n,n]; "start.=end" ->
// [start,end].
func parseRange(body string) (int, int, error) {
	body = strings.TrimSpace(body)
	if parts := strings.SplitN(body, ".=", 2); len(parts) == 2 {
		start, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
		end, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err1 != nil || err2 != nil || start < 1 || end < start {
			return 0, 0, fmt.Errorf("rango invalido")
		}
		return start, end, nil
	}
	n, err := strconv.Atoi(body)
	if err != nil || n < 1 {
		return 0, 0, fmt.Errorf("rango invalido")
	}
	return n, n, nil
}

func parseAnchor(body string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(body))
	if err != nil || n < 1 {
		return 0, fmt.Errorf("ancla invalida")
	}
	return n, nil
}

func malformedOperation(line string) error {
	return fmt.Errorf("patch: operacion malformada: %s", strconv.Quote(line))
}

// readPayload consume las lineas "+..." consecutivas a partir de *i (que avanza),
// quitando el prefijo "+", y las devuelve.
func readPayload(lines []string, i *int) []string {
	var payload []string
	for *i < len(lines) && strings.HasPrefix(lines[*i], "+") {
		payload = append(payload, strings.TrimPrefix(lines[*i], "+"))
		*i++
	}
	return payload
}

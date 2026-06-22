package hashline

import (
	"strconv"
	"strings"
)

// ParsePatch lee el header "[path#HASH]" de la primera linea no vacia y los hunks
// que siguen. Entiende SWAP "start.=end:" (Replace), DEL "n" / "start.=end"
// (Delete) e INS.PRE/POST "n:" + INS.HEAD/TAIL ":" (Insert), con lineas de payload
// prefijadas por "+". Sin header valido devuelve *MissingTagError.
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
	header = strings.TrimPrefix(header, "[")
	header = strings.TrimSuffix(header, "]")
	idx := strings.LastIndex(header, "#")
	if idx < 0 {
		return Patch{}, &MissingTagError{Detail: "el header no trae '#' separando ruta y HASH"}
	}
	path := header[:idx]
	hash := header[idx+1:]
	i++

	var edits []Edit
	for i < len(lines) {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "SWAP "):
			body := strings.TrimSuffix(strings.TrimPrefix(line, "SWAP "), ":")
			start, end := parseRange(body)
			i++
			payload := readPayload(lines, &i)
			edits = append(edits, Edit{
				Kind:  Replace,
				Range: Range{Start: start, End: end},
				Text:  strings.Join(payload, "\n"),
			})
		case strings.HasPrefix(line, "DEL "):
			body := strings.TrimSpace(strings.TrimPrefix(line, "DEL "))
			start, end := parseRange(body)
			i++
			edits = append(edits, Edit{
				Kind:  Delete,
				Range: Range{Start: start, End: end},
			})
		case strings.HasPrefix(line, "INS.PRE "):
			body := strings.TrimSuffix(strings.TrimPrefix(line, "INS.PRE "), ":")
			anchor, _ := strconv.Atoi(strings.TrimSpace(body))
			i++
			payload := readPayload(lines, &i)
			edits = append(edits, Edit{
				Kind:   Insert,
				Cursor: BeforeAnchor,
				Anchor: anchor,
				Text:   strings.Join(payload, "\n"),
			})
		case strings.HasPrefix(line, "INS.POST "):
			body := strings.TrimSuffix(strings.TrimPrefix(line, "INS.POST "), ":")
			anchor, _ := strconv.Atoi(strings.TrimSpace(body))
			i++
			payload := readPayload(lines, &i)
			edits = append(edits, Edit{
				Kind:   Insert,
				Cursor: AfterAnchor,
				Anchor: anchor,
				Text:   strings.Join(payload, "\n"),
			})
		case strings.HasPrefix(line, "INS.HEAD:"):
			i++
			payload := readPayload(lines, &i)
			edits = append(edits, Edit{
				Kind:   Insert,
				Cursor: BOF,
				Text:   strings.Join(payload, "\n"),
			})
		case strings.HasPrefix(line, "INS.TAIL:"):
			i++
			payload := readPayload(lines, &i)
			edits = append(edits, Edit{
				Kind:   Insert,
				Cursor: EOF,
				Text:   strings.Join(payload, "\n"),
			})
		default:
			i++
		}
	}

	return Patch{Sections: []Section{{Path: path, Hash: hash, Edits: edits}}}, nil
}

// parseRange interpreta el cuerpo de un rango: "n" -> [n,n]; "start.=end" ->
// [start,end].
func parseRange(body string) (int, int) {
	body = strings.TrimSpace(body)
	if parts := strings.SplitN(body, ".=", 2); len(parts) == 2 {
		start, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
		end, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
		return start, end
	}
	n, _ := strconv.Atoi(body)
	return n, n
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

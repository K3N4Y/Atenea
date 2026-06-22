package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"atenea/internal/tool/hashline"
)

// FileReader lee el contenido de un archivo por su ruta. Es la unica dependencia
// de FS del read, asi los tests inyectan contenido sin tocar el disco.
type FileReader interface {
	ReadFile(name string) ([]byte, error)
}

// osFS es el FileReader por defecto: delega en os.ReadFile.
type osFS struct{}

func (osFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }

// ReadTool lee un archivo bajo Root, lo numera con un header [path#HASH] y graba
// el snapshot para que el edit pueda anclar contra las lineas vistas. MaxLines es
// el limite de lineas por lectura: al superarlo se trunca la salida y se anexa un
// notice de continuacion con el selector :N para leer el resto.
type ReadTool struct {
	Root      string
	FS        FileReader
	Snapshots hashline.SnapshotStore
	MaxLines  int
}

// NewReadTool arma un ReadTool con el FS de disco por defecto y el limite estandar
// de lineas.
func NewReadTool(root string, snaps hashline.SnapshotStore) *ReadTool {
	return &ReadTool{Root: root, FS: osFS{}, Snapshots: snaps, MaxLines: 2000}
}

func (*ReadTool) Name() string { return "read" }

func (*ReadTool) Description() string {
	return "Lee un archivo y devuelve sus lineas numeradas tras un header [path#HASH]; admite el selector path:N-M para un rango."
}

func (*ReadTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)
}

// Execute parsea el input (path con selector embebido), resuelve la ruta dentro
// de Root (compuerta de sandbox), lee y normaliza, graba el snapshot del archivo
// completo y emite la ventana pedida numerada bajo el header hashline, marcando
// como vistas exactamente las lineas emitidas.
func (rt *ReadTool) Execute(ctx context.Context, input json.RawMessage) (Result, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{}, fmt.Errorf("read: input invalido: %w", err)
	}

	// Selector embebido: si el path lleva ':', se parte en el ULTIMO ':'; el
	// prefijo es la ruta (= display path) y el sufijo el selector. Limitacion v1:
	// no se soportan nombres de archivo con ':'.
	displayPath := in.Path
	hasSel := false
	var fromSel, toSel int // 0 = sin selector
	if i := strings.LastIndex(in.Path, ":"); i >= 0 {
		displayPath = in.Path[:i]
		from, to, err := parseSelector(in.Path[i+1:])
		if err != nil {
			return Result{}, err
		}
		hasSel = true
		fromSel, toSel = from, to
	}

	abs := filepath.Join(rt.Root, displayPath)
	if abs != rt.Root && !strings.HasPrefix(abs, rt.Root+string(filepath.Separator)) {
		return Result{}, fmt.Errorf("read: ruta fuera del workspace: %s", displayPath)
	}

	b, err := rt.FS.ReadFile(abs)
	if err != nil {
		return Result{}, err
	}

	// Binario: un byte NUL -> notice, sin snapshot (no es editable por hashline).
	for _, by := range b {
		if by == 0 {
			notice := "[Cannot read binary file " + displayPath + "; content contains NUL bytes (binary or UTF-16)]"
			return Result{Output: notice}, nil
		}
	}

	// Normaliza: quita el BOM UTF-8 inicial si esta y unifica los saltos de linea.
	norm := strings.TrimPrefix(string(b), "\uFEFF")
	norm = strings.ReplaceAll(norm, "\r\n", "\n")
	norm = strings.ReplaceAll(norm, "\r", "\n")

	// El snapshot guarda SIEMPRE el archivo completo, aun en un read por rango.
	tag := rt.Snapshots.Record(abs, norm)

	lines := hashline.SplitLines(norm)
	total := len(lines)

	// Elige la ventana [from..to] (1-indexed sobre el archivo) y el notice de
	// truncado si la corta el limite de lineas.
	var from, to int
	var truncNotice string
	if hasSel {
		if fromSel > total {
			notice := "Line " + strconv.Itoa(fromSel) + " is beyond end of file (" + strconv.Itoa(total) + " lines total)."
			return Result{Output: notice}, nil
		}
		from, to = fromSel, toSel
		if to > total {
			to = total // el fin que excede el total se clampa en silencio.
		}
	} else {
		from = 1
		to = total
		if rt.MaxLines > 0 && total > rt.MaxLines {
			to = rt.MaxLines
			restantes := total - to
			truncNotice = "\n\n[" + strconv.Itoa(restantes) + " more lines in file. Use :" + strconv.Itoa(to+1) + " to continue]"
		}
	}

	cuerpo := hashline.NumberLines(lines, from, to)
	output := hashline.FormatHeader(displayPath, tag) + "\n" + cuerpo + truncNotice

	seen := make([]int, 0, to-from+1)
	for i := from; i <= to; i++ {
		seen = append(seen, i)
	}
	rt.Snapshots.RecordSeenLines(abs, tag, seen)

	return Result{Output: output}, nil
}

// parseSelector interpreta el sufijo del path: "N" (una linea) o "N-M" (rango),
// enteros con 1 <= N y, en rango, N <= M. Cualquier otra forma es un error de
// tool accionable. Devuelve [from, to] 1-indexed (en "N" from == to).
func parseSelector(sel string) (from, to int, err error) {
	invalid := func() (int, int, error) {
		return 0, 0, fmt.Errorf("read: selector invalido: %s", sel)
	}
	if i := strings.IndexByte(sel, '-'); i >= 0 {
		n, err1 := strconv.Atoi(sel[:i])
		m, err2 := strconv.Atoi(sel[i+1:])
		if err1 != nil || err2 != nil || n < 1 || m < n {
			return invalid()
		}
		return n, m, nil
	}
	n, err1 := strconv.Atoi(sel)
	if err1 != nil || n < 1 {
		return invalid()
	}
	return n, n, nil
}

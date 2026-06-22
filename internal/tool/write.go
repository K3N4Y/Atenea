package tool

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"atenea/internal/tool/hashline"
)

// FileWriter es lo que el write necesita del FS: crear los directorios padre y
// escribir el archivo. El default envuelve os; los tests inyectan un fake en
// memoria para afirmar lo escrito sin tocar disco.
type FileWriter interface {
	MkdirAll(path string, perm os.FileMode) error
	WriteFile(name string, data []byte, perm os.FileMode) error
	Exists(name string) (bool, error)
}

// osWriteFS es el FileWriter por defecto: crea directorios y escribe en disco real.
type osWriteFS struct{}

func (osWriteFS) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }
func (osWriteFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	return os.WriteFile(name, data, perm)
}
func (osWriteFS) Exists(name string) (bool, error) {
	if _, err := os.Stat(name); err == nil {
		return true, nil
	} else if os.IsNotExist(err) {
		return false, nil
	} else {
		return false, err
	}
}

// WriteTool crea un archivo nuevo bajo Root con el contenido completo dado.
// Es la via para archivos NUEVOS: el edit hashline ancla contra un archivo
// existente (lo lee primero), asi que un archivo que no existe se crea con write.
// Comparte el SnapshotStore con read/edit: tras escribir graba el snapshot del
// contenido y marca todas sus lineas como vistas, de modo que el modelo puede
// encadenar un edit sin re-leer.
type WriteTool struct {
	Root             string
	FS               FileWriter
	Snapshots        hashline.SnapshotStore
	SnapshotProvider SnapshotProvider
}

// NewWriteTool arma un WriteTool con el FS de disco por defecto. Recibe el mismo
// Root y SnapshotStore que read/edit para que el snapshot del archivo escrito sea
// el que un edit posterior lee.
func NewWriteTool(root string, snaps hashline.SnapshotStore) *WriteTool {
	return &WriteTool{Root: root, FS: osWriteFS{}, Snapshots: snaps}
}

func NewWriteToolWithSnapshotProvider(root string, provider SnapshotProvider) *WriteTool {
	return &WriteTool{Root: root, FS: osWriteFS{}, SnapshotProvider: provider}
}

func (*WriteTool) Name() string { return "write" }

//go:embed write.txt
var writeDescription string

func (*WriteTool) Description() string { return writeDescription }

func (*WriteTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`)
}

// Execute parsea {path, content}, resuelve la ruta dentro de Root (compuerta de
// sandbox fail-closed, igual que read/edit), normaliza el contenido a LF, crea los
// directorios padre, escribe el archivo y graba el snapshot con todas las lineas
// marcadas como vistas. Devuelve el header [path#HASH] con la ruta RELATIVA (la que
// el modelo encadena en el siguiente edit).
func (wt *WriteTool) Execute(ctx context.Context, input json.RawMessage) (Result, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{}, fmt.Errorf("write: input invalido: %w", err)
	}

	abs, err := sandboxJoin(wt.Root, in.Path, "write")
	if err != nil {
		return Result{}, err
	}
	if _, ok := wt.FS.(osWriteFS); ok {
		if err := rejectRealParentOutside(wt.Root, abs, in.Path, "write"); err != nil {
			return Result{}, err
		}
	}
	exists, err := wt.FS.Exists(abs)
	if err != nil {
		return Result{}, err
	}
	if exists {
		return Result{}, fmt.Errorf("write: el archivo ya existe; usa read+edit: %s", in.Path)
	}

	// Normaliza igual que read/edit: sin BOM inicial y saltos unificados a LF. Asi el
	// hash del snapshot coincide con el que un read posterior computaria.
	norm := strings.TrimPrefix(in.Content, string(rune(0xFEFF)))
	norm = strings.ReplaceAll(norm, "\r\n", "\n")
	norm = strings.ReplaceAll(norm, "\r", "\n")

	if err := wt.FS.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return Result{}, err
	}
	if err := wt.FS.WriteFile(abs, []byte(norm), 0o644); err != nil {
		return Result{}, err
	}

	// Graba el snapshot del archivo recien escrito y marca TODAS sus lineas como
	// vistas: el modelo las autoreo, asi un edit posterior ancla sin re-leer.
	snaps := wt.snapshots(ctx)
	tag := snaps.Record(abs, norm)
	lines := hashline.SplitLines(norm)
	seen := make([]int, 0, len(lines))
	for i := 1; i <= len(lines); i++ {
		seen = append(seen, i)
	}
	snaps.RecordSeenLines(abs, tag, seen)

	return Result{Output: hashline.FormatHeader(in.Path, tag)}, nil
}

func (wt *WriteTool) snapshots(ctx context.Context) hashline.SnapshotStore {
	if wt.SnapshotProvider != nil {
		return wt.SnapshotProvider.Snapshots(ctx)
	}
	return wt.Snapshots
}

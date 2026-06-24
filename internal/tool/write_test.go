package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"atenea/internal/tool/hashline"
)

// fakeWriteFS respalda el FileWriter del write con mapas en memoria: registra cada
// MkdirAll y cada WriteFile por ruta absoluta, sin tocar el disco. Asi el test
// afirma los directorios creados y el contenido escrito.
type fakeWriteFS struct {
	dirs   map[string]bool
	files  map[string]bool
	writes map[string][]byte
}

func newFakeWriteFS() *fakeWriteFS {
	return &fakeWriteFS{dirs: map[string]bool{}, files: map[string]bool{}, writes: map[string][]byte{}}
}

func (f *fakeWriteFS) MkdirAll(path string, perm os.FileMode) error {
	f.dirs[path] = true
	return nil
}

func (f *fakeWriteFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	f.writes[name] = data
	f.files[name] = true
	return nil
}

func (f *fakeWriteFS) Exists(name string) (bool, error) {
	return f.files[name], nil
}

// writeInput serializa {path, content} como lo manda el modelo.
func writeInput(t *testing.T, path, content string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}{Path: path, Content: content})
	if err != nil {
		t.Fatalf("Marshal input: %v", err)
	}
	return b
}

// TestWriteTool_ReturnsAllAdditionsDiff afirma que write devuelve un diff de
// archivo nuevo: hunk "@@ -0,0 +1,N @@", todas las lineas como adicion (+) y sin
// lineas borradas, con la ruta relativa en el header.
func TestWriteTool_ReturnsAllAdditionsDiff(t *testing.T) {
	snaps := hashline.NewMemSnapshotStore()
	fs := newFakeWriteFS()
	wt := &WriteTool{Root: "/work", FS: fs, Snapshots: snaps}

	res, err := wt.Execute(context.Background(), writeInput(t, "n.txt", "x\ny\n"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	for _, want := range []string{"b/n.txt", "@@ -0,0 +1,2 @@", "\n+x\n", "\n+y\n"} {
		if !strings.Contains(res.Diff, want) {
			t.Fatalf("Diff no contiene %q\n--- diff ---\n%s", want, res.Diff)
		}
	}
	if strings.Contains(res.Diff, "\n-") {
		t.Fatalf("archivo nuevo no debe tener lineas borradas\n%s", res.Diff)
	}
}

// TestWriteTool_CreatesNewFileAndReturnsHeader es el happy path: write resuelve la
// ruta relativa "hola.md" bajo Root a la abs "/work/hola.md", escribe el contenido
// y devuelve el header [hola.md#HASH] (ruta relativa) para que el modelo encadene
// un edit sin re-leer. El edit hashline NO puede crear archivos (lee primero y
// falla con "no such file or directory"); write es la via para archivos nuevos.
func TestWriteTool_CreatesNewFileAndReturnsHeader(t *testing.T) {
	snaps := hashline.NewMemSnapshotStore()
	fs := newFakeWriteFS()
	wt := &WriteTool{Root: "/work", FS: fs, Snapshots: snaps}

	res, err := wt.Execute(context.Background(), writeInput(t, "hola.md", "# Hola\nmundo\n"))
	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}

	if !strings.HasPrefix(res.Output, "[hola.md#") {
		t.Fatalf("output = %q, quiero prefijo %q (header con ruta relativa)", res.Output, "[hola.md#")
	}

	written, ok := fs.writes["/work/hola.md"]
	if !ok {
		t.Fatalf("no se escribio /work/hola.md; writes=%v", fs.writes)
	}
	if string(written) != "# Hola\nmundo\n" {
		t.Errorf("contenido escrito = %q, quiero %q", string(written), "# Hola\nmundo\n")
	}
}

// TestWriteTool_RejectsExistingFile afirma que write es solo la via para archivos
// nuevos; modificar existentes debe pasar por read+edit.
func TestWriteTool_RejectsExistingFile(t *testing.T) {
	snaps := hashline.NewMemSnapshotStore()
	fs := newFakeWriteFS()
	fs.files["/work/hola.md"] = true
	wt := &WriteTool{Root: "/work", FS: fs, Snapshots: snaps}

	if _, err := wt.Execute(context.Background(), writeInput(t, "hola.md", "nuevo")); err == nil {
		t.Fatal("Execute: se esperaba error al reemplazar archivo existente")
	}
	if len(fs.writes) != 0 {
		t.Errorf("se escribio aunque el archivo ya existia: %v", fs.writes)
	}
}

// TestWriteTool_RejectsPathOutsideRoot afirma el sandbox fail-closed: una ruta que
// se escapa de Root con ".." se rechaza ANTES de tocar el FS (cero escrituras).
func TestWriteTool_RejectsPathOutsideRoot(t *testing.T) {
	snaps := hashline.NewMemSnapshotStore()
	fs := newFakeWriteFS()
	wt := &WriteTool{Root: "/work", FS: fs, Snapshots: snaps}

	if _, err := wt.Execute(context.Background(), writeInput(t, "../secret.md", "x")); err == nil {
		t.Fatalf("Execute: se esperaba error de ruta fuera del workspace")
	}
	if len(fs.writes) != 0 {
		t.Errorf("se escribio fuera del sandbox: %v", fs.writes)
	}
}

// TestWriteTool_RejectsSymlinkParentOutsideRoot afirma que crear un archivo bajo
// un directorio que es symlink hacia fuera del workspace no esta permitido.
func TestWriteTool_RejectsSymlinkParentOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "out")); err != nil {
		t.Skipf("Symlink no disponible: %v", err)
	}

	wt := NewWriteTool(root, hashline.NewMemSnapshotStore())
	if _, err := wt.Execute(context.Background(), writeInput(t, "out/new.txt", "secret")); err == nil {
		t.Fatal("Execute: se esperaba error por symlink fuera del workspace")
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("outside/new.txt no debio existir; stat err=%v", err)
	}
}

// TestWriteTool_InvalidInputErrors afirma que un input JSON malformado es un error
// de tool accionable, sin escribir nada.
func TestWriteTool_InvalidInputErrors(t *testing.T) {
	snaps := hashline.NewMemSnapshotStore()
	fs := newFakeWriteFS()
	wt := &WriteTool{Root: "/work", FS: fs, Snapshots: snaps}

	if _, err := wt.Execute(context.Background(), json.RawMessage("{")); err == nil {
		t.Fatalf("Execute: se esperaba error de input invalido")
	}
	if len(fs.writes) != 0 {
		t.Errorf("se escribio con input invalido: %v", fs.writes)
	}
}

// TestWriteTool_CreatesParentDirsAndRecordsSeenSnapshot triangula la composicion
// con edit: write crea los directorios padre y graba el snapshot del contenido con
// TODAS sus lineas marcadas como vistas (el modelo las autoreo), de modo que un
// edit posterior puede anclar sus hunks sin que el chequeo "no edites lineas que no
// leiste" lo rechace.
func TestWriteTool_CreatesParentDirsAndRecordsSeenSnapshot(t *testing.T) {
	snaps := hashline.NewMemSnapshotStore()
	fs := newFakeWriteFS()
	wt := &WriteTool{Root: "/work", FS: fs, Snapshots: snaps}

	const abs = "/work/docs/new/foo.md"
	if _, err := wt.Execute(context.Background(), writeInput(t, "docs/new/foo.md", "uno\ndos\n")); err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}

	if !fs.dirs["/work/docs/new"] {
		t.Errorf("no se creo el directorio padre /work/docs/new; dirs=%v", fs.dirs)
	}

	snap := snaps.Head(abs)
	if snap == nil {
		t.Fatalf("no se grabo snapshot para %s", abs)
	}
	if snap.Text != "uno\ndos\n" {
		t.Errorf("snapshot.Text = %q, quiero %q", snap.Text, "uno\ndos\n")
	}
	for _, line := range []int{1, 2} {
		if _, ok := snap.Seen[line]; !ok {
			t.Errorf("linea %d no quedo marcada como vista; Seen=%v", line, snap.Seen)
		}
	}
}

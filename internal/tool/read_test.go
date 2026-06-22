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

// fakeFS es un FileReader respaldado por un mapa de ruta absoluta a bytes, para
// inyectar contenido sin tocar el disco en los tests del read.
type fakeFS map[string][]byte

func (f fakeFS) ReadFile(name string) ([]byte, error) {
	b, ok := f[name]
	if !ok {
		return nil, &fakeFSNotFound{name: name}
	}
	return b, nil
}

type fakeFSNotFound struct{ name string }

func (e *fakeFSNotFound) Error() string { return "fakeFS: no existe " + e.name }

// TestReadTool_WholeFileHasHashHeaderAndNumberedLines afirma el happy path de
// leer un archivo completo: el output es el header "[path#HASH]" seguido de las
// lineas numeradas 1-indexed, con el display path relativo tal como vino.
func TestReadTool_WholeFileHasHashHeaderAndNumberedLines(t *testing.T) {
	contenido := "package main\n\nfunc main(){}\n"
	rt := &ReadTool{
		Root:      "/work",
		FS:        fakeFS{"/work/foo.go": []byte(contenido)},
		Snapshots: hashline.NewMemSnapshotStore(),
		MaxLines:  2000,
	}

	res, err := rt.Execute(context.Background(), json.RawMessage(`{"path":"foo.go"}`))
	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}

	want := "[foo.go#" + hashline.ComputeFileHash(contenido) + "]\n1:package main\n2:\n3:func main(){}"
	if res.Output != want {
		t.Fatalf("Execute: output\n  se esperaba %q\n  se obtuvo  %q", want, res.Output)
	}
}

// TestReadTool_RecordsSnapshotAndSeenLines afirma que Execute graba el snapshot
// del archivo completo y marca como vistas todas las lineas leidas, para que el
// edit pueda anclar contra ellas despues.
func TestReadTool_RecordsSnapshotAndSeenLines(t *testing.T) {
	contenido := "package main\n\nfunc main(){}\n"
	snaps := hashline.NewMemSnapshotStore()
	rt := &ReadTool{
		Root:      "/work",
		FS:        fakeFS{"/work/foo.go": []byte(contenido)},
		Snapshots: snaps,
		MaxLines:  2000,
	}

	if _, err := rt.Execute(context.Background(), json.RawMessage(`{"path":"foo.go"}`)); err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}

	snap := snaps.Head("/work/foo.go")
	if snap == nil {
		t.Fatalf("Head: se esperaba un snapshot grabado, se obtuvo nil")
	}
	if snap.Text != contenido {
		t.Fatalf("Head: Text\n  se esperaba %q\n  se obtuvo  %q", contenido, snap.Text)
	}
	if want := hashline.ComputeFileHash(contenido); snap.Hash != want {
		t.Fatalf("Head: Hash se esperaba %q, se obtuvo %q", want, snap.Hash)
	}
	for _, line := range []int{1, 2, 3} {
		if _, ok := snap.Seen[line]; !ok {
			t.Fatalf("Head: Seen no contiene la linea %d (Seen=%v)", line, snap.Seen)
		}
	}
}

// cinco lineas es el contenido base de los casos borde del selector.
const cincoLineas = "l1\nl2\nl3\nl4\nl5\n"

// TestReadTool_RangeSelectorReadsSubsetButHashesFullFile afirma que ':2-3'
// numera solo las lineas 2 y 3 (con sus numeros reales), pero el header lleva el
// hash del archivo COMPLETO y el snapshot guarda el archivo entero. Seen marca
// exactamente {2,3}: ni la 1 ni la 4.
func TestReadTool_RangeSelectorReadsSubsetButHashesFullFile(t *testing.T) {
	snaps := hashline.NewMemSnapshotStore()
	rt := &ReadTool{
		Root:      "/work",
		FS:        fakeFS{"/work/foo.go": []byte(cincoLineas)},
		Snapshots: snaps,
		MaxLines:  2000,
	}

	res, err := rt.Execute(context.Background(), json.RawMessage(`{"path":"foo.go:2-3"}`))
	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}

	want := "[foo.go#" + hashline.ComputeFileHash(cincoLineas) + "]\n2:l2\n3:l3"
	if res.Output != want {
		t.Fatalf("Execute: output\n  se esperaba %q\n  se obtuvo  %q", want, res.Output)
	}

	snap := snaps.Head("/work/foo.go")
	if snap == nil {
		t.Fatalf("Head: se esperaba un snapshot grabado, se obtuvo nil")
	}
	if snap.Text != cincoLineas {
		t.Fatalf("Head: Text\n  se esperaba %q (archivo completo)\n  se obtuvo  %q", cincoLineas, snap.Text)
	}
	if want := hashline.ComputeFileHash(cincoLineas); snap.Hash != want {
		t.Fatalf("Head: Hash se esperaba %q (archivo completo), se obtuvo %q", want, snap.Hash)
	}
	for _, line := range []int{2, 3} {
		if _, ok := snap.Seen[line]; !ok {
			t.Fatalf("Head: Seen no contiene la linea %d (Seen=%v)", line, snap.Seen)
		}
	}
	for _, line := range []int{1, 4} {
		if _, ok := snap.Seen[line]; ok {
			t.Fatalf("Head: Seen no deberia contener la linea %d (Seen=%v)", line, snap.Seen)
		}
	}
}

// TestReadTool_SingleLineSelector afirma que ':2' emite solo la linea 2 y marca
// exactamente {2} como vista.
func TestReadTool_SingleLineSelector(t *testing.T) {
	snaps := hashline.NewMemSnapshotStore()
	rt := &ReadTool{
		Root:      "/work",
		FS:        fakeFS{"/work/foo.go": []byte(cincoLineas)},
		Snapshots: snaps,
		MaxLines:  2000,
	}

	res, err := rt.Execute(context.Background(), json.RawMessage(`{"path":"foo.go:2"}`))
	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}

	if !strings.HasSuffix(res.Output, "\n2:l2") {
		t.Fatalf("Execute: output\n  se esperaba que terminara en %q\n  se obtuvo  %q", "\n2:l2", res.Output)
	}

	snap := snaps.Head("/work/foo.go")
	if snap == nil {
		t.Fatalf("Head: se esperaba un snapshot grabado, se obtuvo nil")
	}
	if _, ok := snap.Seen[2]; !ok {
		t.Fatalf("Head: Seen no contiene la linea 2 (Seen=%v)", snap.Seen)
	}
	if len(snap.Seen) != 1 {
		t.Fatalf("Head: Seen se esperaba exactamente {2}, se obtuvo %v", snap.Seen)
	}
}

// TestReadTool_TruncatesAtLineLimitWithContinuationNotice afirma que con
// MaxLines=2 sobre el archivo de 5 lineas se emiten 1 y 2, mas el notice de
// continuacion in-band, y Seen cubre solo lo emitido (1 y 2, no 3).
func TestReadTool_TruncatesAtLineLimitWithContinuationNotice(t *testing.T) {
	snaps := hashline.NewMemSnapshotStore()
	rt := &ReadTool{
		Root:      "/work",
		FS:        fakeFS{"/work/foo.go": []byte(cincoLineas)},
		Snapshots: snaps,
		MaxLines:  2,
	}

	res, err := rt.Execute(context.Background(), json.RawMessage(`{"path":"foo.go"}`))
	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}

	if !strings.Contains(res.Output, "1:l1") || !strings.Contains(res.Output, "2:l2") {
		t.Fatalf("Execute: output no contiene las primeras 2 lineas: %q", res.Output)
	}
	if strings.Contains(res.Output, "3:l3") {
		t.Fatalf("Execute: output no deberia contener la linea 3 (truncada): %q", res.Output)
	}
	if notice := "[3 more lines in file. Use :3 to continue]"; !strings.Contains(res.Output, notice) {
		t.Fatalf("Execute: output\n  se esperaba que contuviera %q\n  se obtuvo  %q", notice, res.Output)
	}

	snap := snaps.Head("/work/foo.go")
	if snap == nil {
		t.Fatalf("Head: se esperaba un snapshot grabado, se obtuvo nil")
	}
	for _, line := range []int{1, 2} {
		if _, ok := snap.Seen[line]; !ok {
			t.Fatalf("Head: Seen no contiene la linea %d (Seen=%v)", line, snap.Seen)
		}
	}
	if _, ok := snap.Seen[3]; ok {
		t.Fatalf("Head: Seen no deberia contener la linea 3 (Seen=%v)", snap.Seen)
	}
}

// TestReadTool_ByteLimitMarksOnlyEmittedLines afirma que el limite propio del
// read corta en limites de linea y solo marca Seen para lo que el modelo recibe,
// evitando depender del truncado generico del OutputStore.
func TestReadTool_ByteLimitMarksOnlyEmittedLines(t *testing.T) {
	snaps := hashline.NewMemSnapshotStore()
	rt := &ReadTool{
		Root:      "/work",
		FS:        fakeFS{"/work/foo.go": []byte("11111111111111111111\n22222222222222222222\n33333333333333333333\n")},
		Snapshots: snaps,
		MaxLines:  2000,
		MaxBytes:  55,
	}

	res, err := rt.Execute(context.Background(), json.RawMessage(`{"path":"foo.go"}`))
	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}
	if !strings.Contains(res.Output, "1:11111111111111111111") {
		t.Fatalf("Execute: output no contiene la linea 1: %q", res.Output)
	}
	if strings.Contains(res.Output, "2:22222222222222222222") {
		t.Fatalf("Execute: output no deberia contener la linea 2 por limite de bytes: %q", res.Output)
	}

	snap := snaps.Head("/work/foo.go")
	if snap == nil {
		t.Fatal("Head: se esperaba snapshot grabado")
	}
	if _, ok := snap.Seen[1]; !ok {
		t.Fatalf("Head: Seen no contiene linea emitida 1: %v", snap.Seen)
	}
	if _, ok := snap.Seen[2]; ok {
		t.Fatalf("Head: Seen no deberia contener linea no emitida 2: %v", snap.Seen)
	}
}

// TestReadTool_OutOfRangeSelectorReportsBeyondEOF afirma que un rango cuyo start
// excede el total devuelve el notice beyond-EOF, con el snapshot YA grabado y sin
// ninguna linea marcada como vista.
func TestReadTool_OutOfRangeSelectorReportsBeyondEOF(t *testing.T) {
	snaps := hashline.NewMemSnapshotStore()
	rt := &ReadTool{
		Root:      "/work",
		FS:        fakeFS{"/work/foo.go": []byte(cincoLineas)},
		Snapshots: snaps,
		MaxLines:  2000,
	}

	res, err := rt.Execute(context.Background(), json.RawMessage(`{"path":"foo.go:100-200"}`))
	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}

	if notice := "Line 100 is beyond end of file (5 lines total)."; !strings.Contains(res.Output, notice) {
		t.Fatalf("Execute: output\n  se esperaba que contuviera %q\n  se obtuvo  %q", notice, res.Output)
	}

	snap := snaps.Head("/work/foo.go")
	if snap == nil {
		t.Fatalf("Head: se esperaba un snapshot grabado (el archivo existe), se obtuvo nil")
	}
	if len(snap.Seen) != 0 {
		t.Fatalf("Head: Seen se esperaba vacio en beyond-EOF, se obtuvo %v", snap.Seen)
	}
}

// TestReadTool_InvalidSelectorErrors afirma que un sufijo con forma de selector
// pero invalido devuelve error de tool (sin panico) y no produce snapshot.
func TestReadTool_InvalidSelectorErrors(t *testing.T) {
	for _, sel := range []string{"0", "5-2", "abc"} {
		t.Run(sel, func(t *testing.T) {
			snaps := hashline.NewMemSnapshotStore()
			rt := &ReadTool{
				Root:      "/work",
				FS:        fakeFS{"/work/foo.go": []byte(cincoLineas)},
				Snapshots: snaps,
				MaxLines:  2000,
			}

			input := json.RawMessage(`{"path":"foo.go:` + sel + `"}`)
			if _, err := rt.Execute(context.Background(), input); err == nil {
				t.Fatalf("Execute(:%s): se esperaba error, se obtuvo nil", sel)
			}
		})
	}
}

// TestReadTool_MissingFileReturnsToolError afirma que leer un archivo inexistente
// propaga el error del FS.
func TestReadTool_MissingFileReturnsToolError(t *testing.T) {
	rt := &ReadTool{
		Root:      "/work",
		FS:        fakeFS{},
		Snapshots: hashline.NewMemSnapshotStore(),
		MaxLines:  2000,
	}

	if _, err := rt.Execute(context.Background(), json.RawMessage(`{"path":"foo.go"}`)); err == nil {
		t.Fatalf("Execute: se esperaba error por archivo inexistente, se obtuvo nil")
	}
}

// TestReadTool_BinaryFileReturnsNotice afirma que un archivo con un byte NUL
// devuelve el notice de binario y NO graba snapshot.
func TestReadTool_BinaryFileReturnsNotice(t *testing.T) {
	snaps := hashline.NewMemSnapshotStore()
	rt := &ReadTool{
		Root:      "/work",
		FS:        fakeFS{"/work/foo.bin": []byte("ab\x00cd")},
		Snapshots: snaps,
		MaxLines:  2000,
	}

	res, err := rt.Execute(context.Background(), json.RawMessage(`{"path":"foo.bin"}`))
	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}
	if !strings.Contains(res.Output, "Cannot read binary file") {
		t.Fatalf("Execute: output\n  se esperaba que contuviera %q\n  se obtuvo  %q", "Cannot read binary file", res.Output)
	}
	if snap := snaps.Head("/work/foo.bin"); snap != nil {
		t.Fatalf("Head: no deberia haber snapshot de un binario, se obtuvo %+v", snap)
	}
}

// spyFS cuenta las llamadas a ReadFile para afirmar que la compuerta de sandbox
// rechaza rutas fuera de root SIN tocar el FS.
type spyFS struct{ reads int }

func (s *spyFS) ReadFile(name string) ([]byte, error) {
	s.reads++
	return nil, &fakeFSNotFound{name: name}
}

// TestReadTool_RejectsPathOutsideRoot afirma que una ruta que escapa de Root via
// '..' devuelve error sin llamar a ReadFile (0 lecturas en el spy).
func TestReadTool_RejectsPathOutsideRoot(t *testing.T) {
	fs := &spyFS{}
	rt := &ReadTool{
		Root:      "/work",
		FS:        fs,
		Snapshots: hashline.NewMemSnapshotStore(),
		MaxLines:  2000,
	}

	if _, err := rt.Execute(context.Background(), json.RawMessage(`{"path":"../secret.go"}`)); err == nil {
		t.Fatalf("Execute: se esperaba error por ruta fuera del workspace, se obtuvo nil")
	}
	if fs.reads != 0 {
		t.Fatalf("ReadFile: se esperaban 0 lecturas (rechazo antes de leer), se obtuvieron %d", fs.reads)
	}
}

// TestReadTool_RejectsSymlinkOutsideRoot afirma que el sandbox no solo limpia "..":
// tambien rechaza un symlink dentro del workspace que resuelve fuera.
func TestReadTool_RejectsSymlinkOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("WriteFile target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("Symlink no disponible: %v", err)
	}

	rt := NewReadTool(root, hashline.NewMemSnapshotStore())
	if _, err := rt.Execute(context.Background(), json.RawMessage(`{"path":"link.txt"}`)); err == nil {
		t.Fatal("Execute: se esperaba error por symlink fuera del workspace")
	}
}

// TestReadTool_InvalidInputErrors afirma que un JSON de input malformado devuelve
// error en vez de paniquear.
func TestReadTool_InvalidInputErrors(t *testing.T) {
	rt := &ReadTool{
		Root:      "/work",
		FS:        fakeFS{},
		Snapshots: hashline.NewMemSnapshotStore(),
		MaxLines:  2000,
	}

	if _, err := rt.Execute(context.Background(), json.RawMessage("{")); err == nil {
		t.Fatalf("Execute: se esperaba error por input invalido, se obtuvo nil")
	}
}

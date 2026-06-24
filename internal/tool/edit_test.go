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

// fakeEditFS es un hashline.Filesystem respaldado por mapas en memoria: lee de
// files y registra cada escritura en writes, sin tocar el disco. Asi el test del
// edit afirma tanto lo leido como lo escrito por ruta absoluta.
type fakeEditFS struct {
	files  map[string][]byte
	writes map[string][]byte
}

func (f *fakeEditFS) ReadFile(name string) ([]byte, error) {
	b, ok := f.files[name]
	if !ok {
		return nil, &fakeFSNotFound{name: name}
	}
	return b, nil
}

func (f *fakeEditFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	f.writes[name] = data
	return nil
}

// TestEditTool_AppliesPatchReturnsNewHeader afirma el happy path del edit: el
// patch referencia la ruta RELATIVA "foo.go" bajo Root, el Patcher resuelve a la
// abs "/work/foo.go" para leer/escribir/snapshotear, aplica el SWAP y el output
// devuelve el header con la ruta RELATIVA y el hash del archivo recien escrito.
func TestEditTool_AppliesPatchReturnsNewHeader(t *testing.T) {
	const abs = "/work/foo.go"
	original := "a\nb\nc\nd\n"

	snaps := hashline.NewMemSnapshotStore()
	h := snaps.Record(abs, original)
	snaps.RecordSeenLines(abs, h, []int{2, 3})

	fs := &fakeEditFS{
		files:  map[string][]byte{abs: []byte(original)},
		writes: map[string][]byte{},
	}

	et := NewEditTool("/work", fs, snaps)

	input, err := json.Marshal(struct {
		Patch string `json:"patch"`
	}{Patch: "[foo.go#" + h + "]\nSWAP 2.=3:\n+X"})
	if err != nil {
		t.Fatalf("Marshal: error inesperado: %v", err)
	}

	res, err := et.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}

	if !strings.HasPrefix(res.Output, "[foo.go#") {
		t.Fatalf("Execute: output\n  se esperaba prefijo %q (ruta relativa)\n  se obtuvo  %q", "[foo.go#", res.Output)
	}

	written, ok := fs.writes[abs]
	if !ok {
		t.Fatalf("WriteFile: se esperaba una escritura a %q, writes=%v", abs, fs.writes)
	}

	gotLines := hashline.SplitLines(string(written))
	wantLines := []string{"a", "X", "d"}
	if len(gotLines) != len(wantLines) {
		t.Fatalf("escrito: lineas\n  se esperaba %v\n  se obtuvo  %v", wantLines, gotLines)
	}
	for i := range wantLines {
		if gotLines[i] != wantLines[i] {
			t.Fatalf("escrito: lineas\n  se esperaba %v\n  se obtuvo  %v", wantLines, gotLines)
		}
	}

	wantHeaderPrefix := "[foo.go#" + hashline.ComputeFileHash(string(written)) + "]"
	if !strings.HasPrefix(res.Output, wantHeaderPrefix) {
		t.Fatalf("Execute: output\n  se esperaba prefijo %q (header consistente con lo escrito)\n  se obtuvo  %q", wantHeaderPrefix, res.Output)
	}

	head := snaps.Head(abs)
	if head == nil {
		t.Fatalf("Head: se esperaba un snapshot regrabado en %q, se obtuvo nil", abs)
	}
	if want := hashline.ComputeFileHash(string(written)); head.Hash != want {
		t.Fatalf("Head: Hash\n  se esperaba %q (hash del archivo escrito)\n  se obtuvo  %q", want, head.Hash)
	}
}

// TestEditTool_ReturnsDiff afirma que Execute devuelve un diff unificado con la
// linea cambiada (-b / +X) usando la ruta RELATIVA en el header; la ruta ABSOLUTA
// nunca debe filtrarse al diff (la construye con relPath, no por reemplazo).
func TestEditTool_ReturnsDiff(t *testing.T) {
	const abs = "/work/foo.go"
	original := "a\nb\nc\nd\n"

	snaps := hashline.NewMemSnapshotStore()
	h := snaps.Record(abs, original)
	snaps.RecordSeenLines(abs, h, []int{2, 3})

	fs := &fakeEditFS{
		files:  map[string][]byte{abs: []byte(original)},
		writes: map[string][]byte{},
	}
	et := NewEditTool("/work", fs, snaps)

	input, _ := json.Marshal(struct {
		Patch string `json:"patch"`
	}{Patch: "[foo.go#" + h + "]\nSWAP 2.=3:\n+X"})

	res, err := et.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	for _, want := range []string{"a/foo.go", "b/foo.go", "\n-b\n", "\n+X\n"} {
		if !strings.Contains(res.Diff, want) {
			t.Fatalf("Diff no contiene %q\n--- diff ---\n%s", want, res.Diff)
		}
	}
	if strings.Contains(res.Diff, abs) {
		t.Fatalf("Diff filtro la ruta absoluta %q\n%s", abs, res.Diff)
	}
}

// TestEditTool_MissingTagErrors afirma que un patch sin header [ruta#HASH] hace que
// Execute propague el error (MissingTagError) en vez de inventar una ruta.
func TestEditTool_MissingTagErrors(t *testing.T) {
	snaps := hashline.NewMemSnapshotStore()
	fs := &fakeEditFS{
		files:  map[string][]byte{},
		writes: map[string][]byte{},
	}
	et := NewEditTool("/work", fs, snaps)

	input, err := json.Marshal(struct {
		Patch string `json:"patch"`
	}{Patch: "SWAP 1.=1:\n+x"})
	if err != nil {
		t.Fatalf("Marshal: error inesperado: %v", err)
	}

	if _, err := et.Execute(context.Background(), input); err == nil {
		t.Fatalf("Execute: se esperaba error por header faltante, se obtuvo nil")
	}
}

// TestEditTool_RejectsPathOutsideRoot afirma que la compuerta de sandbox rechaza un
// header que apunta fuera de Root (../secret.go) y no escribe nada.
func TestEditTool_RejectsPathOutsideRoot(t *testing.T) {
	snaps := hashline.NewMemSnapshotStore()
	fs := &fakeEditFS{
		files:  map[string][]byte{},
		writes: map[string][]byte{},
	}
	et := NewEditTool("/work", fs, snaps)

	input, err := json.Marshal(struct {
		Patch string `json:"patch"`
	}{Patch: "[../secret.go#XXXX]\nSWAP 1.=1:\n+x"})
	if err != nil {
		t.Fatalf("Marshal: error inesperado: %v", err)
	}

	if _, err := et.Execute(context.Background(), input); err == nil {
		t.Fatalf("Execute: se esperaba error por ruta fuera del root, se obtuvo nil")
	}
	if len(fs.writes) != 0 {
		t.Fatalf("Execute: no debio escribir fuera del root, writes=%v", fs.writes)
	}
}

// TestEditTool_RejectsSymlinkOutsideRoot afirma que edit no escribe a traves de un
// symlink dentro del workspace que apunta fuera.
func TestEditTool_RejectsSymlinkOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.go")
	original := "a\nb\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatalf("WriteFile target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link.go")); err != nil {
		t.Skipf("Symlink no disponible: %v", err)
	}

	h := hashline.ComputeFileHash(original)
	input, err := json.Marshal(struct {
		Patch string `json:"patch"`
	}{Patch: "[link.go#" + h + "]\nSWAP 2.=2:\n+X"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	et := NewEditTool(root, hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
	if _, err := et.Execute(context.Background(), input); err == nil {
		t.Fatal("Execute: se esperaba error por symlink fuera del workspace")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile target: %v", err)
	}
	if string(got) != original {
		t.Fatalf("target fuera del workspace cambio: %q", string(got))
	}
}

// TestEditTool_SnapshotsAreIsolatedBySession afirma que un header visto en una
// sesion no habilita editar desde otra sesion.
func TestEditTool_SnapshotsAreIsolatedBySession(t *testing.T) {
	const abs = "/work/foo.go"
	original := "a\nb\n"

	sessions := NewSessionSnapshots()
	rt := &ReadTool{
		Root:             "/work",
		FS:               fakeFS{abs: []byte(original)},
		SnapshotProvider: sessions,
		MaxLines:         2000,
	}
	fs := &fakeEditFS{
		files:  map[string][]byte{abs: []byte(original)},
		writes: map[string][]byte{},
	}
	et := NewEditToolWithSnapshotProvider("/work", fs, sessions)

	res, err := rt.Execute(WithSessionID(context.Background(), "s1"), json.RawMessage(`{"path":"foo.go"}`))
	if err != nil {
		t.Fatalf("read s1: %v", err)
	}
	header := strings.Split(res.Output, "\n")[0]
	input, err := json.Marshal(struct {
		Patch string `json:"patch"`
	}{Patch: header + "\nSWAP 2.=2:\n+X"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := et.Execute(WithSessionID(context.Background(), "s2"), input); err == nil {
		t.Fatal("edit s2: se esperaba error por snapshot de otra sesion")
	}
	if len(fs.writes) != 0 {
		t.Fatalf("edit s2 no debio escribir: %v", fs.writes)
	}
}

// TestEditTool_InvalidInputErrors afirma que un JSON de entrada invalido hace que
// Execute devuelva error en vez de entrar al Patcher.
func TestEditTool_InvalidInputErrors(t *testing.T) {
	snaps := hashline.NewMemSnapshotStore()
	fs := &fakeEditFS{
		files:  map[string][]byte{},
		writes: map[string][]byte{},
	}
	et := NewEditTool("/work", fs, snaps)

	if _, err := et.Execute(context.Background(), json.RawMessage("{")); err == nil {
		t.Fatalf("Execute: se esperaba error por input invalido, se obtuvo nil")
	}
}

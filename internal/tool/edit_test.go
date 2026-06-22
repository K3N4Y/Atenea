package tool

import (
	"context"
	"encoding/json"
	"os"
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

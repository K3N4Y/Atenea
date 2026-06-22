package hashline

import (
	"errors"
	"fmt"
	"os"
	"testing"
)

// fakePatchFS es un Filesystem en memoria para el test: ReadFile lee de files y
// WriteFile guarda en writes, sin tocar el disco.
type fakePatchFS struct {
	files  map[string][]byte
	writes map[string][]byte
}

func (f *fakePatchFS) ReadFile(name string) ([]byte, error) {
	data, ok := f.files[name]
	if !ok {
		return nil, fmt.Errorf("fakePatchFS: archivo inexistente %q", name)
	}
	return data, nil
}

func (f *fakePatchFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	f.writes[name] = data
	return nil
}

// TestPatcher_NoDriftAppliesAndRecordsNewHash afirma el happy path sin drift: si
// el hash del archivo vivo coincide con el esperado, el Patcher aplica los edits,
// escribe el archivo, regraba el snapshot con el hash nuevo y devuelve un header
// consistente con lo que escribio (para poder encadenar edits).
func TestPatcher_NoDriftAppliesAndRecordsNewHash(t *testing.T) {
	const path = "/work/foo.go"
	original := "a\nb\nc\nd\n"

	snaps := NewMemSnapshotStore()
	hash := snaps.Record(path, original)
	snaps.RecordSeenLines(path, hash, []int{2, 3})

	fs := &fakePatchFS{
		files:  map[string][]byte{path: []byte(original)},
		writes: map[string][]byte{},
	}
	patch := Patch{Sections: []Section{{
		Path:  path,
		Hash:  hash,
		Edits: []Edit{{Kind: Replace, Range: Range{Start: 2, End: 3}, Text: "X"}},
	}}}

	p := NewPatcher(fs, snaps)

	res, err := p.Apply(patch)
	if err != nil {
		t.Fatalf("Apply: no se esperaba error, se obtuvo %v", err)
	}

	written, ok := fs.writes[path]
	if !ok {
		t.Fatalf("Apply: se esperaba una escritura a %q, no la hubo", path)
	}

	gotLines := SplitLines(string(written))
	wantLines := []string{"a", "X", "d"}
	if len(gotLines) != len(wantLines) {
		t.Fatalf("contenido escrito: se esperaba %v, se obtuvo %v", wantLines, gotLines)
	}
	for i := range wantLines {
		if gotLines[i] != wantLines[i] {
			t.Fatalf("contenido escrito: se esperaba %v, se obtuvo %v", wantLines, gotLines)
		}
	}

	wantHeader := FormatHeader(path, ComputeFileHash(string(written)))
	if res.Header != wantHeader {
		t.Fatalf("PatchResult.Header: se esperaba %q, se obtuvo %q", wantHeader, res.Header)
	}

	newHash := ComputeFileHash(string(written))
	if head := snaps.Head(path); head == nil || head.Hash != newHash {
		t.Fatalf("snapshot regrabado: se esperaba Head con hash %q, se obtuvo %+v", newHash, head)
	}
}

// TestPatcher_DriftWithAnchorReturnsMismatch afirma que si el archivo vivo cambio
// desde el read (drift) y el edit ancla a una linea (SWAP), el Patcher devuelve un
// *MismatchError reconocido (el hash stale si esta en la sesion) y NO escribe: un
// edit anclado no puede aplicarse a salvo sobre un archivo movido.
func TestPatcher_DriftWithAnchorReturnsMismatch(t *testing.T) {
	const path = "/work/foo.go"
	original := "a\nb\nc\nd\n"
	live := "a\nZZZ\nc\nd\n"

	snaps := NewMemSnapshotStore()
	staleHash := snaps.Record(path, original)
	snaps.RecordSeenLines(path, staleHash, []int{2})

	fs := &fakePatchFS{
		files:  map[string][]byte{path: []byte(live)},
		writes: map[string][]byte{},
	}
	patch := Patch{Sections: []Section{{
		Path:  path,
		Hash:  staleHash,
		Edits: []Edit{{Kind: Replace, Range: Range{Start: 2, End: 2}, Text: "X"}},
	}}}

	p := NewPatcher(fs, snaps)

	_, err := p.Apply(patch)
	if err == nil {
		t.Fatalf("Apply: se esperaba un *MismatchError por drift, no hubo error")
	}
	var me *MismatchError
	if !errors.As(err, &me) {
		t.Fatalf("Apply: se esperaba un *MismatchError, se obtuvo %T: %v", err, err)
	}
	if !me.Recognized {
		t.Fatalf("MismatchError: se esperaba Recognized=true (el hash stale esta en la sesion), se obtuvo false")
	}
	if len(fs.writes) != 0 {
		t.Fatalf("Apply: no debio escribir nada por drift, writes=%v", fs.writes)
	}
}

// TestPatcher_HeadTailOnStaleTagAppliesWithWarning afirma que un INS.TAIL (posicion
// estable, fin de archivo) se aplica igual aunque el hash sea stale, porque no
// depende de los numeros de linea del read: agrega un warning y SI escribe.
func TestPatcher_HeadTailOnStaleTagAppliesWithWarning(t *testing.T) {
	const path = "/work/foo.go"
	live := "a\nb\n"
	staleHash := ComputeFileHash("OTRA COSA\n")

	snaps := NewMemSnapshotStore()

	fs := &fakePatchFS{
		files:  map[string][]byte{path: []byte(live)},
		writes: map[string][]byte{},
	}
	patch := Patch{Sections: []Section{{
		Path:  path,
		Hash:  staleHash,
		Edits: []Edit{{Kind: Insert, Cursor: EOF, Text: "appended"}},
	}}}

	p := NewPatcher(fs, snaps)

	res, err := p.Apply(patch)
	if err != nil {
		t.Fatalf("Apply: no se esperaba error para INS.TAIL stale, se obtuvo %v", err)
	}
	if len(res.Warnings) == 0 {
		t.Fatalf("Apply: se esperaba al menos un warning por hash stable+stale, no hubo")
	}
	written, ok := fs.writes[path]
	if !ok {
		t.Fatalf("Apply: se esperaba una escritura a %q, no la hubo (writes=%v)", path, fs.writes)
	}
	got := SplitLines(string(written))
	if len(got) == 0 || got[len(got)-1] != "appended" {
		t.Fatalf("Apply: se esperaba que el contenido terminara en \"appended\", se obtuvo %v", got)
	}
}

// TestPatcher_EditUnseenLineRejected afirma que sin drift, si el snapshot conocido
// no marca como vista una linea anclada por el edit, el Patcher rechaza el cambio y
// NO escribe: no se editan lineas que el read no mostro.
func TestPatcher_EditUnseenLineRejected(t *testing.T) {
	const path = "/work/foo.go"
	original := "a\nb\nc\nd\n"

	snaps := NewMemSnapshotStore()
	h := snaps.Record(path, original)
	snaps.RecordSeenLines(path, h, []int{1, 2})

	fs := &fakePatchFS{
		files:  map[string][]byte{path: []byte(original)},
		writes: map[string][]byte{},
	}
	patch := Patch{Sections: []Section{{
		Path:  path,
		Hash:  h,
		Edits: []Edit{{Kind: Replace, Range: Range{Start: 4, End: 4}, Text: "X"}},
	}}}

	p := NewPatcher(fs, snaps)

	_, err := p.Apply(patch)
	if err == nil {
		t.Fatalf("Apply: se esperaba error por editar una linea no vista (4), no hubo error")
	}
	if len(fs.writes) != 0 {
		t.Fatalf("Apply: no debio escribir nada al rechazar una linea no vista, writes=%v", fs.writes)
	}
}

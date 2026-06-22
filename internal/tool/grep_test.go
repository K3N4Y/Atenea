package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"atenea/internal/tool/hashline"
)

type fakeSearcher struct {
	req    GrepRequest
	result GrepResult
	err    error
	calls  int
}

func (s *fakeSearcher) Grep(ctx context.Context, req GrepRequest) (GrepResult, error) {
	s.calls++
	s.req = req
	return s.result, s.err
}

// TestGrepTool_SearchesPatternAndFormatsHashlineGroups afirma el happy path de
// grep: busca con pattern/path/include, agrupa por archivo y renderiza cada grupo
// como hashline para que el edit pueda anclar despues.
func TestGrepTool_SearchesPatternAndFormatsHashlineGroups(t *testing.T) {
	readText := "package tool\n\nfunc (*ReadTool) Execute() {}\nconst displayPath = \"x\"\n"
	writeText := "package tool\nfunc (*WriteTool) Execute() {}\n"
	searcher := &fakeSearcher{result: GrepResult{Matches: []GrepMatch{
		{Path: "internal/tool/read.go", Line: 3, Text: "ignored"},
		{Path: "internal/tool/read.go", Line: 4, Text: "ignored"},
		{Path: "internal/tool/write.go", Line: 2, Text: "ignored"},
	}}}
	gt := &GrepTool{
		Root:       "/work",
		Searcher:   searcher,
		FS:         fakeFS{"/work/internal/tool/read.go": []byte(readText), "/work/internal/tool/write.go": []byte(writeText)},
		Snapshots:  hashline.NewMemSnapshotStore(),
		MaxMatches: 100,
	}

	res, err := gt.Execute(context.Background(), json.RawMessage(`{"pattern":"Execute","path":"internal/tool","include":"*.go"}`))
	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}

	want := strings.Join([]string{
		"Found 3 matches",
		"[internal/tool/read.go#" + hashline.ComputeFileHash(readText) + "]",
		"3:func (*ReadTool) Execute() {}",
		"4:const displayPath = \"x\"",
		"",
		"[internal/tool/write.go#" + hashline.ComputeFileHash(writeText) + "]",
		"2:func (*WriteTool) Execute() {}",
	}, "\n")
	if res.Output != want {
		t.Fatalf("Execute: output\n  se esperaba %q\n  se obtuvo  %q", want, res.Output)
	}
	if searcher.calls != 1 {
		t.Fatalf("Searcher calls = %d, quiero 1", searcher.calls)
	}
	wantReq := GrepRequest{Root: "/work", Path: "internal/tool", Pattern: "Execute", Include: "*.go", Limit: 100}
	if !reflect.DeepEqual(searcher.req, wantReq) {
		t.Fatalf("Searcher req\n  se esperaba %+v\n  se obtuvo  %+v", wantReq, searcher.req)
	}
}

// TestGrepTool_RecordsSnapshotsAndSeenLines afirma que grep graba el snapshot
// completo de cada archivo con matches y marca solo las lineas emitidas como
// vistas. Editar alrededor del match requiere un read explicito.
func TestGrepTool_RecordsSnapshotsAndSeenLines(t *testing.T) {
	readText := "package tool\n\nfunc (*ReadTool) Execute() {}\nconst displayPath = \"x\"\n"
	writeText := "package tool\nfunc (*WriteTool) Execute() {}\n"
	snaps := hashline.NewMemSnapshotStore()
	gt := &GrepTool{
		Root: "/work",
		Searcher: &fakeSearcher{result: GrepResult{Matches: []GrepMatch{
			{Path: "internal/tool/read.go", Line: 3, Text: "ignored"},
			{Path: "internal/tool/read.go", Line: 4, Text: "ignored"},
			{Path: "internal/tool/write.go", Line: 2, Text: "ignored"},
		}}},
		FS:         fakeFS{"/work/internal/tool/read.go": []byte(readText), "/work/internal/tool/write.go": []byte(writeText)},
		Snapshots:  snaps,
		MaxMatches: 100,
	}

	if _, err := gt.Execute(context.Background(), json.RawMessage(`{"pattern":"Execute"}`)); err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}

	readSnap := snaps.Head("/work/internal/tool/read.go")
	if readSnap == nil {
		t.Fatalf("Head(read): se esperaba snapshot grabado")
	}
	if readSnap.Text != readText {
		t.Fatalf("Head(read).Text\n  se esperaba %q\n  se obtuvo  %q", readText, readSnap.Text)
	}
	if !sameSeenLines(readSnap.Seen, []int{3, 4}) {
		t.Fatalf("Head(read).Seen = %v, quiero {3,4}", readSnap.Seen)
	}

	writeSnap := snaps.Head("/work/internal/tool/write.go")
	if writeSnap == nil {
		t.Fatalf("Head(write): se esperaba snapshot grabado")
	}
	if writeSnap.Text != writeText {
		t.Fatalf("Head(write).Text\n  se esperaba %q\n  se obtuvo  %q", writeText, writeSnap.Text)
	}
	if !sameSeenLines(writeSnap.Seen, []int{2}) {
		t.Fatalf("Head(write).Seen = %v, quiero {2}", writeSnap.Seen)
	}
}

func sameSeenLines(got map[int]struct{}, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for _, line := range want {
		if _, ok := got[line]; !ok {
			return false
		}
	}
	return true
}

func TestGrepTool_DefaultPathAndIncludePassedToSearcher(t *testing.T) {
	searcher := &fakeSearcher{}
	gt := &GrepTool{
		Root:      "/work",
		Searcher:  searcher,
		FS:        fakeFS{},
		Snapshots: hashline.NewMemSnapshotStore(),
	}

	res, err := gt.Execute(context.Background(), json.RawMessage(`{"pattern":"Execute","include":"*.go"}`))
	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}
	if res.Output != "No files found" {
		t.Fatalf("Execute output = %q, quiero No files found", res.Output)
	}
	wantReq := GrepRequest{Root: "/work", Path: ".", Pattern: "Execute", Include: "*.go", Limit: 100}
	if !reflect.DeepEqual(searcher.req, wantReq) {
		t.Fatalf("Searcher req\n  se esperaba %+v\n  se obtuvo  %+v", wantReq, searcher.req)
	}
}

func TestGrepTool_TruncationNotice(t *testing.T) {
	text := "package tool\nfunc one() {}\n"
	gt := &GrepTool{
		Root: "/work",
		Searcher: &fakeSearcher{result: GrepResult{
			Matches:   []GrepMatch{{Path: "a.go", Line: 2, Text: "ignored"}},
			Truncated: true,
		}},
		FS:         fakeFS{"/work/a.go": []byte(text)},
		Snapshots:  hashline.NewMemSnapshotStore(),
		MaxMatches: 1,
	}

	res, err := gt.Execute(context.Background(), json.RawMessage(`{"pattern":"func"}`))
	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}
	if !strings.HasPrefix(res.Output, "Found 1 matches (more matches available)\n") {
		t.Fatalf("Execute output no tiene header de truncado: %q", res.Output)
	}
	if !strings.Contains(res.Output, "(Results truncated. Consider using a more specific path or pattern.)") {
		t.Fatalf("Execute output no contiene notice final de truncado: %q", res.Output)
	}
}

func TestGrepTool_DedupesSameLine(t *testing.T) {
	text := "package tool\nfunc one() {}\n"
	snaps := hashline.NewMemSnapshotStore()
	gt := &GrepTool{
		Root: "/work",
		Searcher: &fakeSearcher{result: GrepResult{Matches: []GrepMatch{
			{Path: "a.go", Line: 2, Text: "ignored"},
			{Path: "a.go", Line: 2, Text: "ignored again"},
		}}},
		FS:         fakeFS{"/work/a.go": []byte(text)},
		Snapshots:  snaps,
		MaxMatches: 100,
	}

	res, err := gt.Execute(context.Background(), json.RawMessage(`{"pattern":"func"}`))
	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}
	if got := strings.Count(res.Output, "2:func one() {}"); got != 1 {
		t.Fatalf("Execute output contiene la linea match %d veces, quiero 1: %q", got, res.Output)
	}
	snap := snaps.Head("/work/a.go")
	if snap == nil {
		t.Fatalf("Head: se esperaba snapshot")
	}
	if !sameSeenLines(snap.Seen, []int{2}) {
		t.Fatalf("Seen = %v, quiero {2}", snap.Seen)
	}
}

func TestGrepTool_RejectsPathOutsideRoot(t *testing.T) {
	searcher := &fakeSearcher{}
	gt := &GrepTool{Root: "/work", Searcher: searcher, FS: fakeFS{}, Snapshots: hashline.NewMemSnapshotStore()}

	_, err := gt.Execute(context.Background(), json.RawMessage(`{"pattern":"x","path":"../../etc/passwd"}`))
	if err == nil {
		t.Fatalf("Execute: se esperaba error por ruta fuera del workspace")
	}
	if searcher.calls != 0 {
		t.Fatalf("Searcher calls = %d, quiero 0", searcher.calls)
	}
}

func TestGrepTool_InvalidInputErrors(t *testing.T) {
	gt := &GrepTool{Root: "/work", Searcher: &fakeSearcher{}, FS: fakeFS{}, Snapshots: hashline.NewMemSnapshotStore()}

	_, err := gt.Execute(context.Background(), json.RawMessage(`{`))
	if err == nil {
		t.Fatalf("Execute: se esperaba error por JSON invalido")
	}
}

func TestGrepTool_EmptyPatternErrors(t *testing.T) {
	searcher := &fakeSearcher{}
	gt := &GrepTool{Root: "/work", Searcher: searcher, FS: fakeFS{}, Snapshots: hashline.NewMemSnapshotStore()}

	_, err := gt.Execute(context.Background(), json.RawMessage(`{"pattern":""}`))
	if err == nil {
		t.Fatalf("Execute: se esperaba error por pattern vacio")
	}
	if searcher.calls != 0 {
		t.Fatalf("Searcher calls = %d, quiero 0", searcher.calls)
	}
}

func TestGrepTool_ReadFailureAfterMatchErrors(t *testing.T) {
	gt := &GrepTool{
		Root:      "/work",
		Searcher:  &fakeSearcher{result: GrepResult{Matches: []GrepMatch{{Path: "missing.go", Line: 1, Text: "ignored"}}}},
		FS:        fakeFS{},
		Snapshots: hashline.NewMemSnapshotStore(),
	}

	_, err := gt.Execute(context.Background(), json.RawMessage(`{"pattern":"x"}`))
	if err == nil {
		t.Fatalf("Execute: se esperaba error al no poder leer archivo encontrado")
	}
}

func TestGrepTool_BinaryMatchedFileReturnsNoticeWithoutSnapshot(t *testing.T) {
	snaps := hashline.NewMemSnapshotStore()
	gt := &GrepTool{
		Root:      "/work",
		Searcher:  &fakeSearcher{result: GrepResult{Matches: []GrepMatch{{Path: "bin.dat", Line: 1, Text: "ignored"}}}},
		FS:        fakeFS{"/work/bin.dat": []byte("a\x00b")},
		Snapshots: snaps,
	}

	res, err := gt.Execute(context.Background(), json.RawMessage(`{"pattern":"x"}`))
	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}
	if !strings.Contains(res.Output, "[Cannot grep binary file bin.dat; content contains NUL bytes]") {
		t.Fatalf("Execute output no contiene notice binario: %q", res.Output)
	}
	if snap := snaps.Head("/work/bin.dat"); snap != nil {
		t.Fatalf("Head(bin.dat) = %+v, quiero nil", snap)
	}
}

func TestGrepTool_CountsOnlyEmittedMatches(t *testing.T) {
	text := "package tool\nfunc one() {}\n"
	gt := &GrepTool{
		Root: "/work",
		Searcher: &fakeSearcher{result: GrepResult{Matches: []GrepMatch{
			{Path: "a.go", Line: 2, Text: "ignored"},
			{Path: "bin.dat", Line: 1, Text: "ignored"},
			{Path: "stale.go", Line: 99, Text: "ignored"},
		}}},
		FS: fakeFS{
			"/work/a.go":     []byte(text),
			"/work/bin.dat":  []byte("a\x00b"),
			"/work/stale.go": []byte("short\n"),
		},
		Snapshots: hashline.NewMemSnapshotStore(),
	}

	res, err := gt.Execute(context.Background(), json.RawMessage(`{"pattern":"x"}`))
	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}
	if !strings.HasPrefix(res.Output, "Found 1 matches\n") {
		t.Fatalf("Execute output debe contar solo matches emitidos: %q", res.Output)
	}
	if strings.Contains(res.Output, "99:") {
		t.Fatalf("Execute output no debe emitir linea stale fuera de rango: %q", res.Output)
	}
	if !strings.Contains(res.Output, "[Cannot grep binary file bin.dat; content contains NUL bytes]") {
		t.Fatalf("Execute output debe conservar notice binario: %q", res.Output)
	}
}

func TestGrepTool_RejectsSymlinkOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlink no soportado en este entorno: %v", err)
	}

	searcher := &fakeSearcher{}
	gt := &GrepTool{Root: root, Searcher: searcher, FS: osFS{}, Snapshots: hashline.NewMemSnapshotStore()}

	_, err := gt.Execute(context.Background(), json.RawMessage(`{"pattern":"secret","path":"link.txt"}`))
	if err == nil {
		t.Fatalf("Execute: se esperaba error por symlink fuera del workspace")
	}
	if searcher.calls != 0 {
		t.Fatalf("Searcher calls = %d, quiero 0", searcher.calls)
	}
}

func TestGrepTool_SearcherErrorPropagates(t *testing.T) {
	wantErr := errors.New("boom")
	gt := &GrepTool{
		Root:      "/work",
		Searcher:  &fakeSearcher{err: wantErr},
		FS:        fakeFS{},
		Snapshots: hashline.NewMemSnapshotStore(),
	}

	_, err := gt.Execute(context.Background(), json.RawMessage(`{"pattern":"x"}`))
	if !errors.Is(err, wantErr) {
		t.Fatalf("Execute error = %v, quiero %v", err, wantErr)
	}
}

func TestRegistry_GrepDefinitionMaterializedWhenPermitted(t *testing.T) {
	reg := NewRegistry(NewOutputStore(0), &GrepTool{
		Root:      "/work",
		Searcher:  &fakeSearcher{},
		FS:        fakeFS{},
		Snapshots: hashline.NewMemSnapshotStore(),
	})
	mat := reg.Materialize(Permissions{"grep": true})

	if len(mat.Definitions) != 1 {
		t.Fatalf("Definitions: se esperaba 1 def, se obtuvieron %d", len(mat.Definitions))
	}
	def := mat.Definitions[0]
	if def.Name != "grep" {
		t.Fatalf("Definitions[0].Name = %q, quiero grep", def.Name)
	}
	if !strings.Contains(def.Description, "Busca contenido") {
		t.Fatalf("Description = %q, quiero que describa busqueda de contenido", def.Description)
	}
	if !strings.Contains(string(def.Schema), `"pattern"`) || !strings.Contains(string(def.Schema), `"include"`) {
		t.Fatalf("Schema = %s, quiero pattern/include", def.Schema)
	}
}

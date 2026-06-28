package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func globInput(t *testing.T, pattern, path string, limit *int) json.RawMessage {
	t.Helper()
	in := map[string]any{"pattern": pattern}
	if path != "" {
		in["path"] = path
	}
	if limit != nil {
		in["limit"] = *limit
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return b
}

type fakeGlobSearcher struct {
	result GlobSearchResult
	err    error
	calls  []GlobSearch
}

func (f *fakeGlobSearcher) Glob(ctx context.Context, input GlobSearch) (GlobSearchResult, error) {
	f.calls = append(f.calls, input)
	if f.err != nil {
		return GlobSearchResult{}, f.err
	}
	return f.result, nil
}

func TestGlobTool_FindsFilesByPattern(t *testing.T) {
	searcher := &fakeGlobSearcher{result: GlobSearchResult{Entries: []GlobEntry{
		{Path: "app.go"},
		{Path: "internal/tool/read.go"},
	}}}
	gt := &GlobTool{Root: "/work", Searcher: searcher, DefaultLimit: defaultGlobLimit, MaxLimit: maxGlobLimit}

	res, err := gt.Execute(context.Background(), globInput(t, "*.go", "", nil))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if res.Output != "app.go\ninternal/tool/read.go" {
		t.Fatalf("Output = %q", res.Output)
	}
	if len(searcher.calls) != 1 {
		t.Fatalf("Glob calls = %d, want 1", len(searcher.calls))
	}
	if searcher.calls[0].Pattern != "*.go" {
		t.Fatalf("Pattern = %q, want *.go", searcher.calls[0].Pattern)
	}
}

func TestGlobTool_SchemaAdvertisesPatternPathLimit(t *testing.T) {
	var schema struct {
		Properties map[string]map[string]any `json:"properties"`
		Required   []string                  `json:"required"`
	}
	if err := json.Unmarshal((&GlobTool{}).Schema(), &schema); err != nil {
		t.Fatalf("Schema no es JSON valido: %v", err)
	}

	if !reflect.DeepEqual(schema.Required, []string{"pattern"}) {
		t.Fatalf("required = %v, quiero [pattern]", schema.Required)
	}
	for _, name := range []string{"pattern", "path", "limit"} {
		if _, ok := schema.Properties[name]; !ok {
			t.Fatalf("schema no anuncia %s; properties=%v", name, schema.Properties)
		}
	}
	if got := fmt.Sprint(schema.Properties["limit"]["minimum"]); got != "1" {
		t.Fatalf("limit.minimum = %v, quiero 1", schema.Properties["limit"]["minimum"])
	}
}

func TestRegistry_GlobDefinitionMaterializedWhenPermitted(t *testing.T) {
	reg := NewRegistry(NewOutputStore(0), NewGlobTool("/work"))

	mat := reg.Materialize(Permissions{"glob": true})

	if len(mat.Definitions) != 1 {
		t.Fatalf("Definitions: se esperaba 1 def, se obtuvieron %d", len(mat.Definitions))
	}
	if got := mat.Definitions[0].Name; got != "glob" {
		t.Fatalf("Definitions[0].Name = %q, quiero glob", got)
	}
}

func TestGlobTool_NoFilesFound(t *testing.T) {
	searcher := &fakeGlobSearcher{}
	gt := &GlobTool{Root: "/work", Searcher: searcher, DefaultLimit: defaultGlobLimit, MaxLimit: maxGlobLimit}

	res, err := gt.Execute(context.Background(), globInput(t, "*.md", "", nil))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if res.Output != "No files found" {
		t.Fatalf("Output = %q", res.Output)
	}
}

func TestGlobTool_PathNarrowsSearchAndOutputsWorkspaceRelativePaths(t *testing.T) {
	searcher := &fakeGlobSearcher{result: GlobSearchResult{Entries: []GlobEntry{{Path: "tool/read.go"}}}}
	gt := &GlobTool{Root: "/work", Searcher: searcher, DefaultLimit: defaultGlobLimit, MaxLimit: maxGlobLimit}

	res, err := gt.Execute(context.Background(), globInput(t, "*.go", "internal", nil))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if got := filepath.ToSlash(searcher.calls[0].Cwd); got != "/work/internal" {
		t.Fatalf("Cwd = %q, want /work/internal", got)
	}
	if res.Output != "internal/tool/read.go" {
		t.Fatalf("Output = %q", res.Output)
	}
}

func TestGlobTool_LimitBoundsOutputAndShowsNotice(t *testing.T) {
	limit := 2
	searcher := &fakeGlobSearcher{result: GlobSearchResult{
		Entries:   []GlobEntry{{Path: "internal/tool/edit.go"}, {Path: "internal/tool/read.go"}},
		Truncated: true,
	}}
	gt := &GlobTool{Root: "/work", Searcher: searcher, DefaultLimit: defaultGlobLimit, MaxLimit: maxGlobLimit}

	res, err := gt.Execute(context.Background(), globInput(t, "*.go", "", &limit))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	want := "internal/tool/edit.go\ninternal/tool/read.go\n\n[Limit reached: showing first 2 files. Use a narrower pattern or higher limit.]"
	if res.Output != want {
		t.Fatalf("Output\nwant %q\ngot  %q", want, res.Output)
	}
	if searcher.calls[0].Limit != 2 {
		t.Fatalf("Limit = %d, want 2", searcher.calls[0].Limit)
	}
}

func TestGlobTool_DefaultLimit(t *testing.T) {
	searcher := &fakeGlobSearcher{}
	gt := &GlobTool{Root: "/work", Searcher: searcher, DefaultLimit: defaultGlobLimit, MaxLimit: maxGlobLimit}

	if _, err := gt.Execute(context.Background(), globInput(t, "*.go", "", nil)); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if searcher.calls[0].Limit != defaultGlobLimit {
		t.Fatalf("Limit = %d, want %d", searcher.calls[0].Limit, defaultGlobLimit)
	}
}

func TestGlobTool_RejectsInvalidLimit(t *testing.T) {
	for _, limit := range []int{0, -1} {
		searcher := &fakeGlobSearcher{}
		gt := &GlobTool{Root: "/work", Searcher: searcher, DefaultLimit: defaultGlobLimit, MaxLimit: maxGlobLimit}

		_, err := gt.Execute(context.Background(), globInput(t, "*.go", "", &limit))
		if err == nil {
			t.Fatalf("limit %d: expected error", limit)
		}
		if len(searcher.calls) != 0 {
			t.Fatalf("limit %d: searcher was called", limit)
		}
	}
}

func TestGlobTool_RejectsLimitAboveMax(t *testing.T) {
	limit := maxGlobLimit + 1
	searcher := &fakeGlobSearcher{}
	gt := &GlobTool{Root: "/work", Searcher: searcher, DefaultLimit: defaultGlobLimit, MaxLimit: maxGlobLimit}

	_, err := gt.Execute(context.Background(), globInput(t, "*.go", "", &limit))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "5000") {
		t.Fatalf("error = %q, want max limit", err.Error())
	}
	if len(searcher.calls) != 0 {
		t.Fatalf("searcher was called")
	}
}

func TestGlobTool_RejectsEmptyPattern(t *testing.T) {
	searcher := &fakeGlobSearcher{}
	gt := &GlobTool{Root: "/work", Searcher: searcher, DefaultLimit: defaultGlobLimit, MaxLimit: maxGlobLimit}

	_, err := gt.Execute(context.Background(), globInput(t, "", "", nil))
	if err == nil {
		t.Fatal("expected error")
	}
	if len(searcher.calls) != 0 {
		t.Fatalf("searcher was called")
	}
}

func TestGlobTool_InvalidInputErrors(t *testing.T) {
	searcher := &fakeGlobSearcher{}
	gt := &GlobTool{Root: "/work", Searcher: searcher, DefaultLimit: defaultGlobLimit, MaxLimit: maxGlobLimit}

	_, err := gt.Execute(context.Background(), json.RawMessage(`{"pattern":`))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "input invalido") {
		t.Fatalf("error = %q", err.Error())
	}
	if len(searcher.calls) != 0 {
		t.Fatalf("searcher was called")
	}
}

func TestGlobTool_RejectsPathOutsideRoot(t *testing.T) {
	searcher := &fakeGlobSearcher{}
	gt := &GlobTool{Root: "/work", Searcher: searcher, DefaultLimit: defaultGlobLimit, MaxLimit: maxGlobLimit}

	_, err := gt.Execute(context.Background(), globInput(t, "*.go", "../secret", nil))
	if err == nil {
		t.Fatal("expected error")
	}
	if len(searcher.calls) != 0 {
		t.Fatalf("searcher was called")
	}
}

func TestGlobTool_RejectsAbsolutePath(t *testing.T) {
	searcher := &fakeGlobSearcher{}
	gt := &GlobTool{Root: "/work", Searcher: searcher, DefaultLimit: defaultGlobLimit, MaxLimit: maxGlobLimit}

	_, err := gt.Execute(context.Background(), globInput(t, "*.go", "/tmp", nil))
	if err == nil {
		t.Fatal("expected error")
	}
	if len(searcher.calls) != 0 {
		t.Fatalf("searcher was called")
	}
}

func TestGlobTool_RejectsSearcherRowsOutsideRoot(t *testing.T) {
	searcher := &fakeGlobSearcher{result: GlobSearchResult{Entries: []GlobEntry{{Path: "../secret.txt"}}}}
	gt := &GlobTool{Root: "/work", Searcher: searcher, DefaultLimit: defaultGlobLimit, MaxLimit: maxGlobLimit}

	_, err := gt.Execute(context.Background(), globInput(t, "*.txt", "", nil))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestGlobTool_NormalizesWindowsSeparators(t *testing.T) {
	searcher := &fakeGlobSearcher{result: GlobSearchResult{Entries: []GlobEntry{{Path: `tool\read.go`}}}}
	gt := &GlobTool{Root: "/work", Searcher: searcher, DefaultLimit: defaultGlobLimit, MaxLimit: maxGlobLimit}

	res, err := gt.Execute(context.Background(), globInput(t, "*.go", "internal", nil))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if res.Output != "internal/tool/read.go" {
		t.Fatalf("Output = %q", res.Output)
	}
}

func TestGlobTool_SearchErrorBecomesToolError(t *testing.T) {
	searcher := &fakeGlobSearcher{err: errors.New("rg missing")}
	gt := &GlobTool{Root: "/work", Searcher: searcher, DefaultLimit: defaultGlobLimit, MaxLimit: maxGlobLimit}

	_, err := gt.Execute(context.Background(), globInput(t, "*.go", "", nil))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "glob") || !strings.Contains(err.Error(), "rg missing") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestGlobTool_RejectsSymlinkSearchRootOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "out")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	runner := &fakeLineRunner{}
	gt := &GlobTool{
		Root:         root,
		Searcher:     &RipgrepGlobSearcher{Runner: runner},
		DefaultLimit: defaultGlobLimit,
		MaxLimit:     maxGlobLimit,
	}

	_, err := gt.Execute(context.Background(), globInput(t, "*.go", "out", nil))
	if err == nil {
		t.Fatal("expected error")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner was called")
	}
}

func TestGlobTool_ContextCancellationPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	searcher := &fakeGlobSearcher{err: context.Canceled}
	gt := &GlobTool{Root: "/work", Searcher: searcher, DefaultLimit: defaultGlobLimit, MaxLimit: maxGlobLimit}

	_, err := gt.Execute(ctx, globInput(t, "*.go", "", nil))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

type fakeLineRunner struct {
	lines     []string
	truncated bool
	err       error
	calls     []lineRun
}

type lineRun struct {
	cwd    string
	binary string
	args   []string
	limit  int
}

func (f *fakeLineRunner) RunLines(ctx context.Context, cwd, binary string, args []string, limit int) ([]string, bool, error) {
	f.calls = append(f.calls, lineRun{cwd: cwd, binary: binary, args: append([]string(nil), args...), limit: limit})
	return f.lines, f.truncated, f.err
}

func TestRipgrepGlobSearcher_UsesProductionRipgrepArgs(t *testing.T) {
	runner := &fakeLineRunner{lines: []string{"a.go"}}
	searcher := &RipgrepGlobSearcher{Binary: "rg-test", Runner: runner}

	res, err := searcher.Glob(context.Background(), GlobSearch{Cwd: "/work", Pattern: "*.go", Limit: 10})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}

	if len(res.Entries) != 1 || res.Entries[0].Path != "a.go" {
		t.Fatalf("Entries = %+v", res.Entries)
	}
	wantArgs := []string{"--no-config", "--files", "--glob=*.go", "--glob=!**/.git/**", "--glob=!**/node_modules/**", "."}
	if !reflect.DeepEqual(runner.calls[0].args, wantArgs) {
		t.Fatalf("args\nwant %v\ngot  %v", wantArgs, runner.calls[0].args)
	}
	if runner.calls[0].binary != "rg-test" {
		t.Fatalf("binary = %q", runner.calls[0].binary)
	}
	if runner.calls[0].limit != 10 {
		t.Fatalf("limit = %d", runner.calls[0].limit)
	}
}

func TestRipgrepGlobSearcher_EmptyExitCodeOneIsNoFiles(t *testing.T) {
	runner := &fakeLineRunner{err: &ripgrepExitError{Code: 1}}
	searcher := &RipgrepGlobSearcher{Runner: runner}

	res, err := searcher.Glob(context.Background(), GlobSearch{Cwd: "/work", Pattern: "*.go", Limit: 10})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(res.Entries) != 0 {
		t.Fatalf("Entries = %+v, want none", res.Entries)
	}
}

func TestRipgrepGlobSearcher_NonzeroFailureIncludesBoundedStderr(t *testing.T) {
	stderr := strings.Repeat("x", 9000)
	runner := &fakeLineRunner{err: &ripgrepExitError{Code: 2, Stderr: stderr}}
	searcher := &RipgrepGlobSearcher{Runner: runner}

	_, err := searcher.Glob(context.Background(), GlobSearch{Cwd: "/work", Pattern: "[", Limit: 10})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "ripgrep") || !strings.Contains(err.Error(), "exit 2") {
		t.Fatalf("error = %q", err.Error())
	}
	if len(err.Error()) > 8400 {
		t.Fatalf("error length = %d, want bounded", len(err.Error()))
	}
}

func TestRipgrepGlobSearcher_StripsDotSlashAndBackslashes(t *testing.T) {
	runner := &fakeLineRunner{lines: []string{`./a\b.go`}}
	searcher := &RipgrepGlobSearcher{Runner: runner}

	res, err := searcher.Glob(context.Background(), GlobSearch{Cwd: "/work", Pattern: "*.go", Limit: 10})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(res.Entries) != 1 || res.Entries[0].Path != "a/b.go" {
		t.Fatalf("Entries = %+v", res.Entries)
	}
}

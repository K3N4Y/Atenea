package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	contract "github.com/K3N4Y/atenea/agentcore/tool"
	"github.com/K3N4Y/atenea/internal/tool/editmode"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

func publicHashlineCall(t *testing.T, edit *EditTool, patch string) (Result, error) {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"input": patch})
	if err != nil {
		t.Fatal(err)
	}
	return edit.Execute(context.Background(), raw)
}

func runTogether(t *testing.T, calls ...func() error) []error {
	t.Helper()
	start := make(chan struct{})
	errs := make([]error, len(calls))
	var ready, done sync.WaitGroup
	ready.Add(len(calls))
	done.Add(len(calls))
	for i, call := range calls {
		i, call := i, call
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			errs[i] = call()
		}()
	}
	ready.Wait()
	close(start)
	finished := make(chan struct{})
	go func() { done.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("public edits deadlocked")
	}
	return errs
}

func assertSnapshotMatchesDisk(t *testing.T, snaps *hashline.MemSnapshotStore, paths ...string) {
	t.Helper()
	for _, path := range paths {
		data, err := os.ReadFile(path)
		head := snaps.Head(path)
		if errors.Is(err, os.ErrNotExist) {
			if head != nil {
				t.Fatalf("removed %s retained snapshot %#v", path, head)
			}
			continue
		}
		if err != nil || head == nil || head.Text != string(data) {
			t.Fatalf("snapshot/disk mismatch %s: disk=%q err=%v snapshot=%#v", path, data, err, head)
		}
	}
}

func TestInverseMovesAreSerializable(t *testing.T) {
	root := t.TempDir()
	a, b, x := filepath.Join(root, "a"), filepath.Join(root, "b"), filepath.Join(root, "x")
	matrixMustWrite(t, a, "A")
	matrixMustWrite(t, b, "B")
	snaps := hashline.NewMemSnapshotStore()
	ha, _ := snaps.Record(a, "A")
	hb, _ := snaps.Record(b, "B")
	one, two := NewEditTool(root, hashline.OSFilesystem{}, snaps), NewEditTool(root, hashline.OSFilesystem{}, snaps)
	var r1, r2 Result
	errs := runTogether(t,
		func() error { var err error; r1, err = publicHashlineCall(t, one, "[a#"+ha+"]\nMV x"); return err },
		func() error { var err error; r2, err = publicHashlineCall(t, two, "[b#"+hb+"]\nMV a"); return err },
	)
	if errs[0] != nil || (errs[1] == nil) != (len(r2.Files) == 1 && r2.Files[0].Committed) {
		t.Fatalf("move outcomes r1=%+v err1=%v r2=%+v err2=%v", r1, errs[0], r2, errs[1])
	}
	if len(r1.Files) != 1 || !r1.Files[0].Committed {
		t.Fatalf("A->X must land: %+v", r1)
	}
	xb, err := os.ReadFile(x)
	if err != nil || string(xb) != "A" {
		t.Fatalf("x=%q err=%v", xb, err)
	}
	ab, aerr := os.ReadFile(a)
	if errs[1] == nil {
		if aerr != nil || string(ab) != "B" {
			t.Fatalf("successful B->A left a=%q err=%v", ab, aerr)
		}
	} else if !errors.Is(aerr, os.ErrNotExist) {
		t.Fatalf("failed B->A unexpectedly left a=%q err=%v", ab, aerr)
	}
	if _, err := os.Stat(b); errs[1] == nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful B->A retained source: %v", err)
	}
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".atenea-") {
			t.Fatalf("temporary file leaked: %s", entry.Name())
		}
	}
	assertSnapshotMatchesDisk(t, snaps, a, b, x)
}

func TestReplaceAndMoveAreSerializable(t *testing.T) {
	root := t.TempDir()
	a, x := filepath.Join(root, "a"), filepath.Join(root, "x")
	matrixMustWrite(t, a, "old")
	snaps := hashline.NewMemSnapshotStore()
	ha, _ := snaps.Record(a, "old")
	replace := NewEditTool(root, hashline.OSFilesystem{}, snaps)
	replace.Mode = editmode.Replace
	move := NewEditTool(root, hashline.OSFilesystem{}, snaps)
	var rr, mr Result
	replaceRaw, _ := json.Marshal(map[string]any{"path": "a", "old_string": "old", "new_string": "new"})
	errs := runTogether(t,
		func() error { var err error; rr, err = replace.Execute(context.Background(), replaceRaw); return err },
		func() error { var err error; mr, err = publicHashlineCall(t, move, "[a#"+ha+"]\nMV x"); return err },
	)
	if errs[0] != nil && errs[1] != nil {
		t.Fatalf("both operations failed: move=%+v/%v replace=%+v/%v", mr, errs[1], rr, errs[0])
	}
	if errs[1] == nil {
		got, err := os.ReadFile(x)
		if err != nil || string(got) != "old" && string(got) != "new" {
			t.Fatalf("destination=%q err=%v", got, err)
		}
		if errs[0] == nil && string(got) != "new" {
			t.Fatalf("committed replace was lost: result=%+v destination=%q", rr, got)
		}
		if _, err := os.Stat(a); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("move retained source: %v", err)
		}
	} else {
		got, err := os.ReadFile(a)
		if err != nil || string(got) != "new" {
			t.Fatalf("replace-only outcome a=%q err=%v", got, err)
		}
	}
	assertSnapshotMatchesDisk(t, snaps, a, x)
}

func TestOverlappingMultiSectionCallsAreSerializable(t *testing.T) {
	root := t.TempDir()
	paths := []string{filepath.Join(root, "a"), filepath.Join(root, "b"), filepath.Join(root, "c")}
	snaps := hashline.NewMemSnapshotStore()
	hashes := make([]string, 3)
	for i, path := range paths {
		matrixMustWrite(t, path, strings.ToUpper(filepath.Base(path)))
		hashes[i], _ = snaps.Record(path, strings.ToUpper(filepath.Base(path)))
	}
	one, two := NewEditTool(root, hashline.OSFilesystem{}, snaps), NewEditTool(root, hashline.OSFilesystem{}, snaps)
	p1 := "[a#" + hashes[0] + "]\nPUT 1:\n+A1\n[b#" + hashes[1] + "]\nPUT 1:\n+B1"
	p2 := "[b#" + hashes[1] + "]\nPUT 1:\n+B2\n[c#" + hashes[2] + "]\nPUT 1:\n+C2"
	var r1, r2 Result
	errs := runTogether(t,
		func() error { var err error; r1, err = publicHashlineCall(t, one, p1); return err },
		func() error { var err error; r2, err = publicHashlineCall(t, two, p2); return err },
	)
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("want exactly one serializable call: r1=%+v/%v r2=%+v/%v", r1, errs[0], r2, errs[1])
	}
	want := []string{"A1", "B1", "C"}
	if errs[1] == nil {
		want = []string{"A", "B2", "C2"}
	}
	for i, path := range paths {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want[i] {
			t.Fatalf("%s=%q err=%v want=%q", path, got, err, want[i])
		}
	}
	assertSnapshotMatchesDisk(t, snaps, paths...)
}

func TestCommitFaultPreservesPrefixStateAtEveryPosition(t *testing.T) {
	for fail := 1; fail <= 3; fail++ {
		fs := &nthWriteFS{memoryEditFS: &memoryEditFS{files: map[string][]byte{"a": []byte("A"), "b": []byte("B"), "c": []byte("C")}}, fail: fail}
		snaps := hashline.NewMemSnapshotStore()
		hashes := make([]string, 3)
		for i, p := range []string{"a", "b", "c"} {
			hashes[i], _ = snaps.Record(p, strings.ToUpper(p))
		}
		patch := "[a#" + hashes[0] + "]\nCUT 1 @landed\nPUT >$:\n+AA\n[b#" + hashes[1] + "]\nCUT 1\nPUT >$:\n+BB\n[c#" + hashes[2] + "]\nPUT 1:\n+CC"
		res, err := publicHashlineCall(t, NewEditTool(".", fs, snaps), patch)
		if err == nil || len(res.Files) != 3 {
			t.Fatalf("position=%d result=%+v err=%v", fail, res, err)
		}
		for i, p := range []string{"a", "b", "c"} {
			committed := i < fail-1
			want := strings.ToUpper(p)
			if committed {
				want += strings.ToUpper(p)
			}
			if res.Files[i].Committed != committed || string(fs.files[p]) != want || snaps.Head(p).Text != want {
				t.Fatalf("position=%d file[%d]=%+v disk=%q snapshot=%#v", fail, i, res.Files[i], fs.files[p], snaps.Head(p))
			}
		}
		fs.fail = 0
		var retry strings.Builder
		for i, p := range []string{"a", "b", "c"} {
			if i >= fail-1 {
				retry.WriteString("[" + p + "#" + hashes[i] + "]\nPUT 1:\n+" + strings.ToUpper(p) + strings.ToUpper(p) + "\n")
			}
		}
		if _, retryErr := publicHashlineCall(t, NewEditTool(".", fs, snaps), retry.String()); retryErr != nil {
			t.Fatal(retryErr)
		}
		for _, p := range []string{"a", "b", "c"} {
			if string(fs.files[p]) != strings.ToUpper(p)+strings.ToUpper(p) {
				t.Fatalf("retry duplicated prefix %s=%q", p, fs.files[p])
			}
		}
	}
}

type uncertainWriteFS struct {
	*memoryEditFS
	n, uncertain int
}

func (f *uncertainWriteFS) WriteFile(path string, data []byte, mode os.FileMode) error {
	f.n++
	_ = f.memoryEditFS.WriteFile(path, data, mode)
	if f.n == f.uncertain {
		return &hashline.CommitUncertainError{Err: errors.New("injected post-visible uncertainty")}
	}
	return nil
}

func TestCommittedUncertaintyPreservesPrefixStateAtEveryPosition(t *testing.T) {
	for position := 1; position <= 3; position++ {
		fs := &uncertainWriteFS{memoryEditFS: &memoryEditFS{files: map[string][]byte{"a": []byte("A"), "b": []byte("B"), "c": []byte("C")}}, uncertain: position}
		snaps := hashline.NewMemSnapshotStore()
		hashes := make([]string, 3)
		for i, p := range []string{"a", "b", "c"} {
			hashes[i], _ = snaps.Record(p, strings.ToUpper(p))
		}
		patch := "[a#" + hashes[0] + "]\nCUT 1 @landed\nPUT >$:\n+AA\n[b#" + hashes[1] + "]\nCUT 1\nPUT >$:\n+BB\n[c#" + hashes[2] + "]\nPUT 1:\n+CC"
		res, err := publicHashlineCall(t, NewEditTool(".", fs, snaps), patch)
		if err != nil || len(res.Files) != 3 || !strings.Contains(res.Files[position-1].Error, "uncertain") {
			t.Fatalf("position=%d result=%+v err=%v", position, res, err)
		}
		for i, p := range []string{"a", "b", "c"} {
			landed := i < position
			want := strings.ToUpper(p)
			if landed {
				want += strings.ToUpper(p)
			}
			if res.Files[i].Committed != landed || string(fs.files[p]) != want || snaps.Head(p).Text != want {
				t.Fatalf("position=%d file[%d]=%+v disk=%q snapshot=%#v", position, i, res.Files[i], fs.files[p], snaps.Head(p))
			}
		}
		if fs.n != position {
			t.Fatalf("retried after uncertainty: writes=%d want=%d", fs.n, position)
		}
	}
}

func TestPartialAggregatePreservesOrderedMixedOutcomes(t *testing.T) {
	// The Publisher persistence integration lives in internal/session/runner;
	// this test pins the aggregate shape before that package's durable round trip.
	files := []contract.FileResult{
		{Path: "a", SourcePath: "a", Operation: contract.FileUpdated, OldText: "A", NewText: "AA", Diff: "d1", Header: "[a#h]", Warnings: []string{"w"}, Diagnostics: []contract.Diagnostic{{Severity: "warning", Message: "d", Line: 1}}, Committed: true},
		{Path: "b", SourcePath: "b", Operation: contract.FileError, Error: "failed", DisplayError: "failed"},
		{Path: "c", SourcePath: "c", Operation: contract.FileError, Error: "not applied because an earlier section failed", DisplayError: "not applied because an earlier section failed"},
	}
	got := NewOutputStore(4).CapResult("partial", Result{Output: "guidance", Diff: "aggregate", Files: files, Metadata: map[string]any{"partial": true}})
	if got.Output != "guid" || !got.Truncated || got.Diff != "aggregate" || !reflect.DeepEqual(got.Files, files) {
		t.Fatalf("partial aggregate damaged before publication: %+v", got)
	}
}

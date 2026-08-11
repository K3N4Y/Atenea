package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

// fakeWriteFS backs FileWriter with in-memory maps: it records directories and
// published bytes without touching disk.
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

func (f *fakeWriteFS) CreateExclusive(name string, data []byte, perm os.FileMode) error {
	if f.files[name] {
		return os.ErrExist
	}
	f.writes[name] = data
	f.files[name] = true
	return nil
}

// exclusiveCollisionWriteFS makes the atomic publication report a destination
// collision, so the tool's translation of os.ErrExist can be pinned without a
// real racing process.
type exclusiveCollisionWriteFS struct {
	*fakeWriteFS
}

func (f *exclusiveCollisionWriteFS) CreateExclusive(string, []byte, os.FileMode) error {
	return os.ErrExist
}

// writeInput serializes {path, content} as sent by the model.
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

// TestWriteTool_ReturnsAllAdditionsDiff verifies that write returns a diff for
// a new file: all lines are additions and the header uses the relative path.
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

// TestWriteTool_CreatesNewFileAndReturnsHeader is the happy path: write resolves
// the relative path under Root, writes the content, and returns the
// [path#HASH] header for the model to chain into an edit.
func TestWriteTool_CreatesNewFileAndReturnsHeader(t *testing.T) {
	snaps := hashline.NewMemSnapshotStore()
	fs := newFakeWriteFS()
	wt := &WriteTool{Root: "/work", FS: fs, Snapshots: snaps}

	res, err := wt.Execute(context.Background(), writeInput(t, "hola.md", "# Hola\nmundo\n"))
	if err != nil {
		t.Fatalf("Execute: unexpected error: %v", err)
	}

	if !strings.HasPrefix(res.Output, "[hola.md#") {
		t.Fatalf("output = %q, want hashline header with relative path", res.Output)
	}

	written, ok := fs.writes["/work/hola.md"]
	if !ok {
		t.Fatalf("file /work/hola.md was not written; writes=%v", fs.writes)
	}
	if string(written) != "# Hola\nmundo\n" {
		t.Errorf("written content = %q, want %q", string(written), "# Hola\nmundo\n")
	}
}

// TestWriteTool_RejectsExistingFile verifies that write remains new-file-only
// and reports the exact actionable error used by the model.
func TestWriteTool_RejectsExistingFile(t *testing.T) {
	snaps := hashline.NewMemSnapshotStore()
	fs := newFakeWriteFS()
	fs.files["/work/hola.md"] = true
	wt := &WriteTool{Root: "/work", FS: fs, Snapshots: snaps}

	_, err := wt.Execute(context.Background(), writeInput(t, "hola.md", "nuevo"))
	if err == nil {
		t.Fatal("Execute: expected error when replacing an existing file")
	}
	want := "write: file already exists; use read+edit: hola.md"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
	if len(fs.writes) != 0 {
		t.Errorf("file was written although it already existed: %v", fs.writes)
	}
}

// TestWriteTool_RejectsPathOutsideRoot verifies the sandbox fail-closed behavior:
// a path escaping Root is rejected before the filesystem is touched.
func TestWriteTool_RejectsPathOutsideRoot(t *testing.T) {
	snaps := hashline.NewMemSnapshotStore()
	fs := newFakeWriteFS()
	wt := &WriteTool{Root: "/work", FS: fs, Snapshots: snaps}

	if _, err := wt.Execute(context.Background(), writeInput(t, "../secret.md", "x")); err == nil {
		t.Fatalf("Execute: expected error for a path outside the workspace")
	}
	if len(fs.writes) != 0 {
		t.Errorf("file was written outside the sandbox: %v", fs.writes)
	}
}

// TestWriteTool_AcceptsAbsolutePathInsideRoot verifies that write accepts an
// absolute path inside Root and publishes it at the equivalent absolute path.
func TestWriteTool_AcceptsAbsolutePathInsideRoot(t *testing.T) {
	snaps := hashline.NewMemSnapshotStore()
	fs := newFakeWriteFS()
	wt := &WriteTool{Root: "/work", FS: fs, Snapshots: snaps}

	if _, err := wt.Execute(context.Background(), writeInput(t, "/work/sub/new.md", "hello")); err != nil {
		t.Fatalf("Execute with an absolute path inside Root: unexpected error: %v", err)
	}
	if got, ok := fs.writes["/work/sub/new.md"]; !ok || string(got) != "hello" {
		t.Fatalf("CreateExclusive: want /work/sub/new.md = %q, writes = %v", "hello", fs.writes)
	}
}

// TestWriteTool_RejectsSymlinkParentOutsideRoot verifies that creating a file
// below a symlink pointing outside the workspace is rejected.
func TestWriteTool_RejectsSymlinkParentOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "out")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	wt := NewWriteTool(root, hashline.NewMemSnapshotStore())
	if _, err := wt.Execute(context.Background(), writeInput(t, "out/new.txt", "secret")); err == nil {
		t.Fatal("expected error for a symlink parent outside the workspace")
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("outside/new.txt should not exist; stat error=%v", err)
	}
}

// TestWriteTool_InvalidInputErrors verifies that malformed JSON returns an error
// without writing.
func TestWriteTool_InvalidInputErrors(t *testing.T) {
	snaps := hashline.NewMemSnapshotStore()
	fs := newFakeWriteFS()
	wt := &WriteTool{Root: "/work", FS: fs, Snapshots: snaps}

	if _, err := wt.Execute(context.Background(), json.RawMessage("{")); err == nil {
		t.Fatal("Execute: expected an invalid-input error")
	}
	if len(fs.writes) != 0 {
		t.Errorf("invalid input created files: %v", fs.writes)
	}
}

// TestWriteTool_CreatesParentDirsAndRecordsSeenSnapshot verifies that write
// creates parent directories and records the authored lines as seen for edit.
func TestWriteTool_CreatesParentDirsAndRecordsSeenSnapshot(t *testing.T) {
	snaps := hashline.NewMemSnapshotStore()
	fs := newFakeWriteFS()
	wt := &WriteTool{Root: "/work", FS: fs, Snapshots: snaps}

	const abs = "/work/docs/new/foo.md"
	if _, err := wt.Execute(context.Background(), writeInput(t, "docs/new/foo.md", "one\ntwo\n")); err != nil {
		t.Fatalf("Execute: unexpected error: %v", err)
	}

	if !fs.dirs["/work/docs/new"] {
		t.Errorf("parent directory /work/docs/new was not created; dirs=%v", fs.dirs)
	}

	snap := snaps.Head(abs)
	if snap == nil {
		t.Fatalf("snapshot was not recorded for %s", abs)
	}
	if snap.Text != "one\ntwo\n" {
		t.Errorf("snapshot.Text = %q, want %q", snap.Text, "one\ntwo\n")
	}
	for _, line := range []int{1, 2} {
		if _, ok := snap.Seen[line]; !ok {
			t.Errorf("line %d was not marked as seen; Seen=%v", line, snap.Seen)
		}
	}
}

// The tool must publish through the exclusive, atomic path and return a usable
// hashline header, with content normalized to LF.
func TestWriteTool_UsesExclusiveCreationAndReturnsHeader(t *testing.T) {
	fs := newFakeWriteFS()
	wt := &WriteTool{Root: "/work", FS: fs, Snapshots: hashline.NewMemSnapshotStore()}

	res, err := wt.Execute(context.Background(), writeInput(t, "new.txt", "one\r\ntwo\r\n"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasPrefix(res.Output, "[new.txt#") {
		t.Fatalf("output = %q, want hashline header", res.Output)
	}
	if got := string(fs.writes["/work/new.txt"]); got != "one\ntwo\n" {
		t.Fatalf("written content = %q, want normalized LF content", got)
	}
}

// TestWriteToolConcurrentCreationHasOneWinner races real concurrent creations of
// the same path against the real filesystem, because that is where the guarantee
// lives: exclusivity comes from the kernel refusing a second link, not from
// anything this package can arrange. A fake that serializes its own map would
// pass with the old check-then-write sequence too, and prove nothing.
func TestWriteToolConcurrentCreationHasOneWinner(t *testing.T) {
	root := t.TempDir()
	wt := &WriteTool{Root: root, FS: osWriteFS{}, Snapshots: hashline.NewMemSnapshotStore()}

	const racers = 8
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	errs := make([]error, racers)
	for i := range racers {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			input := writeInput(t, "same.txt", strings.Repeat("x", i+1))
			start.Wait() // release every goroutine at once
			_, errs[i] = wt.Execute(context.Background(), input)
		}(i)
	}
	start.Done()
	done.Wait()

	successes := 0
	for i, err := range errs {
		if err == nil {
			successes++
			continue
		}
		want := "write: file already exists; use read+edit: same.txt"
		if err.Error() != want {
			t.Errorf("racer %d error = %q, want %q", i, err, want)
		}
	}
	if successes != 1 {
		t.Fatalf("successful creations = %d, want exactly one: a losing racer overwrote the winner", successes)
	}

	// The surviving file must be one racer's content in full, never a blend of
	// two: a torn publish is the failure atomicity exists to prevent.
	published, err := os.ReadFile(filepath.Join(root, "same.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Trim(string(published), "x") != "" || len(published) == 0 {
		t.Fatalf("published content = %q, want one racer's content intact", published)
	}
}

// TestWriteToolRefusesToReplaceAnExistingFile pins the contract against the real
// filesystem rather than a fake that reports whatever it was told.
func TestWriteToolRefusesToReplaceAnExistingFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "taken.txt")
	if err := os.WriteFile(target, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt := &WriteTool{Root: root, FS: osWriteFS{}, Snapshots: hashline.NewMemSnapshotStore()}

	_, err := wt.Execute(context.Background(), writeInput(t, "taken.txt", "replacement"))
	want := "write: file already exists; use read+edit: taken.txt"
	if err == nil || err.Error() != want {
		t.Fatalf("Execute error = %v, want %q", err, want)
	}
	// The refusal must not have touched the existing content.
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "original" {
		t.Fatalf("existing file = %q, want it untouched", got)
	}
}

// TestWriteToolLeavesNoTemporaryBehind checks the real publish path: a successful
// create must leave the destination and nothing else. The temporary that
// atomicCreateWithOps stages through is an implementation detail the workspace
// must never see afterwards.
func TestWriteToolLeavesNoTemporaryBehind(t *testing.T) {
	root := t.TempDir()
	wt := &WriteTool{Root: root, FS: osWriteFS{}, Snapshots: hashline.NewMemSnapshotStore()}

	if _, err := wt.Execute(context.Background(), writeInput(t, "created.txt", "content")); err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if len(names) != 1 || names[0] != "created.txt" {
		t.Fatalf("directory = %v, want only created.txt (a staging temporary leaked)", names)
	}
}

func TestWriteToolHonorsCanceledContextBeforeCreation(t *testing.T) {
	fs := newFakeWriteFS()
	wt := &WriteTool{Root: "/work", FS: fs, Snapshots: hashline.NewMemSnapshotStore()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := wt.Execute(ctx, writeInput(t, "canceled.txt", "content")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute error = %v, want context.Canceled", err)
	}
	if len(fs.writes) != 0 {
		t.Fatalf("canceled write created files: %v", fs.writes)
	}
}

package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	contract "github.com/K3N4Y/atenea/agentcore/tool"
	"github.com/K3N4Y/atenea/internal/tool/editmode"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

func TestHashlineEditThenMovePreservesContentResultAndProvenance(t *testing.T) {
	root := t.TempDir()
	source, destination := filepath.Join(root, "source.txt"), filepath.Join(root, "moved", "destination.txt")
	if err := os.WriteFile(source, []byte("one\ntwo\nthree\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	snaps := hashline.NewMemSnapshotStore()
	read := NewReadTool(root, snaps)
	readResult, err := read.Execute(context.Background(), json.RawMessage(`{"path":"source.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	old := snaps.Head(source)
	if old == nil || !strings.Contains(readResult.Output, "[source.txt#"+old.Hash+"]") {
		t.Fatalf("read=%q snapshot=%+v", readResult.Output, old)
	}

	edit := NewEditTool(root, hashline.OSFilesystem{}, snaps)
	body, _ := json.Marshal(map[string]string{"input": "[source.txt#" + old.Hash + "]\nPUT 2:\n+TWO\nMV moved/destination.txt"})
	result, err := edit.Execute(context.Background(), body)
	if err != nil {
		t.Fatal(err)
	}
	want := "one\nTWO\nthree\n"
	got, readErr := os.ReadFile(destination)
	if readErr != nil || string(got) != want {
		t.Fatalf("destination bytes=%q err=%v", got, readErr)
	}
	if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source must be absent: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("files=%+v", result.Files)
	}
	file := result.Files[0]
	if file.Path != destination || file.SourcePath != source || file.Destination != destination || file.Operation != contract.FileMoved || !file.Committed || file.OldText != "one\ntwo\nthree\n" || file.NewText != want || file.FirstChangedLine != 2 {
		t.Fatalf("file result=%+v", file)
	}
	if file.Diff == "" || !strings.Contains(file.Diff, "-two") || !strings.Contains(file.Diff, "+TWO") || file.Header == "" || !strings.Contains(file.Header, "[moved/destination.txt#") {
		t.Fatalf("diff/header not usable: %+v", file)
	}
	if snaps.Head(source) != nil {
		t.Fatal("source history was not removed")
	}
	newHead := snaps.Head(destination)
	if newHead == nil || newHead.Text != want || newHead.Version == old.Version {
		t.Fatalf("destination head=%+v old=%+v", newHead, old)
	}
	if relocated := snaps.ByContent(old.Text); len(relocated) != 1 || relocated[0].Path != destination || relocated[0].Version != old.Version || len(relocated[0].Seen) != 3 {
		t.Fatalf("relocated history=%+v", relocated)
	}

	// The move result must be immediately chainable at the destination.
	chain, _ := json.Marshal(map[string]string{"input": "[moved/destination.txt#" + newHead.Hash + "]\nPUT 3:\n+THREE"})
	chained, err := edit.Execute(context.Background(), chain)
	if err != nil || len(chained.Files) != 1 || chained.Files[0].FirstChangedLine != 3 {
		t.Fatalf("chained result=%+v err=%v", chained, err)
	}
	if got, _ := os.ReadFile(destination); string(got) != "one\nTWO\nTHREE\n" {
		t.Fatalf("chained bytes=%q", got)
	}
}

func TestPatchMovesRelocateCollisionHistoryAcrossVersions(t *testing.T) {
	for _, mode := range []editmode.Mode{editmode.Patch, editmode.ApplyPatch} {
		for _, uncertain := range []bool{false, true} {
			t.Run(string(mode)+map[bool]string{false: "/committed", true: "/source-remains"}[uncertain], func(t *testing.T) {
				root := filepath.Clean("/work")
				source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
				a, b := round4ToolCollisionFixture(t)
				texts := []string{"oldest\n", a + "\n", b + "\n", "head\n"}
				snaps, versions := hashline.NewMemSnapshotStore(), map[string]uint64{}
				snaps.Record(destination, "destination-old\n")
				for i, text := range texts {
					snaps.Record(source, text)
					s := snaps.ByContent(text)[0]
					versions[text] = s.Version
					snaps.RecordSeenSnapshot(source, s.Version, []int{i + 1})
				}
				fs := &round4MoveFaultFS{files: map[string][]byte{source: []byte("head\n")}}
				if uncertain {
					fs.kind = "destination-committed-source-remains"
				}
				edit := NewEditTool(root, fs, snaps)
				edit.Mode = mode
				var input json.RawMessage
				if mode == editmode.Patch {
					input = json.RawMessage(`{"path":"source","edits":[{"rename":"destination","diff":"@@\n-head\n+landed"}]}`)
				} else {
					input, _ = json.Marshal(map[string]string{"input": "*** Begin Patch\n*** Update File: source\n*** Move to: destination\n@@\n-head\n+landed\n*** End Patch"})
				}
				result, err := edit.Execute(context.Background(), input)
				if err != nil || len(result.Files) != 1 || !result.Files[0].Committed || string(fs.files[destination]) != "landed\n" {
					t.Fatalf("result=%+v err=%v files=%q", result, err, fs.files)
				}
				if uncertain {
					if snaps.Head(source) == nil || snaps.Head(source).Text != "head\n" || snaps.Head(destination).Text != "landed\n" {
						t.Fatalf("separate source=%+v dest=%+v", snaps.Head(source), snaps.Head(destination))
					}
					return
				}
				if snaps.Head(source) != nil || snaps.Head(destination).Text != "landed\n" {
					t.Fatalf("source=%+v dest=%+v", snaps.Head(source), snaps.Head(destination))
				}
				for _, text := range []string{"head\n", a + "\n", b + "\n"} {
					matches := snaps.ByContent(text)
					if len(matches) != 1 || matches[0].Path != destination || matches[0].Version != versions[text] || len(matches[0].Seen) != 1 {
						t.Fatalf("identity lost for %q: %+v", text, matches)
					}
				}
				if got := snaps.Candidates(destination, hashline.ComputeFileHash(a+"\n")); len(got) != 2 {
					t.Fatalf("collision lost: %+v", got)
				}
				// A public hashline edit can immediately use the landed head with
				// seen enforcement; colliding retained versions remain unambiguous.
				landed := snaps.Head(destination)
				snaps.RecordSeenSnapshot(destination, landed.Version, []int{1})
				chain := NewEditTool(root, fs, snaps)
				chain.EnforceSeenLines = true
				chainInput, _ := json.Marshal(map[string]string{"input": "[destination#" + landed.Hash + "]\nPUT 1:\n+LANDED"})
				chained, chainErr := chain.Execute(context.Background(), chainInput)
				if chainErr != nil || len(chained.Files) != 1 || string(fs.files[destination]) != "LANDED\n" {
					t.Fatalf("immediate seen-enforced edit selected wrong version: result=%+v err=%v files=%q", chained, chainErr, fs.files)
				}
			})
		}
	}
}

func round4ToolCollisionFixture(t *testing.T) (string, string) {
	t.Helper()
	seen := map[string]string{}
	for i := 0; i < 200000; i++ {
		text := fmt.Sprintf("round4-collision-%d", i)
		h := hashline.ComputeFileHash(text + "\n")
		if prior, ok := seen[h]; ok {
			return prior, text
		}
		seen[h] = text
	}
	t.Fatal("deterministic collision search exhausted")
	return "", ""
}

type round4CreateFS struct {
	mu          sync.Mutex
	files       map[string][]byte
	readErr     error
	publishHook func(string)
	creates     int
}

func (f *round4CreateFS) ReadFile(path string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return nil, f.readErr
	}
	b, ok := f.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), b...), nil
}
func (f *round4CreateFS) WriteFile(path string, data []byte, _ os.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[path] = append([]byte(nil), data...)
	return nil
}
func (f *round4CreateFS) CreateExclusive(path string, data []byte, _ os.FileMode) error {
	f.mu.Lock()
	f.creates++
	hook := f.publishHook
	f.mu.Unlock()
	if hook != nil {
		hook(path)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.files[path]; exists {
		return os.ErrExist
	}
	f.files[path] = append([]byte(nil), data...)
	return nil
}

func round4CreateInput(mode editmode.Mode, path, text string) json.RawMessage {
	if mode == editmode.Patch {
		body, _ := json.Marshal(map[string]any{"path": path, "edits": []map[string]string{{"op": "create", "diff": text}}})
		return body
	}
	body, _ := json.Marshal(map[string]string{"input": "*** Begin Patch\n*** Add File: " + path + "\n+" + strings.TrimSuffix(text, "\n") + "\n*** End Patch"})
	return body
}

func TestCreateDoesNotOverwriteOnReadErrorOrRace(t *testing.T) {
	for _, mode := range []editmode.Mode{editmode.Patch, editmode.ApplyPatch} {
		t.Run(string(mode)+"/read-error", func(t *testing.T) {
			root := filepath.Clean("/workspace")
			path := filepath.Join(root, "created.txt")
			fs := &round4CreateFS{files: map[string][]byte{path: []byte("winner\n")}, readErr: errors.New("permission denied")}
			snaps := hashline.NewMemSnapshotStore()
			edit := NewEditTool(root, fs, snaps)
			edit.Mode = mode
			result, err := edit.Execute(context.Background(), round4CreateInput(mode, "created.txt", "loser\n"))
			if err == nil || string(fs.files[path]) != "winner\n" || fs.creates != 0 || snaps.Head(path) != nil || len(result.Files) != 1 || result.Files[0].Committed {
				t.Fatalf("result=%+v err=%v files=%q creates=%d head=%+v", result, err, fs.files, fs.creates, snaps.Head(path))
			}
		})
		t.Run(string(mode)+"/external-creator-at-publish", func(t *testing.T) {
			root := filepath.Clean("/workspace")
			path := filepath.Join(root, "created.txt")
			fs := &round4CreateFS{files: map[string][]byte{}}
			fs.publishHook = func(p string) {
				fs.mu.Lock()
				fs.files[p] = []byte("external winner\n")
				fs.publishHook = nil
				fs.mu.Unlock()
			}
			snaps := hashline.NewMemSnapshotStore()
			edit := NewEditTool(root, fs, snaps)
			edit.Mode = mode
			result, err := edit.Execute(context.Background(), round4CreateInput(mode, "created.txt", "tool loser\n"))
			if err == nil || string(fs.files[path]) != "external winner\n" || snaps.Head(path) != nil || len(result.Files) != 1 || result.Files[0].Committed {
				t.Fatalf("result=%+v err=%v bytes=%q head=%+v", result, err, fs.files[path], snaps.Head(path))
			}
		})
	}

	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "target")
			alias := filepath.Join(root, "alias")
			if err := os.WriteFile(target, []byte("protected\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			var err error
			if kind == "symlink" {
				err = os.Symlink(target, alias)
			} else {
				err = os.Link(target, alias)
			}
			if err != nil {
				t.Fatal(err)
			}
			edit := NewEditTool(root, hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
			edit.Mode = editmode.ApplyPatch
			result, execErr := edit.Execute(context.Background(), round4CreateInput(editmode.ApplyPatch, "alias", "replacement\n"))
			got, _ := os.ReadFile(target)
			if execErr == nil || string(got) != "protected\n" || len(result.Files) != 1 || result.Files[0].Committed {
				t.Fatalf("result=%+v err=%v target=%q", result, execErr, got)
			}
		})
	}

	for _, mode := range []editmode.Mode{editmode.Patch, editmode.ApplyPatch} {
		t.Run(string(mode)+"/two-concurrent-creators", func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "created.txt")
			start := make(chan struct{})
			type outcome struct {
				result Result
				err    error
			}
			out := make(chan outcome, 2)
			for _, text := range []string{"first\n", "second\n"} {
				go func(text string) {
					edit := NewEditTool(root, hashline.OSFilesystem{}, hashline.NewMemSnapshotStore())
					edit.Mode = mode
					<-start
					r, err := edit.Execute(context.Background(), round4CreateInput(mode, "created.txt", text))
					out <- outcome{r, err}
				}(text)
			}
			close(start)
			a, b := <-out, <-out
			winner, readErr := os.ReadFile(path)
			if readErr != nil || string(winner) != "first\n" && string(winner) != "second\n" {
				t.Fatalf("winner=%q err=%v", winner, readErr)
			}
			successes := 0
			for _, got := range []outcome{a, b} {
				if got.err == nil {
					successes++
				} else if len(got.result.Files) != 1 || got.result.Files[0].Committed {
					t.Fatalf("loser=%+v err=%v", got.result, got.err)
				}
			}
			if successes != 1 {
				t.Fatalf("outcomes: a=%+v/%v b=%+v/%v", a.result, a.err, b.result, b.err)
			}
		})
	}
}

func TestPatchCreateRejectsSymlinkAndHardlinkAliases(t *testing.T) {
	for _, mode := range []editmode.Mode{editmode.Patch, editmode.ApplyPatch} {
		for _, kind := range []string{"symlink-target", "hardlink-target", "symlinked-parent", "existing-symlink-alias"} {
			t.Run(string(mode)+"/"+kind, func(t *testing.T) {
				root := t.TempDir()
				target := filepath.Join(root, "protected")
				if err := os.WriteFile(target, []byte("protected\n"), 0o640); err != nil {
					t.Fatal(err)
				}
				before, err := os.Stat(target)
				if err != nil {
					t.Fatal(err)
				}
				rel := "alias"
				switch kind {
				case "symlink-target", "existing-symlink-alias":
					if err := os.Symlink(target, filepath.Join(root, rel)); err != nil {
						t.Fatal(err)
					}
				case "hardlink-target":
					if err := os.Link(target, filepath.Join(root, rel)); err != nil {
						t.Fatal(err)
					}
				case "symlinked-parent":
					realParent := filepath.Join(root, "real-parent")
					if err := os.Mkdir(realParent, 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(realParent, filepath.Join(root, "alias-parent")); err != nil {
						t.Fatal(err)
					}
					rel = filepath.Join("alias-parent", "created")
				}
				snaps := hashline.NewMemSnapshotStore()
				edit := NewEditTool(root, hashline.OSFilesystem{}, snaps)
				edit.Mode = mode
				result, execErr := edit.Execute(context.Background(), round4CreateInput(mode, rel, "replacement\n"))
				if execErr == nil || len(result.Files) > 0 && result.Files[0].Committed {
					t.Fatalf("expected structured uncommitted error: result=%+v err=%v", result, execErr)
				}
				after, statErr := os.Stat(target)
				got, readErr := os.ReadFile(target)
				if statErr != nil || readErr != nil || string(got) != "protected\n" || !os.SameFile(before, after) {
					t.Fatalf("target changed: before=%+v after=%+v bytes=%q statErr=%v readErr=%v", before, after, got, statErr, readErr)
				}
				if snaps.Head(filepath.Join(root, rel)) != nil {
					t.Fatalf("snapshot retained for rejected create")
				}
				entries, err := os.ReadDir(root)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".atenea-") {
						t.Fatalf("temporary file leaked: %s", entry.Name())
					}
				}
			})
		}
	}
}

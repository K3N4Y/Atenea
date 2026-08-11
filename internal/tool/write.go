package tool

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/K3N4Y/atenea/agentcore/permission"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

// FileWriter defines the filesystem operations write needs: creating parent
// directories and publishing a new file without replacing an existing path.
// There is deliberately no plain write: this tool only ever creates, and an
// operation that could truncate an existing file would be a way to lose one.
// The default wraps os; tests inject fakes to verify writes without touching disk.
type FileWriter interface {
	MkdirAll(path string, perm os.FileMode) error
	// CreateExclusive publishes data at name atomically and durably, and fails
	// with os.ErrExist if name already exists.
	CreateExclusive(name string, data []byte, perm os.FileMode) error
}

// osWriteFS is the default FileWriter: it creates directories and publishes
// files to disk, reusing the durable publish the hashline patcher already owns.
type osWriteFS struct{}

func (osWriteFS) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }
func (osWriteFS) CreateExclusive(name string, data []byte, perm os.FileMode) error {
	return hashline.OSFilesystem{}.CreateExclusive(name, data, perm)
}

// WriteTool creates a new file under Root with the complete given content.
// It handles NEW files: hashline edit anchors to an existing file (after reading
// it), so write creates files that do not exist.
// It shares the SnapshotStore with read/edit: after writing, it records a content
// snapshot and marks all its lines as seen, allowing the model to chain an edit
// without reading again.
type WriteTool struct {
	Root             string
	FS               FileWriter
	Snapshots        hashline.SnapshotStore
	SnapshotProvider SnapshotProvider
}

// NewWriteTool builds a WriteTool with the default disk filesystem. It receives
// the same Root and SnapshotStore as read/edit so a later edit sees the snapshot
// of the written file.
func NewWriteTool(root string, snaps hashline.SnapshotStore) *WriteTool {
	return &WriteTool{Root: root, FS: osWriteFS{}, Snapshots: snaps}
}

func NewWriteToolWithSnapshotProvider(root string, provider SnapshotProvider) *WriteTool {
	return &WriteTool{Root: root, FS: osWriteFS{}, SnapshotProvider: provider}
}

func (*WriteTool) Name() string { return "write" }

//go:embed write.txt
var writeDescription string

func (*WriteTool) Description() string { return writeDescription }

// Effects: the tool exists to put a file on disk under the workspace root.
func (*WriteTool) Effects() Effects { return WritesFiles }

// GrantRule grants the tool itself. What approving a write for the session
// authorizes is "create files under this workspace", which is exactly the subject
// the permission panel names — there is no narrower honest shape, since the next
// write will name a different path and a path prefix is not what the user was
// shown.
func (wt *WriteTool) GrantRule(Call) (permission.Rule, bool) {
	return permission.Rule{Tool: wt.Name()}, true
}

func (*WriteTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1,"description":"Path for the new workspace file."},"content":{"type":"string","description":"Complete file content."}},"required":["path","content"],"additionalProperties":false}`)
}

// AutoAcceptSafe proves this remains the write tool's new-file-only operation.
func (wt *WriteTool) AutoAcceptSafe(call Call) bool {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if json.Unmarshal(call.Input, &in) != nil || in.Path == "" {
		return false
	}
	abs, err := sandboxJoin(wt.Root, in.Path, "write")
	if err != nil {
		return false
	}
	if _, ok := wt.FS.(osWriteFS); !ok {
		return false
	}
	if rejectRealParentOutside(wt.Root, abs, in.Path, "write") != nil {
		return false
	}
	_, err = os.Lstat(abs)
	return os.IsNotExist(err)
}

// Execute parses {path, content}, resolves the path within Root (using the same
// fail-closed sandbox gate as read/edit), normalizes content to LF, creates parent
// directories, writes the file, and records the snapshot with every line marked
// as seen. It returns the [path#HASH] header with the RELATIVE path (which the
// model uses in the next edit).
func (wt *WriteTool) Execute(ctx context.Context, input json.RawMessage) (Result, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{}, fmt.Errorf("write: invalid input: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	abs, err := sandboxJoin(wt.Root, in.Path, "write")
	if err != nil {
		return Result{}, err
	}
	if _, ok := wt.FS.(osWriteFS); ok {
		if err := rejectRealParentOutside(wt.Root, abs, in.Path, "write"); err != nil {
			return Result{}, err
		}
	}

	// Normalize like read/edit: remove an initial BOM and unify line endings as LF,
	// so the snapshot hash matches one computed by a later read.
	norm := strings.TrimPrefix(in.Content, string(rune(0xFEFF)))
	norm = strings.ReplaceAll(norm, "\r\n", "\n")
	norm = strings.ReplaceAll(norm, "\r", "\n")

	if err := wt.FS.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return Result{}, err
	}
	// Creating the file is what makes this call refuse to replace one: the OS
	// decides exclusivity atomically, so a file appearing between here and the
	// previous statement loses the race instead of being silently overwritten.
	if err := wt.FS.CreateExclusive(abs, []byte(norm), 0o644); err != nil {
		if errors.Is(err, os.ErrExist) {
			return Result{}, fmt.Errorf("write: file already exists; use read+edit: %s", in.Path)
		}
		return Result{}, err
	}

	// Record the newly written file's snapshot and mark ALL its lines as seen: the
	// model authored them, so a later edit can anchor without reading again.
	snaps := wt.snapshots(ctx)
	tag, recorded := snaps.Record(abs, norm)
	lines := hashline.SplitLines(norm)
	seen := make([]int, 0, len(lines))
	for i := 1; i <= len(lines); i++ {
		seen = append(seen, i)
	}
	if recorded {
		snaps.RecordSeenContent(abs, norm, seen)
	}

	// Diff ONLY for the UI: a new file is entirely additions (empty old content),
	// using the relative path that the model chains into the next operation.
	diff := hashline.UnifiedDiff(in.Path, "", norm, 3)
	if !recorded {
		return Result{Output: "[File " + in.Path + " was created, but its snapshot could not be retained safely; no hashline header was issued. To make it editable, change or reduce the content or start a new session, then use read.]", Diff: diff}, nil
	}
	return Result{Output: hashline.FormatHeader(in.Path, tag), Diff: diff}, nil
}

func (wt *WriteTool) snapshots(ctx context.Context) hashline.SnapshotStore {
	if wt.SnapshotProvider != nil {
		return wt.SnapshotProvider.Snapshots(ctx)
	}
	return wt.Snapshots
}

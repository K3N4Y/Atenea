package tool

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

// FileReader reads a file's contents by path. It is the read tool's only FS
// dependency, allowing tests to inject content without touching the disk.
type FileReader interface {
	ReadFile(name string) ([]byte, error)
}

// osFS is the default FileReader and delegates to os.ReadFile.
type osFS struct{}

func (osFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }

// ReadTool reads a file under Root, numbers it with a [path#HASH] header, and
// records the snapshot so edit can anchor against the viewed lines. MaxLines is
// the maximum number of lines per read; output beyond it is truncated and a
// continuation notice with the :N selector is appended to read the rest.
type ReadTool struct {
	Root             string
	FS               FileReader
	Snapshots        hashline.SnapshotStore
	SnapshotProvider SnapshotProvider
	MaxLines         int
	MaxBytes         int
}

const defaultReadMaxBytes = 30 * 1024

// NewReadTool creates a ReadTool with the default disk FS and standard line
// limit.
func NewReadTool(root string, snaps hashline.SnapshotStore) *ReadTool {
	return &ReadTool{Root: root, FS: osFS{}, Snapshots: snaps, MaxLines: 2000, MaxBytes: defaultReadMaxBytes}
}

func NewReadToolWithSnapshotProvider(root string, provider SnapshotProvider) *ReadTool {
	return &ReadTool{Root: root, FS: osFS{}, SnapshotProvider: provider, MaxLines: 2000, MaxBytes: defaultReadMaxBytes}
}

func (*ReadTool) Name() string { return "read" }

//go:embed read.txt
var readDescription string

func (*ReadTool) Description() string { return readDescription }

// Effects: none. Reading is not an effect — the file is left as it was, and the
// snapshot the read records is the agent's own state.
func (*ReadTool) Effects() Effects { return NoEffects }

func (*ReadTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1,"description":"Workspace file path. Append :N or :N-M to read an inclusive 1-indexed line range."}},"required":["path"],"additionalProperties":false}`)
}

// Execute parses the input (a path with an embedded selector), resolves the path
// within Root (the sandbox gate), reads and normalizes it, records the complete
// file snapshot, and emits the requested numbered window under the hashline
// header, marking exactly the emitted lines as seen.
func (rt *ReadTool) Execute(ctx context.Context, input json.RawMessage) (Result, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{}, fmt.Errorf("read: invalid input: %w", err)
	}

	// Embedded selector: if the path contains ':', split it at the LAST ':'; the
	// prefix is the path (= display path) and the suffix is the selector. v1
	// limitation: filenames containing ':' are not supported.
	displayPath := in.Path
	hasSel := false
	var fromSel, toSel int // 0 = no selector
	if i := strings.LastIndex(in.Path, ":"); i >= 0 {
		displayPath = in.Path[:i]
		from, to, err := parseSelector(in.Path[i+1:])
		if err != nil {
			return Result{}, err
		}
		hasSel = true
		fromSel, toSel = from, to
	}

	abs, err := sandboxJoin(rt.Root, displayPath, "read")
	if err != nil {
		return Result{}, err
	}
	if _, ok := rt.FS.(osFS); ok {
		if err := rejectRealPathOutside(rt.Root, abs, displayPath, "read"); err != nil {
			return Result{}, err
		}
	}

	b, err := rt.FS.ReadFile(abs)
	if err != nil {
		return Result{}, err
	}

	// Binary: a NUL byte produces a notice and no snapshot (not editable via hashline).
	for _, by := range b {
		if by == 0 {
			notice := "[Cannot read binary file " + displayPath + "; content contains NUL bytes (binary or UTF-16)]"
			return Result{Output: notice}, nil
		}
	}

	// Normalize: remove an initial UTF-8 BOM if present and unify line endings.
	norm := strings.TrimPrefix(string(b), "\uFEFF")
	norm = strings.ReplaceAll(norm, "\r\n", "\n")
	norm = strings.ReplaceAll(norm, "\r", "\n")

	// The snapshot ALWAYS stores the complete file, even for a ranged read.
	snaps := rt.snapshots(ctx)
	tag, recorded := snaps.Record(abs, norm)
	if !recorded {
		return Result{Output: "[File " + displayPath + " could not be retained as an unambiguous editable snapshot; no hashline header was issued. Change or reduce the content or start a new session, then use read again before editing.]"}, nil
	}

	lines := hashline.SplitLines(norm)
	total := len(lines)

	// Choose the window [from..to] (1-indexed over the file) and the truncation
	// notice if the line limit shortens it.
	var from, to int
	var truncNotice string
	if hasSel {
		if fromSel > total {
			notice := "Line " + strconv.Itoa(fromSel) + " is beyond end of file (" + strconv.Itoa(total) + " lines total)."
			return Result{Output: notice}, nil
		}
		from, to = fromSel, toSel
		if to > total {
			to = total // silently clamp an end beyond the total.
		}
	} else {
		from = 1
		to = total
		if rt.MaxLines > 0 && total > rt.MaxLines {
			to = rt.MaxLines
			remaining := total - to
			truncNotice = "\n\n[" + strconv.Itoa(remaining) + " more lines in file. Use :" + strconv.Itoa(to+1) + " to continue]"
		}
	}

	header := hashline.FormatHeader(displayPath, tag)
	to, truncNotice = rt.capWindow(lines, from, to, total, header, truncNotice)

	body := ""
	if to >= from {
		body = hashline.NumberLines(lines, from, to)
	}
	output := header + "\n" + body + truncNotice

	seen := make([]int, 0, max(0, to-from+1))
	for i := from; i <= to; i++ {
		seen = append(seen, i)
	}
	snaps.RecordSeenLines(abs, tag, seen)

	return Result{Output: output}, nil
}

func (rt *ReadTool) snapshots(ctx context.Context) hashline.SnapshotStore {
	if rt.SnapshotProvider != nil {
		return rt.SnapshotProvider.Snapshots(ctx)
	}
	return rt.Snapshots
}

func (rt *ReadTool) capWindow(lines []string, from, to, total int, header, notice string) (int, string) {
	if rt.MaxBytes <= 0 || to < from {
		return to, notice
	}
	for cappedTo := to; cappedTo >= from; cappedTo-- {
		body := hashline.NumberLines(lines, from, cappedTo)
		if len(header+"\n"+body) <= rt.MaxBytes {
			if cappedTo < to {
				return cappedTo, continuationNotice(total-cappedTo, cappedTo+1)
			}
			return to, notice
		}
	}
	return from - 1, continuationNotice(total-from+1, from)
}

func continuationNotice(remaining, next int) string {
	return "\n\n[" + strconv.Itoa(remaining) + " more lines in file. Use :" + strconv.Itoa(next) + " to continue]"
}

// parseSelector interprets the path suffix: "N" (one line) or "N-M" (a range),
// integers where 1 <= N and, for a range, N <= M. Any other form is an
// actionable tool error. It returns 1-indexed [from, to] (for "N", from == to).
func parseSelector(sel string) (from, to int, err error) {
	invalid := func() (int, int, error) {
		return 0, 0, fmt.Errorf("read: invalid selector: %s", sel)
	}
	if i := strings.IndexByte(sel, '-'); i >= 0 {
		n, err1 := strconv.Atoi(sel[:i])
		m, err2 := strconv.Atoi(sel[i+1:])
		if err1 != nil || err2 != nil || n < 1 || m < n {
			return invalid()
		}
		return n, m, nil
	}
	n, err1 := strconv.Atoi(sel)
	if err1 != nil || n < 1 {
		return invalid()
	}
	return n, n, nil
}

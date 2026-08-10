package tool

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

// FileReader reads a file's contents by path. It remains the read tool's
// compatibility seam for tests and older integrations.
type FileReader interface {
	ReadFile(name string) ([]byte, error)
}

type fileOpener interface {
	Open(name string) (io.ReadCloser, error)
}

// osFS is the default FileReader and delegates reads to the operating system.
// Open is used by ReadTool when it is available so a read can stay incremental.
type osFS struct{}

func (osFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }
func (osFS) Open(name string) (io.ReadCloser, error) {
	return os.Open(name)
}

// ReadTool reads a file under Root, numbers it with a [path#HASH] header, and
// records a bounded snapshot so edit can anchor against the viewed lines.
//
// MaxLines and MaxBytes are retained for in-package callers and tests. They are
// policy limits, not model-facing options; values above the phase-one limits are
// clamped and non-positive values use the defaults.
type ReadTool struct {
	Root             string
	FS               FileReader
	Snapshots        hashline.SnapshotStore
	SnapshotProvider SnapshotProvider
	MaxLines         int
	MaxBytes         int
}

const (
	defaultReadMaxLines     = 2000
	defaultReadMaxBytes     = 30 * 1024
	defaultReadMaxLineChars = 2000
	readBufferSize          = 32 * 1024
	maxReadSnapshotBytes    = 12 << 20
)

// NewReadTool creates a ReadTool with the default disk FS and standard policy
// limits.
func NewReadTool(root string, snaps hashline.SnapshotStore) *ReadTool {
	return &ReadTool{Root: root, FS: osFS{}, Snapshots: snaps, MaxLines: defaultReadMaxLines, MaxBytes: defaultReadMaxBytes}
}

func NewReadToolWithSnapshotProvider(root string, provider SnapshotProvider) *ReadTool {
	return &ReadTool{Root: root, FS: osFS{}, SnapshotProvider: provider, MaxLines: defaultReadMaxLines, MaxBytes: defaultReadMaxBytes}
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

// Execute parses the input, resolves the path within Root, and scans the file
// through a bounded buffer. Only the selected window and a bounded snapshot are
// retained; the scanner still counts the complete file so the established
// hashline and offset semantics remain intact.
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
	fs := rt.fileReader()
	if _, ok := fs.(osFS); ok {
		if err := rejectRealPathOutside(rt.Root, abs, displayPath, "read"); err != nil {
			return Result{}, err
		}
	}

	file, err := rt.openFile(fs, abs)
	if err != nil {
		return Result{}, err
	}
	defer file.Close()

	maxLines := rt.readMaxLines()
	maxBytes := rt.readMaxBytes()
	windowFrom, windowTo := 1, maxLines
	if hasSel {
		windowFrom, windowTo = fromSel, limitedWindowEnd(fromSel, toSel, maxLines)
	}
	scan := newReadScanner(displayPath, windowFrom, windowTo, maxLines, maxBytes)
	if err := scan.run(ctx, file); err != nil {
		if errors.Is(err, errBinaryRead) {
			notice := "[Cannot read binary file " + displayPath + "; content contains NUL bytes (binary or UTF-16)]"
			return Result{Output: notice}, nil
		}
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	// The existing edit contract requires a complete retained snapshot before a
	// hashline header is useful. The scanner never lets this buffer grow beyond
	// the snapshot retention bound; oversized files keep the established
	// no-header result instead of pretending a partial hash is editable.
	snaps := rt.snapshots(ctx)
	var (
		tag      string
		recorded bool
		norm     string
	)
	if scan.snapshotRetained {
		norm = string(scan.snapshot)
		tag, recorded = snaps.Record(abs, norm)
	}
	if !recorded {
		return Result{Output: "[File " + displayPath + " could not be retained as an unambiguous editable snapshot; no hashline header was issued. Change or reduce the content or start a new session, then use read again before editing.]"}, nil
	}

	if hasSel && fromSel > scan.total {
		snaps.RecordSeenContent(abs, norm, nil)
		notice := "Line " + strconv.Itoa(fromSel) + " is beyond end of file (" + strconv.Itoa(scan.total) + " lines total)."
		return Result{Output: notice}, nil
	}

	header := hashline.FormatHeader(displayPath, tag)
	output, truncated := scan.output(header, hasSel, fromSel, toSel)

	snaps.RecordSeenContent(abs, norm, scan.seen)
	return Result{Output: output, Truncated: truncated}, nil
}

func (rt *ReadTool) fileReader() FileReader {
	if rt.FS != nil {
		return rt.FS
	}
	return osFS{}
}

func (rt *ReadTool) openFile(fs FileReader, name string) (io.ReadCloser, error) {
	if opener, ok := fs.(fileOpener); ok {
		return opener.Open(name)
	}
	b, err := fs.ReadFile(name)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (rt *ReadTool) snapshots(ctx context.Context) hashline.SnapshotStore {
	if rt.SnapshotProvider != nil {
		return rt.SnapshotProvider.Snapshots(ctx)
	}
	if rt.Snapshots != nil {
		return rt.Snapshots
	}
	return hashline.NewMemSnapshotStore()
}

func (rt *ReadTool) readMaxLines() int {
	limit := rt.MaxLines
	if limit <= 0 {
		return defaultReadMaxLines
	}
	if limit > defaultReadMaxLines {
		return defaultReadMaxLines
	}
	return limit
}

func (rt *ReadTool) readMaxBytes() int {
	limit := rt.MaxBytes
	if limit <= 0 {
		return defaultReadMaxBytes
	}
	if limit > defaultReadMaxBytes {
		return defaultReadMaxBytes
	}
	return limit
}

func limitedWindowEnd(from, to, maxLines int) int {
	if to-from >= maxLines {
		return from + maxLines - 1
	}
	return to
}

var errBinaryRead = errors.New("read: binary content")

type readScanner struct {
	windowFrom int
	windowTo   int
	maxLines   int
	bodyBudget int

	body          []byte
	seen          []int
	displayed     int
	lastDisplayed int
	bodyFull      bool

	lineNo      int
	lineStarted bool
	current     *displayLine
	total       int

	snapshot         []byte
	snapshotRetained bool
}

func newReadScanner(displayPath string, windowFrom, windowTo, maxLines, maxBytes int) *readScanner {
	// Reserve the header and its separating newline. This keeps the complete
	// result within the byte policy for ordinary paths while leaving the line
	// content budget stricter than the public cap.
	headerLen := len(hashline.FormatHeader(displayPath, "0000"))
	bodyBudget := maxBytes - headerLen - 1
	if bodyBudget < 0 {
		bodyBudget = 0
	}
	s := &readScanner{
		windowFrom:       windowFrom,
		windowTo:         windowTo,
		maxLines:         maxLines,
		bodyBudget:       bodyBudget,
		lineNo:           1,
		snapshotRetained: true,
	}
	s.startLine()
	return s
}

// run normalizes raw line endings while reading through a fixed-size buffer.
// A CR is held for one byte so both CRLF and standalone CR become one LF.
func (s *readScanner) run(ctx context.Context, r io.Reader) error {
	var prefix [3]byte
	n, err := io.ReadFull(r, prefix[:])
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return err
	}
	initial := prefix[:n]
	if n == len(prefix) && bytes.Equal(initial, []byte{0xEF, 0xBB, 0xBF}) {
		initial = nil
	}

	pendingCR := false
	processRaw := func(b byte) error {
		if pendingCR {
			pendingCR = false
			if b == '\n' {
				if err := s.consume('\n'); err != nil {
					return err
				}
				return nil
			}
			if err := s.consume('\n'); err != nil {
				return err
			}
		}
		if b == '\r' {
			pendingCR = true
			return nil
		}
		return s.consume(b)
	}

	for _, b := range initial {
		if err := processRaw(b); err != nil {
			return err
		}
	}

	buf := make([]byte, readBufferSize)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := r.Read(buf)
		for i := 0; i < n; i++ {
			if processErr := processRaw(buf[i]); processErr != nil {
				return processErr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
	}
	if pendingCR {
		if err := s.consume('\n'); err != nil {
			return err
		}
	}
	if s.lineStarted {
		s.finishLine()
	}
	return nil
}

func (s *readScanner) consume(b byte) error {
	if b == 0 {
		return errBinaryRead
	}
	if s.snapshotRetained {
		if len(s.snapshot) == maxReadSnapshotBytes {
			// Drop the partial capture as soon as retention is impossible. The
			// displayed window remains independently bounded.
			s.snapshot = nil
			s.snapshotRetained = false
		} else {
			s.snapshot = append(s.snapshot, b)
		}
	}
	if b == '\n' {
		s.finishLine()
		return nil
	}
	s.lineStarted = true
	if s.current != nil {
		s.current.writeByte(b)
	}
	return nil
}

func (s *readScanner) startLine() {
	if !s.bodyFull && s.displayed < s.maxLines &&
		s.lineNo >= s.windowFrom && s.lineNo <= s.windowTo {
		s.current = &displayLine{exact: true}
	}
}

func (s *readScanner) finishLine() {
	s.total++
	if s.current != nil {
		s.current.finish()
		s.appendLine(s.lineNo, s.current.bytes, s.current.exact && !s.current.truncated)
	}
	s.lineNo++
	s.lineStarted = false
	s.current = nil
	s.startLine()
}

func (s *readScanner) appendLine(lineNo int, text []byte, complete bool) {
	if s.bodyFull || s.displayed >= s.maxLines {
		return
	}
	prefix := strconv.Itoa(lineNo) + ":"
	separator := 0
	if len(s.body) > 0 {
		separator = 1
	}
	required := separator + len(prefix) + len(text)
	if required > s.bodyBudget-len(s.body) {
		s.bodyFull = true
		return
	}
	if separator != 0 {
		s.body = append(s.body, '\n')
	}
	s.body = append(s.body, prefix...)
	s.body = append(s.body, text...)
	s.displayed++
	s.lastDisplayed = lineNo
	if complete {
		s.seen = append(s.seen, lineNo)
	}
}

func (s *readScanner) output(header string, hasSel bool, fromSel, toSel int) (string, bool) {
	out := make([]byte, 0, len(header)+1+len(s.body))
	out = append(out, header...)
	out = append(out, '\n')
	out = append(out, s.body...)

	// The notice is appended even when it pushes the result a few dozen bytes
	// past the cap: without it a body that happens to end near the boundary
	// reads as a complete file, and the model stops instead of continuing.
	notice := s.continuationNotice(hasSel, fromSel, toSel)
	out = append(out, notice...)
	return string(out), notice != ""
}

func (s *readScanner) continuationNotice(hasSel bool, fromSel, toSel int) string {
	if s.total == 0 {
		return ""
	}
	start := 1
	requestedEnd := s.total
	if hasSel {
		start = fromSel
		requestedEnd = toSel
		if requestedEnd > s.total {
			requestedEnd = s.total
		}
	}
	if start > requestedEnd {
		return ""
	}
	limitedEnd := limitedWindowEnd(start, requestedEnd, s.maxLines)
	if s.lastDisplayed >= limitedEnd && limitedEnd >= requestedEnd {
		return ""
	}
	next := s.lastDisplayed + 1
	if next < start {
		next = start
	}
	remaining := s.total - s.lastDisplayed
	if remaining < 1 {
		return ""
	}
	return continuationNotice(remaining, next)
}

func continuationNotice(remaining, next int) string {
	return "\n\n[" + strconv.Itoa(remaining) + " more lines in file. Use :" + strconv.Itoa(next) + " to continue]"
}

// displayLine keeps at most the configured number of Unicode code points.
// It assembles one rune at a time instead of converting an arbitrarily long
// source line to []rune, so minified files remain bounded.
type displayLine struct {
	bytes     []byte
	pending   []byte
	expected  int
	chars     int
	exact     bool
	truncated bool
}

func (l *displayLine) writeByte(b byte) {
	if l.truncated {
		return
	}
	if len(l.pending) == 0 {
		l.expected = utf8Width(b)
		l.pending = append(l.pending, b)
		if l.expected == 1 {
			l.emitPending()
		}
		return
	}
	if !isUTF8Continuation(b) {
		l.emitReplacement()
		l.pending = l.pending[:0]
		l.expected = 0
		l.writeByte(b)
		return
	}
	l.pending = append(l.pending, b)
	if len(l.pending) == l.expected {
		if utf8.Valid(l.pending) {
			l.emit(l.pending)
		} else {
			l.emitReplacement()
		}
		l.pending = l.pending[:0]
		l.expected = 0
	}
}

func (l *displayLine) finish() {
	if len(l.pending) != 0 {
		l.emitReplacement()
		l.pending = l.pending[:0]
		l.expected = 0
	}
}

func (l *displayLine) emitPending() {
	if utf8.Valid(l.pending) {
		l.emit(l.pending)
	} else {
		l.emitReplacement()
	}
	l.pending = l.pending[:0]
	l.expected = 0
}

func (l *displayLine) emitReplacement() {
	l.exact = false
	l.emit([]byte{0xEF, 0xBF, 0xBD})
}

func (l *displayLine) emit(r []byte) {
	if l.chars >= defaultReadMaxLineChars {
		l.truncated = true
		return
	}
	l.bytes = append(l.bytes, r...)
	l.chars++
}

func utf8Width(b byte) int {
	switch {
	case b < 0x80:
		return 1
	case b >= 0xC2 && b <= 0xDF:
		return 2
	case b >= 0xE0 && b <= 0xEF:
		return 3
	case b >= 0xF0 && b <= 0xF4:
		return 4
	default:
		return 1
	}
}

func isUTF8Continuation(b byte) bool {
	return b&0xC0 == 0x80
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

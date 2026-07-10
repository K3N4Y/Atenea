---
updated_at: 2026-07-09
summary: Implementation plan for the read-only TUI file viewer.
---

# TUI File Viewer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a read-only TUI viewer, opened from the explorer, that replaces the chat and maintains explorer, cursor and scroll.

**Architecture:** `fileViewer` is a pure component that sorts, highlights and pages a file. `Model` maintains focus and delegates the keyboard to you after the permissions/plan gates. An injectable `FileReader` reads only under the root of the workspace.

**Tech Stack:** Go 1.25, Bubble Tea, Lip Gloss, Chroma v2, Charm ANSI and creack/pty.

---

## Execution rules

- Use the `Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence` cycle in each task.
- Execute from dedicated worktree; a new branch uses `posthog-code/`.
- Each commit executes `git commit -m '<titulo>' -m 'Generated-By: PostHog Code' -m 'Task-Id: 1e67e8ab-2799-4404-b66f-0d73583f7166'`.
- Do not introduce editing, tabs, search, automatic reload, Wails changes or file writing actions.

## File Map

- Create `internal/tui/file_viewer.go` and `internal/tui/file_viewer_test.go` for the pure core.
- Modify `go.mod`, `go.sum` to import Chroma directly.
- Modify `internal/tui/model.go`, `internal/tui/model_test.go` for focus and keyboard.
- Modify `internal/tui/view.go` for the alternative layout.
- Modify `cmd/atenea-tui/main.go` and create `cmd/atenea-tui/main_test.go` plus `cmd/atenea-tui/testdata/file-viewer/project/hello.go` for wiring/E2E.
- Modify `../architecture/tui.md` and `../specs/2026-07-09-tui-file-viewer.md` for documentation and evidence.

## Base API

```go
const maxFileViewerBytes = 1 << 20
var ErrFileViewerBinary = errors.New("archivo binario")
var ErrFileViewerTooLarge = errors.New("archivo demasiado grande")
type FileReader func(path string) ([]byte, error)
func WorkspaceFileReader(root string) FileReader
type fileViewer struct { path string; lines []string; offset int; message string; empty bool; lineCount int }
func openFileViewer(path string, content []byte) fileViewer
func openFileViewerError(path string, err error) fileViewer
func (v fileViewer) active() bool
func (v fileViewer) visibleRange(height int) (int, int)
func (v *fileViewer) scroll(delta, height int)
func (v *fileViewer) clamp(height int)
func (v fileViewer) header(width, height int) string
func (v fileViewer) render(width, height int) string
```

### Task 1: Reading core, classification and Chroma

**Files:** `internal/tui/file_viewer.go`, `internal/tui/file_viewer_test.go`, `go.mod`, `go.sum`.

- [ ] **Step 1: Safety net.** Run `go test ./internal/tui`; expected PASS.
- [ ] **Step 2: RED.** Create `file_viewer_test.go` with these cases: `TestOpenFileViewer_NormalizesCRLFAndNumbersLines`, `TestOpenFileViewer_EmptyBinaryAndLargeStates`, `TestOpenFileViewer_UsesLexerAndPlainFallback`, `TestWorkspaceFileReader_ReadsRelativePathAndRejectsEscape`.
- [ ] **Step 3: Verify RED.** Run `go test -run 'Test(OpenFileViewer|WorkspaceFileReader)' -v ./internal/tui`; expected failure due to non-existent base API symbols.
- [ ] **Step 4: GREEN.** Implement `WorkspaceFileReader` with `filepath.Abs`, `filepath.Clean(filepath.FromSlash(path))`, rejection of absolute, `.`, `..` and prefix `../`, plus final validation `filepath.Rel` before `os.ReadFile`. `openFileViewer` rejects `> 1<<20` bytes, NUL, normalizes CRLF, preserves a line for `"\n"`, and marks zero bytes as empty. `openFileViewerError` uses exactly `archivo binario: <path>`, `archivo demasiado grande (> 1 MiB): <path>`, and `no se puede abrir <path>: <error>`.
- [ ] **Step 5: Chroma.** Import `formatters`, `lexers`, and `styles` from `github.com/alecthomas/chroma/v2`; `highlightFile` does `lexers.Match(path)`, fallback `lexers.Text`, `Tokenise`, `formatters.TTY16m.Format(..., styles.Monokai, ...)` and returns to plain text if there is an error or the lines change. Run `go mod tidy`.
- [ ] **Step 6: GREEN and triangulation.** Add asserts from `lineCount == 2` to `one\ntwo\n` and `lineCount == 1` to `\n`. Run `go test ./internal/tui`; expected PASS.
- [ ] **Step 7: Commit.** Run `gofmt -w internal/tui/file_viewer.go internal/tui/file_viewer_test.go && go test ./internal/tui`; commit `feat(tui): add safe file viewer content loader`.

### Task 2: Viewport and secure ANSI rendering

**Files:** `internal/tui/file_viewer.go`, `internal/tui/file_viewer_test.go`.

- [ ] **Step 1: Safety net.** Run `go test ./internal/tui`; expected PASS.
- [ ] **Step 2: RED.** Add `TestFileViewer_ScrollAndResizeClamp` with six lines: scroll `99` to high `3` ends offset `3`, scroll `-99` ends `0`, clamp offset `99` to high `10` ends `0`, clamp with high `0` ends `0`. Add `TestFileViewer_RenderShowsRangeAndNeverOverflows`: offset `2`, height `2`, header `many.go · 3-4/5`, contains 3/4 lines and `ansi.StringWidth` never exceeds `0`, `1`, `4` or `12`.
- [ ] **Step 3: Verify RED.** Run `go test -run 'TestFileViewer_(Scroll|Render)' -v ./internal/tui`; expected FAIL.
- [ ] **Step 4: GREEN.** Implement `active`, `visibleRange`, `clamp` and `scroll` with the `[0,max(lineCount-height,0)]` limits. `header` uses `ansi.Truncate`; for content it returns `path · first-last/total`, and for message/empty it returns only truncated path. `render` computes gutter with `len(strconv.Itoa(lineCount))`, displays the range, and truncates each trailing row with `ansi.Truncate(gutter+lines[index], max(width,0), "…")`.
- [ ] **Step 5: Gate.** Run `gofmt -w internal/tui/file_viewer.go internal/tui/file_viewer_test.go && go test -run 'TestFileViewer_(Scroll|Render)' -v ./internal/tui && go test ./internal/tui`; expected PASS. Confirm that ANSI strings are not cut with slices nor are Chroma invoked during scroll.
- [ ] **Step 6: Commit.** Commit `feat(tui): render scrollable highlighted file content`.

### Task 3: Model status, explorer and priorities

**Files:** `internal/tui/model.go`, `internal/tui/model_test.go`.

- [ ] **Step 1: Safety net.** Run `go test ./internal/tui`; expected PASS.
- [ ] **Step 2: NETWORK.** Add `viewerReader(map[string][]byte) FileReader` which returns `fs.ErrNotExist` for missing paths. Add: `TestModel_TreeEnterFileOpensViewerWithoutMention` (Enter leaves `viewer.active`, `treeOpen` and composer empty); `TestModel_FileViewerEscapePreservesExplorerCursor`; `TestModel_FileViewerScrollCapturesKeysButPermissionWins` (Down changes offset; after `EventMsg{Kind: session.KindToolPermissionRequested}` Down does not change it); and a read error test that shows `no se puede abrir gone.go` and preserves tree after Esc.
- [ ] **Step 3: Verify RED.** Run `go test -run 'TestModel_(TreeEnterFile|FileViewer)' -v ./internal/tui`; expected FAIL.
- [ ] **Step 4: GREEN.** Add `fileReader FileReader` and `viewer fileViewer` next to the tree state. Add builder `WithFileReader(read FileReader) Model`. Implement `openTreeFile`: nil reader uses `errors.New("lector de archivos no configurado")`; error uses `openFileViewerError`; success uses `openFileViewer` and `clamp`.
- [ ] **Step 5: Key routing.** Implement `fileViewerHeight() int { return max(m.height-1, 0) }` and `handleFileViewerKey`: Clean Esc `viewer`, Down/j shifts 1, Up/k -1, PgDown/PgUp shifts `max(height,1)` and its negative. In `handleTreeKey`, the file branch of Enter/l goes to `m = m.openTreeFile(node.path)` without closing the tree or inserting a mention. In `handleKey`, after Ctrl+C, permission and plan, but before PgUp/PgDn and `treeOpen`, insert `if m.viewer.active() { return m.handleFileViewerKey(msg) }`. In `tea.WindowSizeMsg` do `m.viewer.clamp(m.fileViewerHeight())`.
- [ ] **Step 6: Gate and commit.** Run `gofmt -w internal/tui/model.go internal/tui/model_test.go && go test -run 'TestModel_(TreeEnterFile|FileViewer)' -v ./internal/tui && go test ./internal/tui`; expected PASS. Verify that normal Esc continues to stop run outside the viewer and that it only closes the tree without the viewer. Commit `feat(tui): open explorer files in read-only viewer`.

### Task 4: Replace chat layout with viewer

**Files:** `internal/tui/view.go`, `internal/tui/model_test.go`.

- [ ] **Step 1: Safety net.** Run `go test ./internal/tui`; expected PASS.
- [ ] **Step 2: NETWORK.** Add `TestModel_FileViewerReplacesChatWithHeaderAndGutter`: to 80x8 expect `explorer`, `main.go · 1-2/2`, numbers/content and absence of `build ·`. Add `TestModel_FileViewerNarrowTerminalNeverOverflows`: at 12x4 each row meets `ansi.StringWidth <= 12`.
- [ ] **Step 3: Verify RED.** Run `go test -run 'TestModel_FileViewer(Replaces|Narrow)' -v ./internal/tui`; expected FAIL.
- [ ] **Step 4: GREEN.** Add `renderFileViewer(width,height)` which uses `statusStyle.Render(m.viewer.header(width,max(height-1,0)))`, body `m.viewer.render(width,max(height-1,0))` and a jump only if there is a body. At the beginning of `View`, if there is a viewer, render it as the right of the explorer, not as transcript/composer.
- [ ] **Step 5: Reuse layout math.** Extract from the existing branch of the `contentWidth()` and `renderTreeAndContent(left,right string)` tree if they are missing. Chat and viewer use exactly that calculation. `renderTreeAndContent` truncates each row joined with `ansi.Truncate(line,max(m.width,0),"")`; do not use `viewport` for chat or `syncViewport` when viewing file.
- [ ] **Step 6: Gate, inspection and commit.** Run `gofmt -w internal/tui/view.go internal/tui/model_test.go && go test -run 'TestModel_FileViewer(Replaces|Narrow)' -v ./internal/tui && go test ./internal/tui`; expected PASS. Run `go run ./cmd/atenea-tui`; inspect Space+e, Enter in Go, narrow resize, j/k, PgUp/PgDn and Esc: fixed tree, aligned gutter, no composer/status, no bleed colors. Commit `feat(tui): render file viewer in chat area`.

### Task 5: Wiring, E2E PTY and docs

**Files:** `cmd/atenea-tui/main.go`, `cmd/atenea-tui/main_test.go`, `cmd/atenea-tui/testdata/file-viewer/project/hello.go`, `../architecture/tui.md`.

- [ ] **Step 1: Safety net.** Run `go test ./cmd/atenea-tui`; expected PASS.
- [ ] **Step 2: RED fixture/E2E.** Create fixture `hello.go` with three logical lines and the text `hello from file viewer`. Create `TestTUI_FileViewerFlowUnderPTY`: build the binary in `t.TempDir`, `pty.StartWithSize` 100x24 from the fixture, `OPENROUTER_API_KEY=` and `ATENEA_DB` temporary, reader goroutine to `bytes.Buffer`, poll `waitForPTYText` for 3 seconds using `ansi.Strip` every 20ms. Sequence: wait `build · demo`; write `" e\r"`; wait `hello.go`; write Enter; wait `hello.go · 1-3/3` and `hello from file viewer`; Esc; wait `build · demo`; Ctrl+C; wait for departure. Close PTY and wait for process in defer.
- [ ] **Step 3: Verify RED.** Run `go test -run TestTUI_FileViewerFlowUnderPTY -v ./cmd/atenea-tui`; expected FAIL due to lack of reader in wiring or current mention flow.
- [ ] **Step 4: GREEN wiring.** Add to `main.go` builder: `.WithFileReader(tui.WorkspaceFileReader(root))`; root is already the same one used by `Engine` and `ProjectFiles`.
- [ ] **Step 5: GREEN/docs.** Run `go test -run TestTUI_FileViewerFlowUnderPTY -v ./cmd/atenea-tui`; expected PASS. If the tree starts in a folder, add to E2E only the minimum explicit sequence l/Down, without weakening asserts. Document: Enter opens read-only without `@ruta`/close, Esc preserves tree, j/k/Up/Down/PgUp/PgDn scroll, path+numbers+Chroma, and binary states/>1MiB/empty/error.
- [ ] **Step 6: Commit.** Run `gofmt -w cmd/atenea-tui/main.go cmd/atenea-tui/main_test.go && go test ./cmd/atenea-tui`; expected PASS. Commit `test(tui): cover file viewer flow under pty`.

### Task 6: Quality gates and TDD evidence

**Files:** `../specs/2026-07-09-tui-file-viewer.md`.

- [ ] **Step 1: Run final gates.** Run in order `go test -race ./internal/tui ./cmd/atenea-tui`, `go test ./...`, `gofmt -l .`, `go vet ./...`. Expected: tests PASS, gofmt no exit, vet exit 0.
- [ ] **Step 2: Record evidence.** Replace each `Pendiente de implementacion` of `TDD Cycle Evidence` with actually executed commands: safety net TUI, content REDs/model/PTY, GREENs, CRLF/binary/>1MiB/height 0/narrow width/PTY, refactor and final gates. Record date and real names of tests, without asserting unexecuted commands.
- [ ] **Step 3: Commit.** Commit `docs: record TUI file viewer validation evidence`.

## Self-review

- Tasks 1-2 cover secure root, CRLF, empty, binary, 1MiB limit, Chroma/fallback, gutter, scroll and ANSI.
- Tasks 3-4 cover Enter, Esc, gates, explorer preservation, visual replacement and narrow terminal.
- Task 5 provides production root, E2E real PTY and docs; task 6 requires race, full suite, gofmt, vet and honest evidence.
- All APIs used after are set above; the plan does not exceed the approved scope.

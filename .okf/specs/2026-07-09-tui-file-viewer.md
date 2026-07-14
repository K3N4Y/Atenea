---
updated_at: 2026-07-13
summary: Design specification for the read-only TUI file viewer.
---

# Design: read-only file viewer in the TUI

Date: 2026-07-09
Status: approved in brainstorming; implementation plan pending

## Objective

Extend the existing `atenea-tui` explorer with an integrated
file viewer. When you press `Enter` on a file, the viewer replaces the main
chat area (transcript and composer), keeps the explorer open, and
displays the content in read-only mode with line numbers and
syntax highlighting. `Esc` returns to the chat without losing the tree, the cursor or the scroll of the
explorer.

The feature is exclusive to the TUI. It does not change the Wails app or enable editing,
saving, creating, deleting or renaming files.

## Motivation

The current explorer allows you to find files, but `Enter` inserts a
`@ruta` mention in the composer and closes the panel. To inspect code the
user must leave the flow or ask the agent to use `read`. A local and navigable
viewer allows you to quickly review the workspace with an
interaction similar to LazyVim, without running tools or modifying the filesystem.

## Scope v1

- Open a file from the explorer with `Enter`.
- Replace only the right chat area with the viewer; the explorer remains
 open on the left.
- Render text with line numbers, vertical scroll and ANSI highlighting.
- Choose lexer from the file path with Chroma and use a stable dark
 theme that preserves contrast with the existing Lip Gloss styles.
- Show stable header with relative path and viewport position.
- Navigate the content with `j`/Down, `k`/Up, PgUp and PgDn.
- Close the viewer with `Esc` and recover the chat exactly as it was.
- Reject binary files, too large or not readable with an explicit
 status within the viewer; never try to display them partially.
- Maintain tolerance for narrow or zero-height terminals, same as the current
 viewport.
- Cover contracts with `internal/tui` unit tests and an E2E test
 under PTY of the TUI binary for actual keyboard navigation.

## Out of range

- Editing, saving or dirty state.
- Tabs, recent files, split panes or simultaneous preview of several
 files.
- Search within the file, goto line, minimap, folds or selection/copy.
- Automatic reload when the file changes on disk.
- Semantic/LSP highlighting or analysis of the project.
- Render images, PDF, Office files or any binary format.
- Change the current behavior of `@ruta`: the `@` menu and the flow of
 mentions remain intact.

## User experience

### Main flow

1. With the composer empty, `Space` + `e` open the explorer as today.
2. `j`/Down and `k`/Up move the cursor; `h`/`l` browse folders like today.
3. `Enter` on a folder retains its current semantics: expand/collapse.
4. `Enter` about a file tries to open it in the viewer; does not insert `@ruta` and
 does not close the explorer.
5. In the viewer, `j`/Down, `k`/Up, PgUp, PgDn, and the mouse wheel scroll the
 content. Clicks do not interact with the hidden transcript; focus remains in
 the viewer, not in the composer or in the tree.
6. `Esc` returns to the chat. The explorer remains open, retains the selected
 file, and the transcript returns to its prior scroll position even if events
 arrived while reading.
7. `q` and `Space` + `e` keep closing the explorer from the chat. Inside the
 viewer only `Esc` exits reading mode; `q` is not overloaded to avoid
 ambiguity with content and maintain a unique, visible and safe output.

### Layout

```
╭ explorer ───────────╮  ╭ internal/tui/model.go · 42-71/612 ─────────╮
│ 󰉋 cmd               │  │  42  func (m Model) Update(msg tea.Msg) ... │
│ 󰝰 internal          │  │  43      switch ev := msg.(type) {          │
│   󰝰 tui             │  │  44      case EventMsg:                     │
│     model.go        │  │  45          m = m.foldEvent(ev)           │
│     view.go         │  │  46          ...                           │
│  go.mod             │  │                                             │
╰─────────────────────╯  ╰─────────────────────────────────────────────╯
```

- The explorer retains its current width and viewport.
- The viewer uses all the remaining width, including the space previously occupied by
 transcript, autocomplete menu, composer and status.
- If the terminal is too narrow to show both columns, the viewer replaces the
 explorer for the duration of read mode, so the open file remains visible; a
 later resize restores the split layout and the file offset.
- The header contains the relative path of the workspace and `primera-ultima/total`
 of visible lines. In a single line file use `1-1/1`.
- Each line has a fixed width gutter calculated from the total number of lines,
 aligned to the right. The gutter is faint; the highlighted content is the focus.
- Every rendered source row resets its ANSI style, including intermediate rows
 of multiline tokens, so file syntax colors never affect the explorer panel.
- Source tabs are expanded to four spaces before highlighting. This makes ANSI
 width calculations match terminal cells, so a long tab-indented line cannot
 wrap into a second physical row and corrupt incremental scrolling.
- Long lines do not wrap in v1: they are trimmed to the visible width to
 preserve line numbers, performance and predictable vertical navigation.
- When opening a file, the viewport starts at line 1. When resizing, the first visible line is preserved
, limited to the new valid range.

### Keyboard Priority

With the viewer active, existing global gates retain priority:

1. Ctrl+C (stops the run and exits the TUI).
2. Pending permit and plan offer (`y`/`n`).
3. Active viewer (`Esc`, vertical navigation).
4. Open Explorer.
5. Leader `Space` + `e`, menu and composer.

Opening the viewer is not allowed while a permit or plan offer
 is pending. If they appear while the viewer is open, the gate is displayed and
takes the keyboard; When resolved, the viewer remains open.

## Architecture and contracts

### Model status

`internal/tui.Model` adds a read mode state separate from the explorer
state. You must save, at least:

- Relative path selected.
- Original lines of the file, total lines and vertical offset.
- Content already rendered/highlighted per line or an equivalent representation
 that does not rerun the lexer for each frame.
- Error state visible when the opening does not produce content.

The mode is activated only after validating and uploading the file. A failure does not
alter the transcript or the composer: it opens the viewer with its error status and
`Esc` returns to the normal chat.

`listFiles` is still the source of tree paths. The read receives an explicit, injectable
dependency, for example `readFile(path string)
([]byte, error)`, configured from the same workspace root as
`Engine.ProjectFiles`. The production implementation must resolve symlinks for both the workspace root and requested file, then reject any real path outside the root before invoking `os.ReadFile`; the tests use
a fake in memory. The path shown and the path used to read are always
relative, with separators `/`.

### Content classification

The opening follows this order, before creating the render:

1. Resolve and validate that the route is within the workspace.
2. Read the entire file once.
3. Reject if it exceeds `maxFileViewerBytes` (v1 value: 1 MiB).
4. Reject as binary if it contains a NUL byte (`0x00`).
5. Normalize CRLF to LF only for rendering and dividing into lines. An empty
 file has total `0` and shows an empty status line, not a `1` gutter.
6. Detect lexer by name/path with Chroma. If there is no lexer, use
 plaintext lexer; opening an unknown format is never a mistake.

The limit is evaluated on the bytes read. No previews or skipping of the
end of a large file: the limit message is displayed to avoid an
incomplete view that looks faithful.

Status messages are stable and assertable:

- `no se puede abrir <ruta>: <error>`
- `archivo binario: <ruta>`
- `archivo demasiado grande (> 1 MiB): <ruta>`
- `archivo vacio: <ruta>`

### Highlighted

The module uses `github.com/alecthomas/chroma/v2`, which is already in the
indirect dependencies graph. The implementation must import it directly and
update `go.mod`/`go.sum` deliberately. Chroma selects the lexer
by filename; the fallback is plain text. An ANSI formatter and a stable
dark style produce ANSI sequences that Lip Gloss and the terminal can compose.

The renderer must measure visible width ignoring ANSI escapes and clip without
cutting an escape sequence or leaving the style open. To do this, it relies
on the ANSI infrastructure already present in the project, not on `len(string)`.
The gutter does not go through Chroma and is composed outside of the highlighted content.

The implementation separates the pure content transformation (normalization,
classification, lexer and rendered lines) from the Bubble Tea layout. This allows
to test binaries, limits, fallback and numbering without a real terminal.

### Viewport

The viewport of the transcript is not reused to prevent offsets and scrolls of
chat from mixing. The display maintains its own offset and calculates its height from the
terminal minus a header line. All scroll and resize operations clamp to `[0, max(totalLineas - altoVisible, 0)]`.

The content is rendered as a window of lines `[offset, offset+altoVisible)`
 so as not to build a gigantic chain per frame. Opening can prepare
full highlighting of the file (maximum 1 MiB); later frames only
select the window and apply horizontal cropping.

## Planned changes

| Archive | Responsibility |
| --- | --- |
| `internal/tui/file_viewer.go` | Pure content model, boundaries, sorting, highlighting and scroll/crop helpers. |
| `internal/tui/file_viewer_test.go` | Unit tests of the viewer and its edges. |
| `internal/tui/model.go` | Reading dependency, transition `Enter` file -> viewer, focus, keyboard and resize. |
| `internal/tui/model_test.go` | Model integration, priority and explorer preservation tests. |
| `internal/tui/view.go` | Layout and render of the active viewer. |
| `internal/tui/view_test.go` or existing tests | Header assertions, gutter, clipping and visible states. |
| `cmd/atenea-tui/main.go` | Wire secure reader from the root already used by the TUI. |
| `cmd/atenea-tui/main_test.go` or existing PTY | E2E from the keyboard flow if the current harness lives here. |
| `../architecture/tui.md` | Document shortcuts, read-only mode, and limits. |

The exact names may conform to the structure found when implementing,
without breaking the contracts in this document.

## Testing contract

The implementation follows the mandatory `Safety net -> Understand -> RED
-> GREEN -> TRIANGULATE -> REFACTOR -> Evidence` cycle. The suite
wide is executed first; Afterwards, each new test is created red and verified separately before
minimum production.

| Test | Behavior |
| --- | --- |
| `TestFileViewer_OpensTextWithLineNumbers` | Text file produces 1-indexed lines and aligned gutter. |
| `TestFileViewer_SelectsLexerFromPath` | A known route receives ANSI from Chroma; an unknown woman uses plain text. |
| `TestFileViewer_NormalizesCRLF` | CRLF does not leave `\r` visible nor does it change the line count. |
| `TestFileViewer_RejectsBinaryFile` | NUL Byte produces binary state and does not render bytes of the file. |
| `TestFileViewer_RejectsOversizedFile` | More than 1 MiB produces the limit state and does not attempt to highlight. |
| `TestFileViewer_ClampsScrollAndResize` | Scroll, PgUp/PgDn and resize never go out of range. |
| `TestFileViewer_TruncatesAnsiSafely` | Narrow trim preserves visible width and correct ANSI reset. |
| `TestModel_TreeEnterFileOpensViewer` | Entering file activates viewer, does not add `@ruta` and does not close explorer. |
| `TestModel_FileViewerEscRestoresChatAndTreeSelection` | Esc turns off the display and keeps the tree, cursor and offset. |
| `TestModel_FileViewerCapturesNavigationKeys` | j/k/arrows/PgUp/PgDn move the viewer and do not change composer or tree. |
| `TestModel_FileViewerPermissionGateWins` | A pending permission retains priority over display keys. |
| `TestModel_FileViewerReadFailureShowsState` | Read error is visible and Esc returns to the chat without losing state. |
| `TestTUI_FileViewerFlowUnderPTY` | End-to-end flow: open explorer, select file, Enter, check header/content, Esc and return to chat. |

The E2E uses a temporary workspace and a real binary/harness under PTY, not a fake
of `Model`, to cover key sequences, terminal dimensions and
ANSI escapes. You should visually check the result during implementation: gutter
aligned, explorer stable, header without overlapping and correct clipping in
narrow terminal.

## Borderline cases

- Explorer empty or with error: `Enter` does not open viewer or panic.
- Invalid cursor after collapsing a folder: the existing clamp is applied
 before opening.
- File disappears between listing and opening: state `no se puede abrir...`.
- File without final newline: the last line is shown and numbered.
- File with only newline: shows an empty numbered line; zero
 byte file uses state `archivo vacio`.
- Unicode path: displayed and trimmed by cell width, not by bytes.
- 0x0 terminal, one row or width less than gutter: no panic; header and
 content degrade to available space.
- Agent activity can continue arriving while reading; transcript and
 composer retain their underlying state and are shown updated upon return.
- Changes on disk after opening are not reflected until closing and opening
 again, intentional behavior of v1.

## Success criteria

1. `Enter` on an explorer file opens a read-only view in the main
 area and does not modify the composer.
2. The view has path, line numbers, scroll and language highlighting with
 plain text fallback.
3. `Esc` returns to the chat and keeps the explorer, cursor and tree scroll.
4. Binaries, files larger than 1 MiB and read errors are reported without
 panic or partial render.
5. There are no actions that modify the filesystem.
6. The E2E test under PTY validates the actual flow and visual inspection shows no
 layout defects.
7. `go test ./...`, `go test -race ./...`, `gofmt -l .` and `go vet ./...`
 finish clean before closing the deployment.

## TDD Cycle Evidence

Implementation validated on 07-09-2026. The initial safety net of the worktree
executed all the packages except the root: `go test ./...` failed at startup because
`main.go` embedded `frontend/dist`, a directory that does not exist in a new worktree.
After `npm ci && npm run build` the complete gate passed. Vite's
large chunks warning and its two vulnerabilities reported by `npm audit` were not
introduced or modified by this feature.

| Phase | Evidence required | Current status |
| --- | --- | --- |
| Safety net | `go test ./internal/tui` PASS before changes; the initial `go test ./...` only failed because `frontend/dist` was absent in the root | focal PASS |
| Understand | Reading `model.go`, `view.go`, `tree.go`, explorer tests and wiring `cmd/atenea-tui` | PASS |
| NETWORK | `Test(OpenFileViewer|WorkspaceFileReader)` missing API failure; `TestModel_(TreeEnterFile|FileViewer)` missing builder/state failure | PASS verified |
| GREEN | Focal tests of content, viewport, model and green layout after implementing | PASS |
| TRIANGULATE | CRLF, empty, binary, >1 MiB, flat fallback, height 0, narrow width, read error, permission and PTY | PASS |
| REFACTOR | `go test ./internal/tui` green after separating `file_viewer.go`; E2E uses synchronized buffer after `-race` discovery | PASS |
| Evidence | `go test -race ./internal/tui ./cmd/atenea-tui`, `go test ./...`, `gofmt -l .`, `go vet ./...` PASS after `npm ci && npm run build` | PASS |

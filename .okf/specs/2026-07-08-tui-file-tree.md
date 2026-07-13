---
updated_at: 2026-07-13
summary: Design specification for the TUI file tree.
---

# Design: file tree in the TUI (Space+e)

Date: 2026-07-08
Status: approved in brainstorming; implementation plan pending

## Objective

In `atenea-tui`, the shortcut **Space** (vim style leader) + **`e`** opens or closes a
**left side panel** with the workspace file tree. The user
navigates with **nvim-tree** style keys and, upon confirming a **file**,
`@ruta` is inserted into the composer (same semantics as the current `@` menu). The
folder and language/framework icons are **Nerd Fonts** (no ASCII fallback).

## Motivation

Today composer only offers flat list files via the `@` token (filtered
by prefix). A hierarchical explorer allows you to navigate the workspace without
leaving the TUI and reuses the same mention contract (`@path`) that the
agent already understands.

## Scope v1

- Leader Space + `e`: explorer panel toggle.
- Left panel; transcript + composer on the right.
- In-memory tree built from `listFiles` / `Engine.ProjectFiles`
 (gitignore + glob limit, same as the `@` menu).
- Navigation j/k (and arrows), h/l, Enter, Esc, q, `r` to refresh, mouse wheel and click.
- Insert `@ruta` when committing a file and **close** the panel.
- Nerd Font icons by extension and by folder type.
- Behavior tests in `internal/tui` (TDD with evidence).

## Outside v1

- Fuzzy filter / search by typing in the tree.
- Preview file content.
- Multi-select.
- Git status in the tree.
- FS actions (rename, delete, create).
- Nerd Font or ASCII fallback detection.
- Change the model from the TUI (pending already documented in `../architecture/tui.md`).

## Architecture

All work lives in `internal/tui`. Do not play the runner, Wails or
`internal/wiring`.

| Piece | Responsibility |
| --- | --- |
| `tree.go` + `tree_test.go` | Node model, build from flat paths, visible rows (expand/collapse), icon map Nerd Font |
| `model.go` | Explorer and leader status; keyboard branch in `handleKey` |
| `view.go` | Horizontal layout: panel + chat; lipgloss styles |
| `engine.go` | No new API: reuse `ProjectFiles` / `listFiles` from `WithCompletions` |

### Tree data

1. When **opening** the panel without a current snapshot, invoke `listFiles()`
 (same source as `@-menu`). A successful tool event with a diff invalidates the
 snapshot and refreshes an open panel immediately; `r` forces the same refresh.
2. Convert the slice of flat relative paths into a tree (`treeNode` with
 name, relative path, isDir, children, expanded).
3. The **visible rows** are calculated by traversing the tree in order of
 depth, only expanding nodes with `expanded == true`.
4. Initial state when opening: **logical root expanded**, daughter folders
 **collapsed** (only first level entries are seen under the root of the
 workspace). If `listFiles` does not include empty directories,
 folders exist only if they have at least one file under them (consistent with the current
 glob).
5. Rebuilding preserves expanded paths and the selected path when they still
 exist in the new snapshot.

Conceptual structure:

```go
type treeNode struct {
    name     string
    path     string // relativo al workspace; "" para raiz virtual si hace falta
    isDir    bool
    expanded bool
    children []*treeNode
}

type fileTree struct {
    root   *treeNode
    rows   []treeRow // vista aplanada de visibles
    cursor int
}

type treeRow struct {
    node  *treeNode
    depth int
}
```

### Insert `@ruta`

Same idea as `applySelection` from the file menu:

- Insert (or complete) the token `@` + relative path in the caret of
 `textinput`.
- Do not send the prompt.
- Close the tree (`treeOpen = false`) after inserting a file.
- Confirm on a **folder** not inserted: only expand (Enter / `l`).

### Leader Space

- With the tree **closed** and without permission/plan gates ahead: the first
 **Space** key (`tea.KeySpace` or space rune) **does not** write to the input; leader
 and, if opened, load/rebuild the tree.
- If **another key** or **timeout** arrives: cancel leader. In v1 **not** the Space nor the key after the input is reinjected (predictable
 behavior; the user writes again).
- With the tree **open**, Space+e (or just the chord leader) closes the panel;### Keyboard Priority (High to Low)

1. Ctrl+C (stop + quit)
2. PgUp / PgDn (transcript scroll)
3. Pending leave (y/n)
4. Pending plan (y/n)
5. **Settled thinking `Shift+Tab`** (global toggle; no effect while live)
6. **Open tree** (capture navigation; does not feed input)
7. **Leader Space + e** (toggle tree)
8. Open autocomplete menu (`/` / `@`)
9. Esc / Enter / Tab / history / input

##UX

### Layout

```
╭ explorer ──────╮  <transcript viewport>
│ 󰉋 cmd          │  ...
│ 󰉋 internal     │
│    model.go   │  ╭ composer ─────────╮
│    tree.go    │  │ ❯ @internal/tui…  │
│  go.mod       │  ╰───────────────────╯
╰────────────────╯  build · modelo
```

- Panel width: reasonable fixed (~28 cells) or ~25% of the terminal width, with
 minimum and maximum for narrow terminals. If the total width is very low, the
 panel can occupy the entire useful width (simple degradation).
- Panel title: stable text `explorer` (assertable in tests).
- Cursor: highlight the active row (reverse style or prefix `>`).

### Keys with open shaft

| Key | Action |
| --- | --- |
| `j` / Down | Lower cursor (clamp at the end) |
| `k` / Up | Raise cursor (clamp at start) |
| `l` / Enter | If say: expand; if file: insert `@path` and close |
| `h` | If say expanded: collapse; else: move cursor to parent (if exists) |
| Esc | Close panel without inserting |
| q | Close panel without inserting |
| Space+e | Close panel (toggle) |
| Mouse wheel over explorer | Move the selection by three rows and keep it visible; the transcript does not scroll |
| Left click anywhere on a visible row | Select and activate the complete row: folder toggles; file opens or replaces the active viewer |

The composer does **not** receive keys while the tree is open.

### Nerd Font Icons

Map in `tree.go` (Unicode constants from Nerd Fonts). Minimal examples v1:

| Class | Glyph (example) | Notes |
| --- | --- | --- |
| Collapsed folder | `󰉋` | nf-md-folder |
| Expanded folder | `󰝰` | nf-md-folder-open |
| `.go` | `` | |
| `.ts` / `.tsx` | TypeScript icon seti/dev | |
| `.js` / `.jsx` | JS icon | |
| `.vue` | Vue icon | |
| `.md` | markdown icon | |
| `.json` / `.yaml` / `.yml` | config icons | |
| `.css` / `.html` | web icons | |
| default file | `󰈔` | nf-md-file-document-outline |

It is assumed that the user's terminal uses a **Nerd Font**. There is no
detection or fallback in v1.

Render of a row: `indent + icon + " " + name` (indent = two spaces per
level depth).

## Test contract

Names by behavior, tests along with the code (`tree_test.go`,
`model_test.go`, `View` asserts where applicable):

| Test | Behavior |
| --- | --- |
| `TestTree_BuildsFromPaths` | Flat routes → hierarchy and correct relative paths |
| `TestTree_ExpandCollapseVisibleRows` | Expand/collapse changes the set of visible rows |
| `TestTree_IconForExtension` | Known extensions return the expected glyph |
| `TestModel_LeaderSpaceE_OpensTree` | Space then opens the panel |
| `TestModel_LeaderSpaceE_TogglesClosed` | With the tree open, Space+e closes it |
| `TestModel_TreeKeys_NavigateAndInsertAt` | j/k move; Enter in file inserts `@path` and closes |
| `TestModel_TreeOpen_CapturesKeyboard` | With the tree open, runes do not go to the textinput |
| (view) | With open tree, `View()` contains marker `explorer` |

Keyboard tests use `tea.KeyMsg` like the rest of `model_test.go`
(including `KeySpace` for space, already used in the suite).

## Errors and edges

- `listFiles` nil or error: panel opens empty or with a faint error line;
 no panic.
- Workspace without files: panel with only empty message / without rows.
- Leader timeout: cancels without side effects.
- Permission or pending plan: the leader and the tree **do not** intercept (gates
 existing first).
- Terminal 0x0 / very narrow: no panic; bounded dimensions like the rest
 of the viewport.

## Impact on docs

Update `../architecture/tui.md` in the implementation: explorer keys,
leader Space, and remove or note the pending if applicable.

## Success criteria

1. Space+e opens the left panel with the workspace tree.
2. Space+e again (or Esc/q) closes it.
3. Enter /l on a file leaves `@ruta` in the composer and closes the panel.
4. Nerd Font icons visible in terminal with that font.
5. Green `internal/tui` package test suite; `gofmt` and `go vet` clean.

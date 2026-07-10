---
updated_at: 2026-07-09
summary: TDD implementation plan for direct mouse focus in the TUI explorer, chat, and file viewer.
---

# TUI Panel Focus Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `subagent-driven-development` (recommended) or `executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add direct-click focus to explorer, chat, and file viewer; visibly mark the active panel; route keyboard and wheel events by focus; preserve `Tab` for agent-mode switching.

**Architecture:** Add a `panelFocus` enum to `Model`. Normalize focus as visibility changes, route clicks based on tree/right-panel coordinates, and apply focus only after Ctrl+C, permission, and plan gates. Render active/inactive titled borders in the split layout while keeping viewport dimensions correct.

**Tech Stack:** Go 1.23+, Bubble Tea, Bubbles, Lip Gloss, Charm ANSI, `creack/pty`.

---

## Preconditions

- Use a dedicated worktree on branch `posthog-code/tui-panel-focus`.
- Preserve the current uncommitted tree mouse changes; this work extends them.
- Apply `.claude/skills/tdd-cycle-evidence/SKILL.md` per task.
- Do not modify `Tab`, Wails, editing, file tabs, split files, search, or add focus shortcuts.

## File map

- `internal/tui/model.go`: focus state, normalization, click mapping, key/wheel routing.
- `internal/tui/view.go`: active/inactive split-panel borders and titles.
- `internal/tui/model_test.go`: unit contracts for state, input routing, and layout.
- `cmd/atenea-tui/main_test.go`: actual SGR mouse behavior under PTY.
- `.okf/architecture/tui.md`: shipped focus behavior.
- `.okf/specs/2026-07-09-tui-panel-focus.md`: implementation status and real evidence.

## Task 1: Model focus and routing

**Files:**
- Modify: `internal/tui/model.go:190-202,324-360,399-505,507-705`
- Test: `internal/tui/model_test.go:3440-3830`

- [ ] **Step 1: Safety net.**

```bash
go test -run 'TestModel_(TreeMouse|TreeOpen_CapturesKeyboard|TreeEnterFile|FileViewerEscape|FileViewerMouse)' -v ./internal/tui
```

Expected: PASS. Stop for a pre-existing failure.

- [ ] **Step 2: RED — write focus tests.** Add the following test cases, reusing `apply`, `fakeAgent`, and `viewerReader`:

```go
func TestModel_ClickTreeFocusesExplorerAndCapturesKeys(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) { return []string{"one.go", "two.go"}, nil })
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	m = m.toggleTree()
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: 3})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if got, want := m.focus, focusExplorer; got != want { t.Fatalf("focus = %v, want %v", got, want) }
	if got, want := m.treeCursor, 1; got != want { t.Fatalf("treeCursor = %d, want %d", got, want) }
	if got := m.input.Value(); got != "" { t.Fatalf("input = %q, explorer key reached composer", got) }
}

func TestModel_ClickChatFocusesComposer(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) { return []string{"one.go"}, nil })
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	m = m.toggleTree()
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.treePanelWidth() + 2, Y: 0})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if got, want := m.focus, focusChat; got != want { t.Fatalf("focus = %v, want %v", got, want) }
	if got, want := m.input.Value(), "x"; got != want { t.Fatalf("input = %q, want %q", got, want) }
}

func TestModel_ClickViewerFocusesFileScroll(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) { return []string{"many.txt"}, nil }).WithFileReader(viewerReader(map[string][]byte{"many.txt": []byte("1\n2\n3\n4\n5\n6\n7\n8\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 6})
	m = m.toggleTree()
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: 3})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.treePanelWidth() + 2, Y: 0})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if got, want := m.focus, focusViewer; got != want { t.Fatalf("focus = %v, want %v", got, want) }
	if got, want := m.viewer.offset, 1; got != want { t.Fatalf("viewer.offset = %d, want %d", got, want) }
}

func TestModel_TreeFileClickKeepsExplorerFocus(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) { return []string{"first.go", "second.go"}, nil }).WithFileReader(viewerReader(map[string][]byte{"first.go": []byte("package first\n"), "second.go": []byte("package second\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	m = m.toggleTree()
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.treePanelWidth() - 1, Y: 3})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.treePanelWidth() - 1, Y: 4})
	if got, want := m.focus, focusExplorer; got != want { t.Fatalf("focus = %v, want %v", got, want) }
	if got, want := m.viewer.path, "second.go"; got != want { t.Fatalf("viewer.path = %q, want %q", got, want) }
}

func TestModel_ClosingFocusedViewerReturnsChatFocus(t *testing.T) {
	m := NewModel(&fakeAgent{}, "s1", nil).WithCompletions(nil, func() ([]string, error) { return []string{"one.go"}, nil }).WithFileReader(viewerReader(map[string][]byte{"one.go": []byte("package one\n")}))
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 12})
	m = m.toggleTree()
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: 3})
	m = apply(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: m.treePanelWidth() + 2, Y: 0})
	m = apply(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.viewer.active() { t.Fatal("Esc must close viewer") }
	if got, want := m.focus, focusChat; got != want { t.Fatalf("focus = %v, want %v", got, want) }
}
```

- [ ] **Step 3: Verify RED.**

```bash
go test -run 'TestModel_(ClickTreeFocusesExplorerAndCapturesKeys|ClickChatFocusesComposer|ClickViewerFocusesFileScroll|TreeFileClickKeepsExplorerFocus|ClosingFocusedViewerReturnsChatFocus)' -v ./internal/tui
```

Expected: compile failure because `focus`, `focusExplorer`, `focusChat`, and `focusViewer` are absent.

- [ ] **Step 4: GREEN — add state and transitions.** Before `Model`, add:

```go
type panelFocus uint8

const (
	focusChat panelFocus = iota
	focusExplorer
	focusViewer
)
```

Add `focus panelFocus` near `treeOpen` and `viewer`, then add:

```go
func (m *Model) normalizeFocus() {
	if !m.treeOpen && m.focus == focusExplorer { m.focus = focusChat }
	if !m.viewer.active() && m.focus == focusViewer { m.focus = focusChat }
	if m.treeOpen && m.ready && m.treePanelWidth() >= m.width { m.focus = focusExplorer }
}
```

Opening tree selects explorer; closing tree normalizes; `openTreeFile` retains explorer focus; viewer `Esc` sets chat focus after restoring `viewerReturnY`.

- [ ] **Step 5: GREEN — route keys and mouse.** After Ctrl+C, permission, and plan gates, normalize and dispatch:

```go
if m.focus == focusViewer && m.viewer.active() { return m.handleFileViewerKey(msg) }
if (msg.Type == tea.KeyPgUp || msg.Type == tea.KeyPgDown) && m.focus == focusChat { return m.scrollViewport(msg) }
if m.focus == focusExplorer && m.treeOpen { return m.handleTreeKey(msg) }
```

Keep menu/composer/history code unchanged. Add `rightPanelMouseOver` and `focusMouseTarget` so left clicks select tree if X is inside `treePanelWidth`, otherwise select viewer when active or chat. Call it before mouse dispatch. Explorer uses `handleTreeMouse`, viewer uses `scrollFileViewerMouse`, and only chat may run transcript-thinking click logic.

- [ ] **Step 6: GREEN gate and triangulation.**

```bash
gofmt -w internal/tui/model.go internal/tui/model_test.go
go test ./internal/tui
```

Expected: PASS. Add compact tests proving tree close returns chat focus and a 12x4 full-width tree normalizes to explorer focus.

- [ ] **Step 7: Commit.**

```bash
git add internal/tui/model.go internal/tui/model_test.go
git commit -m "feat(tui): route input through clicked panel focus"
```

Append the required trailers from the repository instructions after a blank line.

## Task 2: Render focus chrome

**Files:**
- Modify: `internal/tui/view.go:76-90,570-760`
- Modify: `internal/tui/model.go`
- Test: `internal/tui/model_test.go`

- [ ] **Step 1: Safety net.**

```bash
go test -run 'TestModel_(TreeViewHandlesNarrowTerminal|FileViewerReplacesChatWithHeaderAndGutter|FileViewerNarrowTerminalNeverOverflows)' -v ./internal/tui
```

Expected: PASS.

- [ ] **Step 2: RED visual contract.** Add `TestModel_FocusBordersMarkOnlyActivePanel`: with tree open at 80x12, tree click must produce `explorer *` and `chat` in `ansi.Strip(m.View())`; after opening a file and clicking right content it must produce `viewer *` and no `explorer *`.

- [ ] **Step 3: Verify RED.** Run `go test -run TestModel_FocusBordersMarkOnlyActivePanel -v ./internal/tui`; expected FAIL because titled focus chrome is absent.

- [ ] **Step 4: GREEN styles and title.** In `view.go`, add active/muted rounded border styles and active/muted title styles based on color `6`; add:

```go
func panelTitle(name string, active bool) string {
	if active { return panelActiveTitleStyle.Render(name + " *") }
	return panelMutedTitleStyle.Render(name)
}
```

Use `panelTitle("explorer", m.focus == focusExplorer)` as tree title and select its active/muted border without changing tree width/height.

- [ ] **Step 5: GREEN right panel.** Add `rightPanelTitle`, `rightPanel`, and `rightPanelContentHeight`. In split layout, wrap existing chat/viewer output in a right titled rounded panel. Its style is active when chat/viewer matches focus; set width `max(m.contentWidth()-2, 0)` and height `max(m.height-2, 0)`. Make `resizeViewport`, `fileViewerHeight`, and viewer rendering use `rightPanelContentHeight` so title and border rows are accounted for. Keep tree-closed rendering unchanged.

- [ ] **Step 6: GREEN gate.**

```bash
gofmt -w internal/tui/view.go internal/tui/model.go internal/tui/model_test.go
go test -run 'TestModel_(FocusBordersMarkOnlyActivePanel|TreeViewHandlesNarrowTerminal|FileViewerReplacesChatWithHeaderAndGutter|FileViewerNarrowTerminalNeverOverflows)' -v ./internal/tui
```

Expected: PASS.

- [ ] **Step 7: Triangulate.** Add `TestModel_SplitPanelFocusChromeNeverOverflows`: at 24x6 with `long-file-name.go`, tree open, each rendered line must satisfy `ansi.StringWidth(line) <= 24`. Run it with the visual contract; expected PASS.

- [ ] **Step 8: Commit.**

```bash
git add internal/tui/model.go internal/tui/model_test.go internal/tui/view.go
git commit -m "feat(tui): show active explorer chat and viewer focus"
```

Append the required trailers from repository instructions.

## Task 3: PTY verification and documentation

**Files:**
- Modify: `cmd/atenea-tui/main_test.go:64-110`
- Modify: `.okf/architecture/tui.md:86-103`
- Modify: `.okf/specs/2026-07-09-tui-panel-focus.md`

- [ ] **Step 1: Safety net.** Run `go test -run TestTUI_FileTreeMouseWheelAndClickUnderPTY -v ./cmd/atenea-tui`; expected PASS.

- [ ] **Step 2: RED/GREEN PTY test.** Keep the flow that opens `file-03.go` and replaces it with `file-05.go`. Add real SGR left-clicks inside the 100-column right and left panels, assert `viewer *`, then `explorer *`, then retain `file-05.go · 1-1/1`. Use exact `terminal.Write` escape sequences after checking actual rendered panel coordinates. Before Task 2 marker assertions fail; afterward this command passes:

```bash
go test -run TestTUI_FileTreeMouseWheelAndClickUnderPTY -v ./cmd/atenea-tui
```

- [ ] **Step 3: Documentation.** Add to `.okf/architecture/tui.md`: direct mouse focus in split layout; cyan `*` marker; explorer click retains navigation; chat click restores composer/transcript; viewer click enables j/k, PgUp/PgDn and wheel; `Tab` remains build/plan; tree replacement retains explorer focus; viewer Esc returns chat focus; permission/plan gates win. Change focus spec status to `implemented` only after final gates. Append exact executed TDD evidence.

- [ ] **Step 4: Final gates.**

```bash
gofmt -l .
go vet ./...
go test -race ./internal/tui ./cmd/atenea-tui
go test ./...
```

Expected: no `gofmt` output, vet exit 0, race PASS, full suite PASS.

- [ ] **Step 5: Commit.**

```bash
git add cmd/atenea-tui/main_test.go .okf/architecture/tui.md .okf/specs/2026-07-09-tui-panel-focus.md
git commit -m "test(tui): verify direct panel focus under pty"
```

Append the required trailers from repository instructions.

## Plan self-review

- **Coverage:** Task 1 covers focus state, direct clicks, keys/wheel, gates, normalization, tree replacement, and viewer Esc. Task 2 covers visible focus and narrow layout. Task 3 covers real terminal clicks, docs, and quality gates.
- **Scope:** Only TUI implementation/tests and `.okf` docs change. `Tab`, Wails, editing, tabs, and focus shortcuts remain untouched.
- **Resolved behavior:** Right clicks select viewer only when viewer is active, otherwise chat; file clicks retain explorer; wheel follows focus; full-width tree always owns focus.
- **Consistency:** Task 1 defines focus types/routing before Task 2 visual rendering and Task 3 PTY assertions.
- **Placeholder scan:** No deferred implementation steps remain.

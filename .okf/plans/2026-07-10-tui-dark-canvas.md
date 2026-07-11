---
updated_at: 2026-07-10
summary: TDD implementation plan for the complete #141414 terminal UI canvas.
---

# TUI Dark Canvas Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Paint every known terminal cell rendered by the Atenea TUI with the exact `#141414` background while preserving the existing layout and functional highlights.

**Architecture:** Route every `Model.View()` layout branch through one root `renderCanvas` helper. The helper applies a dedicated Lip Gloss background style and, only after a window size is known, fills the reported width and height.

**Tech Stack:** Go 1.23+, Bubble Tea, Lip Gloss, Termenv, `testing`.

---

## File Map

- Modify `internal/tui/view.go`: define the canvas color/style and centralize final root rendering.
- Modify `internal/tui/model_test.go`: prove ANSI background coverage, dimensions, alternate layouts, and unknown-size safety.
- Modify `.okf/architecture/tui.md`: record the root-canvas rendering invariant.
- Modify `.okf/README.md`: index this specification and plan.

### Task 1: Root Canvas RED and GREEN

**Files:**
- Modify: `internal/tui/model_test.go`
- Modify: `internal/tui/view.go`

- [x] **Step 1: Write the failing full-canvas test**

Add `TestModel_ViewPaintsCompleteDarkCanvas`. Force `termenv.TrueColor`, apply a `tea.WindowSizeMsg{Width: 32, Height: 10}`, render `m.View()`, and assert:

```go
if !strings.Contains(view, "\x1b[48;2;20;20;20m") {
    t.Fatalf("View() = %q, want #141414 true-color background", view)
}
plain := ansi.Strip(view)
lines := strings.Split(plain, "\n")
if len(lines) != 10 {
    t.Fatalf("View() has %d lines, want 10", len(lines))
}
for i, line := range lines {
    if got := lipgloss.Width(line); got != 32 {
        t.Fatalf("line %d width = %d, want 32", i, got)
    }
}
```

- [x] **Step 2: Verify RED**

Run: `go test -run TestModel_ViewPaintsCompleteDarkCanvas -v ./internal/tui`

Expected: FAIL because the current root view neither emits `#141414` nor fills the complete terminal dimensions.

- [x] **Step 3: Implement the minimal root canvas**

In `internal/tui/view.go`, add:

```go
const canvasBackground = "#141414"

var canvasStyle = lipgloss.NewStyle().Background(lipgloss.Color(canvasBackground))

func (m Model) renderCanvas(content string) string {
    style := canvasStyle
    if m.ready {
        style = style.Width(max(m.width, 0)).Height(max(m.height, 0))
    }
    return style.Render(content)
}
```

Refactor `Model.View()` so it computes one `content` result for chat, tree, and viewer branches, then returns `m.renderCanvas(content)` exactly once.

- [x] **Step 4: Verify GREEN**

Run: `go test -run TestModel_ViewPaintsCompleteDarkCanvas -v ./internal/tui`

Expected: PASS.

- [x] **Step 5: Check the affected package**

Run: `go test ./internal/tui`

Expected: PASS.

### Task 2: TRIANGULATE Layout and Size Cases

**Files:**
- Modify: `internal/tui/model_test.go`

- [x] **Step 1: Add alternate-layout coverage**

Add a table-driven test that renders the normal chat, the open tree, and an active file viewer at the same known size. Assert every case contains the true-color `#141414` sequence and strips to the requested width and height. This prevents any `Model.View()` branch from bypassing `renderCanvas`.

- [x] **Step 2: Add unknown-size safety coverage**

Add `TestModel_ViewDarkCanvasWithoutWindowSizeDoesNotPad`. Render a fresh model before `WindowSizeMsg`; assert it includes the background sequence, remains non-empty, and does not acquire arbitrary terminal-sized padding.

- [x] **Step 3: Verify triangulation**

Run: `go test -run 'TestModel_View(Paint|DarkCanvas)' -v ./internal/tui`

Expected: PASS for all canvas scenarios.

### Task 3: REFACTOR, Documentation, and Evidence

**Files:**
- Modify: `.okf/architecture/tui.md`
- Modify: `.okf/README.md`
- Modify: `.okf/plans/2026-07-10-tui-dark-canvas.md`

- [x] **Step 1: Document the invariant**

Add a short TUI architecture section explaining that the root view owns the `#141414` background and fills known dimensions, while child views retain explicit state highlights. Add links for the dark-canvas spec and plan to `.okf/README.md`.

- [x] **Step 2: Format and run quality gates**

Run:

```bash
gofmt -w internal/tui/view.go internal/tui/model_test.go
test -z "$(gofmt -l .)"
go vet ./...
go test ./...
```

Expected: every command exits zero and `gofmt -l .` is empty.

- [x] **Step 3: Record evidence and commit**

Update this plan's checkboxes and append the exact RED, GREEN, TRIANGULATE, and REFACTOR results. Commit implementation and documentation together after the gates pass.

## TDD Cycle Evidence

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Existing repository suite checked from the clean worktree | `go test ./...` | pass; all Go packages green before implementation |
| Understand | Root render paths, size handling, tests, and approved specification reviewed | `internal/tui/view.go`, `internal/tui/model.go`, `internal/tui/model_test.go`, `.okf/specs/2026-07-10-tui-dark-canvas.md` | behavior identified: every `Model.View` branch must share one sized root canvas |
| RED | Full-canvas test written before production code | `go test -run TestModel_ViewPaintsCompleteDarkCanvas -v ./internal/tui` | expected failure: rendered `32×10` view had no `#141414` ANSI background; gate ok |
| GREEN | Root canvas style and single final render path added | `internal/tui/view.go`; `go test -run TestModel_ViewPaintsCompleteDarkCanvas -v ./internal/tui`; `go test ./internal/tui` | focused test passed; package passed after normalizing one semantic test against deliberate right padding; gate ok |
| TRIANGULATE | Chat, explorer, file viewer, and unknown-size cases added | `go test -run 'TestModel_View(Paint|DarkCanvas)' -v ./internal/tui` | all layout and size cases passed; gate ok |
| REFACTOR | `Model.View` reduced to one canvas return; docs updated; E2E layout inspected | `test -z "$(gofmt -l .)"`; `go vet ./...`; `go test ./...`; `tmux` capture at `40×12` | formatting empty, vet clean, full suite green, real TUI layout intact; gate ok |

The repository TDD skill requests one delegated subagent per phase. This run
performed the same isolated gates directly because subagent creation was not
explicitly authorized by the user in this environment; no phase or gate was
skipped.

# TUI Chat Composer Visual Refresh Implementation Plan

> **For agentic workers:** Execute this plan task-by-task with the repository TDD cycle: Safety net, Understand, RED, GREEN, TRIANGULATE, REFACTOR, Evidence.

**Goal:** Move the active model into the lower-right composer border while preserving the token label, input behavior, and exact-width rendering.

**Architecture:** Keep `textarea.Model` and the existing rounded Lip Gloss box. Post-process the rendered top and bottom border rows through one width-aware border-label helper, using tokens on top and the model on the bottom; remove the standalone agent/model footer so the composer consumes no extra row.

**Tech Stack:** Go, Bubble Tea, Bubbles textarea, Lip Gloss, Charm ANSI utilities.

---

### Task 1: Model label in composer border

**Files:**
- Modify: `internal/tui/model_test.go`
- Modify: `internal/tui/view.go`

- [ ] Replace the footer behavior test with a failing render test that locates the final rounded-border row, expects `openrouter/free` inside it, rejects a standalone status row, and checks exact width.
- [ ] Run `go test -run TestModel_ComposerBottomBorderShowsModel -v ./internal/tui` and verify the expected failure.
- [ ] Remove the standalone footer row and its reserved viewport line.
- [ ] Add a width-aware bottom-border label renderer and use the current model as its label.
- [ ] Run the focused test and `go test ./internal/tui` until both pass.

### Task 2: Narrow and dynamic composer cases

**Files:**
- Modify: `internal/tui/model_test.go`
- Modify: `internal/tui/view.go`

- [ ] Add table-driven cases for a long model at normal width and a model that cannot fit in a narrow terminal.
- [ ] Assert every composer border row keeps the exact allocated width and the label truncates or disappears without malformed corners.
- [ ] Assert the token label remains in the top border while the model remains in the bottom border.
- [ ] Assert multiline composer height remains unchanged and no shortcut text is rendered.
- [ ] Refactor top and bottom border decoration through the same helper.
- [ ] Run the focused composer tests and `go test ./internal/tui`.

### Task 3: Documentation and quality gates

**Files:**
- Modify: `.okf/specs/2026-07-10-tui-chat-composer-visual-refresh.md`
- Modify: `.okf/architecture/tui.md`

- [ ] Correct the specification to record that token usage remains in the top border.
- [ ] Update TUI architecture documentation with the model-in-border layout and removed standalone footer row.
- [ ] Run `gofmt -l .`, `go vet ./...`, and `go test ./...`.
- [ ] Inspect the rendered TUI in a PTY or deterministic render fixture against the approved screenshot.
- [ ] Commit the implementation, push the branch, and open a pull request with the required project footer.

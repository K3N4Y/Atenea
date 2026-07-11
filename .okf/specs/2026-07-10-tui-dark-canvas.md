---
updated_at: 2026-07-10
summary: Specification for painting the complete terminal UI canvas with an OpenCode-inspired dark background.
---

# TUI Dark Canvas

## Goal

Give the terminal UI a consistent dark canvas inspired by OpenCode by painting
the complete known terminal surface with the exact background color `#141414`.

## Scope

- Apply only to the Go terminal UI under `internal/tui`.
- Cover the full width and height reported by Bubble Tea's window size,
  including otherwise empty cells around the chat, explorer, viewer, and
  composer.
- Preserve explicit functional styling for cursors, selections, diffs,
  statuses, and other highlighted content.
- Do not change the Wails frontend theme.

## Design

The final string returned by the root TUI view is rendered through a dedicated
Lip Gloss canvas style with `#141414` as its background. When terminal
dimensions are known, the style receives both width and height so Lip Gloss
fills every cell rather than painting only cells already occupied by content.

Keeping the background at the root avoids duplicating the color across child
views and ensures future layouts inherit the canvas automatically. Child
styles with explicit backgrounds continue to override the canvas where visual
state must remain distinct. Because child styles may emit complete ANSI resets,
the root canvas restores `#141414` immediately after those resets before
rendering any following cells.

## Acceptance Criteria

1. A rendered TUI with known dimensions includes an ANSI true-color background
   for `#141414` across the complete canvas.
2. Empty rows and cells are painted, not only visible text.
3. The plain-text layout retains the requested terminal width and height.
4. Existing selection, diff, status, and navigation behavior remains intact.
5. Zero-sized or not-yet-sized terminals continue to render without panic.
6. ANSI resets emitted by child styles do not expose the terminal's default
   background before the end of a rendered line.

## Verification

- Add a render-level test that inspects ANSI output and stripped dimensions.
- Add a second case for a minimal or unknown terminal size to prevent a
  size-dependent regression.
- Run the focused TUI test, the complete Go suite, `gofmt -l .`, and
  `go vet ./...`.

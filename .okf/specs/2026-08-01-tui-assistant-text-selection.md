---
updated_at: 2026-08-01
summary: Precise mouse selection and OSC 52 clipboard copy for settled assistant responses in the TUI.
---

# TUI assistant text selection

## Contract

The TUI keeps mouse capture enabled for scrolling and interactive controls while
allowing precise text selection inside a settled assistant response:

- A left-button drag that reaches a second terminal cell starts a linear selection.
- Only visible text in one settled assistant response is selectable. User
  messages, live responses, reasoning, tool activity, errors, and controls are
  excluded.
- Dragging beyond the response clamps the selection to that response. Selection
  does not auto-scroll.
- Highlighting uses reverse video and exists only while the button is held.
- Releasing copies the selected visible text through OSC 52 and immediately
  removes the highlight. ANSI styling and visual wrapping are omitted from the
  copied text; semantic line breaks remain.
- Empty and whitespace-only ranges do not copy.
- Resize, transcript mutation, scrolling, or terminal focus loss cancels an
  active selection without copying.

## Rendering architecture

Selection builds its copy projection from Glamour's natural-width Markdown
render, so copied text preserves Markdown semantics without introducing visual
wraps or allocating padding for an artificial width. During a drag, mouse
motion updates only selection coordinates. Reverse-video highlighting is
overlaid on the viewport output in `View`; it does not rebuild or replace the
rendered transcript.

## Copy confirmation

A successful OSC 52 write shows `Copied to clipboard` for two seconds. A write
error shows `Could not copy selection` for the same duration. OSC 52 cannot
confirm whether the terminal accepted the sequence, so success means that
Atenea wrote it successfully.

The snackbar overlays the transcript at the bottom right, one blank row above
the composer. It reserves no layout space and blocks pointer interaction over
covered cells except for the mouse wheel. It has one row of vertical padding,
two cells of horizontal padding, a `#303030` background across every cell, and
only a colored left rail: green for success and red for failure. A newer
notification replaces an older one and owns its own expiry generation.

On constrained terminals the snackbar first drops horizontal padding, then
truncates its one-line message, then drops vertical padding. It is omitted when
it cannot fit above the composer without covering it.

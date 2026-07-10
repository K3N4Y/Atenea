---
updated_at: 2026-07-09
summary: Approved interaction design for direct mouse focus in the TUI explorer, chat, and file viewer.
---

# Design: direct mouse focus for TUI panels

Date: 2026-07-09
Status: approved in brainstorming; implementation plan pending

## Objective

Make the active area of `atenea-tui` explicit when the explorer, chat, and
file viewer are visible. A direct left click selects the intended area; the
selected area receives its corresponding keyboard and mouse-wheel actions and
is visually distinguished.

The feature is limited to the TUI. It does not change the Wails app, the
agent-mode `Tab` shortcut, or add tabbed files, split editors, or new global
keyboard shortcuts.

## Motivation

The explorer and file viewer can be visible together, but keyboard ownership
is currently implicit. Direct focus makes it clear whether navigation targets
the tree, transcript/composer, or the open file, while preserving the existing
mouse workflow for opening files from the explorer.

## Scope

- Three focus targets: explorer, chat, and file viewer.
- Left click inside a visible target sets focus to that target.
- The active target has an accent border and title; inactive visible targets
  retain a muted border.
- When the explorer is focused, `j`/Down, `k`/Up, `h`, `l`, Enter, and wheel
  navigation operate on the tree.
- When the chat is focused, typing goes to the composer and the wheel scrolls
  the transcript.
- When the viewer is focused, `j`/Down, `k`/Up, PgUp, PgDn, and the wheel
  scroll the file.
- Clicking a file row in the explorer opens or replaces the viewer and keeps
  focus in the explorer, so another file can be opened immediately.
- Clicking the viewer explicitly transfers focus to it.
- Clicking the chat area explicitly transfers focus to it.
- `Esc` keeps its current behavior of closing the viewer and returning to chat.
- Existing permission and plan gates remain higher priority than panel focus.

## Out of scope

- Changing the existing `Tab` agent-mode shortcut.
- Keyboard focus cycling or new global focus shortcuts.
- A focusable terminal status/footer area.
- Editing, saving, file tabs, split panes, search, or selection/copy support.
- Changing the existing explorer mouse semantics beyond assigning focus.

## Focus model

Represent focus as a model-owned enum with these values:

| Focus | Availability | Default/transition behavior |
| --- | --- | --- |
| `explorer` | Tree is open | Opening the tree selects it; opening/replacing a file from its row retains it |
| `chat` | Always, unless a full-width tree replaces the right area | Initial state and destination after closing the viewer with `Esc` |
| `viewer` | A file viewer is active | Selected only by clicking within the rendered viewer area |

If the tree closes while it owns focus, focus returns to chat. If the viewer
is not active, viewer focus is normalized to chat. In narrow layouts where the
tree occupies the full terminal width, the tree is the only clickable and
focusable target.

## Input routing

Mouse coordinates determine the clicked panel from the rendered layout:

1. A click in the left explorer column focuses the explorer. Clicking a row
   continues to activate it: folder rows toggle and file rows open or replace
   the viewer.
2. With the explorer and right content visible, a click right of the panel
   separator focuses the right target: the file viewer when active, otherwise
   the chat.
3. A click on explorer chrome (title, border, empty area) changes focus but
   does not activate a tree row.
4. A click on chat or viewer chrome changes focus but does not alter content.
5. Wheel events are delivered only to the focused target, except the explorer
   retains its existing row activation behavior for an explicit click.

Keyboard routing follows focus after the current global priority rules:

1. Ctrl+C, permission approvals, and plan approvals keep their existing
   priority.
2. When explorer focus is active, tree keys are captured.
3. When viewer focus is active, viewer scroll keys are captured.
4. When chat focus is active, existing transcript/composer/menu behavior runs.

`Space e` continues to toggle the explorer only from chat focus. The existing
viewer `Esc` behavior always closes the viewer and selects chat, irrespective
of the current focus.

## Visual treatment

Each visible panel uses the same rounded border shape already used by the
explorer and composer. The active panel receives the existing accent color in
its border and title; inactive panels use a faint neutral border/title. The
tree-row reverse highlight remains independent of panel focus, so its selected
file remains legible when chat or viewer owns focus.

The right chat area gains a stable title/border treatment consistent with the
tree and viewer so all three focusable areas are equally discoverable. The
visual focus indication must not reduce the usable viewport height or cause
line wrapping at narrow terminal widths.

## Testing contract

Behavior tests remain in `internal/tui` and use the existing model helpers:

| Test | Behavior |
| --- | --- |
| `TestModel_ClickTreeFocusesExplorerAndCapturesKeys` | Tree click changes focus and tree keys do not reach the composer |
| `TestModel_ClickChatFocusesComposer` | Chat click switches focus and typed runes reach the composer |
| `TestModel_ClickViewerFocusesFileScroll` | Viewer click switches focus and scroll keys move the file |
| `TestModel_TreeFileClickKeepsExplorerFocus` | Opening/replacing a file from the tree does not steal tree focus |
| `TestModel_ClosingFocusedViewerReturnsChatFocus` | `Esc` closes viewer and normalizes focus to chat |
| `TestModel_FocusBordersMarkOnlyActivePanel` | Visible output exposes exactly one active panel marker in split layouts |
| E2E PTY | Actual SGR clicks switch focus and preserve existing tree-to-viewer replacement |

## Success criteria

1. A user can identify the active panel from the TUI rendering.
2. Clicking explorer, chat, or viewer makes that visible panel the keyboard and
   wheel target without adding a focus shortcut.
3. `Tab` continues to change the agent mode exactly as it does today.
4. A user can click several files from the focused explorer to replace the
   viewer, then click the viewer to scroll it.
5. Existing gates, narrow-terminal degradation, file-viewer scrolling, and
   tree mouse activation remain covered by regression tests.

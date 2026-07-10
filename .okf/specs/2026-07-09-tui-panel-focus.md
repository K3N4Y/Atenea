---
updated_at: 2026-07-09
summary: Implemented interaction design for direct mouse focus in the TUI explorer, chat, and file viewer.
---

# Design: direct mouse focus for TUI panels

Date: 2026-07-09
Status: implemented

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
- The focused panel title includes a cyan `*`; panel borders stay rounded and
  neutral.
- Keyboard navigation follows focus: explorer receives `j`/Down, `k`/Up, `h`,
  `l`, and Enter; chat receives composer and transcript controls; viewer
  receives `j`/Down, `k`/Up, PgUp, and PgDn.
- Mouse-wheel scrolling follows the panel under the pointer without changing
  keyboard focus: explorer moves the tree, chat moves the transcript, and
  viewer moves the file.
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
5. Wheel events are delivered to the visible panel under the pointer without
   changing focus. Explorer row activation remains an explicit click behavior.

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

The explorer, chat, and viewer use rounded neutral borders. Their title shows a
cyan `*` only while that panel owns focus. Clicking chat routes typing to the
composer and wheel events to the transcript. The tree-row reverse highlight
remains independent of panel focus, so its selected file remains legible when
chat or viewer owns focus.

The split rendering preserves the existing narrow-terminal behavior: when the
tree consumes the full width, it is the sole focus target and the title does
not add a marker. Viewer rendering reserves its title inside the existing
panel height, so direct focus does not add a new viewport row.

## Testing contract

Behavior tests remain in `internal/tui` and use the existing model helpers:

| Test | Behavior |
| --- | --- |
| `TestModel_ClickTreeFocusesExplorerAndCapturesKeys` | Tree click changes focus and tree keys do not reach the composer |
| `TestModel_ClickChatFocusesComposer` | Chat click switches focus and typed runes reach the composer |
| `TestModel_ClickViewerFocusesFileScroll` | Viewer click switches focus and scroll keys move the file |
| `TestModel_TreeFileClickKeepsExplorerFocus` | Opening/replacing a file from the tree does not steal tree focus |
| `TestModel_ClosingFocusedViewerReturnsChatFocus` | `Esc` closes viewer and normalizes focus to chat |
| Existing `TestModel_Tab*` and `TestModel_TreeLeaderDoesNotInterceptPendingGates` | Build/plan `Tab` semantics and approval-gate priority remain covered |
| `TestModel_ClickChatMarksOnlyChatActiveWithoutOverflow` | Chat click exposes the only `chat *` marker without overflowing split rows |
| `TestModel_MouseWheelRoutesByPointerWithoutChangingKeyboardFocus` | Wheel follows the hovered explorer/chat/viewer panel, preserves keyboard focus, and keeps narrow split chat within bounds |
| `TestModel_ViewerFocusWheelAtPointerOriginScrollsSplitExplorer` | A wheel event at the valid `(0,0)` explorer origin never falls through to the viewer |
| `TestModel_NarrowFullWidthViewerWheelNeverScrollsHiddenTree` | In a narrow full-width viewer, wheel input targets the visible viewer instead of the hidden tree |
| `TestTUI_FileTreeMouseWheelAndClickUnderPTY` | Actual SGR tree clicks replace files, then SGR panel clicks expose `viewer *` and `explorer *` |

## Implementation evidence

The verification below was executed after the implementation on 2026-07-09.

| TDD phase | Evidence |
| --- | --- |
| Safety net | `go test ./internal/tui ./cmd/atenea-tui` passed before implementation work. |
| Understand | The implementation, direct-focus plan, model contracts, and PTY test were inspected. |
| RED | Direct-focus, resize/chrome, chat-marker, post-click wheel/key routing, and pointer-wheel routing tests failed first with behavioral assertions before production changes. |
| GREEN | `go test -run 'TestModel_(ClickTreeFocusesExplorerAndCapturesKeys|ClickChatFocusesComposer|ClickViewerFocusesFileScroll|TreeFileClickKeepsExplorerFocus|ClosingFocusedViewerReturnsChatFocus|ResizeToFullWidthTreeNormalizesFocusAndKeepsItAfterSplitReturns|SplitFocusChromeMarksExactlyOneActivePanel|ClickChatMarksOnlyChatActiveWithoutOverflow|ClickChatAfterOpeningViewerRoutesMouseWheelToTranscript|ExplorerSelectionAfterOpeningViewerRoutesTreeKeysAndEsc|MouseWheelRoutesByPointerWithoutChangingKeyboardFocus)' -v ./internal/tui` passed. |
| TRIANGULATE | The contracts cover all three focus targets, split/full-width transitions, tree file replacement, pointer-targeted wheel routing at normal and one-cell narrow widths, valid origin coordinates, visible full-width viewer routing, viewer scrolling, `Esc`, `Tab`, and approval-gate precedence. |
| REFACTOR | Focus dispatch remains keyboard-only; wheel hit-testing follows the visible panel and all visible focusable panels share titled neutral chrome with exactly one cyan marker. |
| PTY evidence | `go test -run 'TestTUI_(FileViewerFlowUnderPTY|FileTreeMouseWheelAndClickUnderPTY)' -v ./cmd/atenea-tui` passed, including actual SGR mouse sequences that replace the viewer file and expose the focus markers. |
| Final gates | `gofmt -l .` had no output; `go vet ./...`, `go test -race ./internal/tui ./cmd/atenea-tui`, and `go test ./...` passed. |

## Success criteria

1. A user can identify the active panel from the TUI rendering.
2. Clicking explorer, chat, or viewer makes that visible panel the keyboard and
   wheel target without adding a focus shortcut.
3. `Tab` continues to change the agent mode exactly as it does today.
4. A user can click several files from the focused explorer to replace the
   viewer, then click the viewer to scroll it.
5. Existing gates, narrow-terminal degradation, file-viewer scrolling, and
   tree mouse activation remain covered by regression tests.

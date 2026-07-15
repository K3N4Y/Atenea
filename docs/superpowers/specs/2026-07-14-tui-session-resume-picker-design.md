# TUI Session Resume Picker Design

## Goal

Replace the current `/resume` behavior, which immediately opens the previous
TUI session, with a full-screen session picker for the current workspace.

The picker must let the user search sessions, inspect each session's title and
last activity date and time, and explicitly choose which session to open.

## User Experience

Entering `/resume` and pressing Enter opens a full-screen dark view over the
chat. The search field at the top receives focus immediately. The session list
appears below it, ordered by most recent activity first.

Each row shows:

- The session title on the left.
- The last activity date and time on the right.
- A subtle marker when the row represents the currently open session.

Keyboard behavior:

- Typing filters session titles case-insensitively.
- Up and Down move the selection through visible results.
- Enter opens the selected session.
- Escape closes the picker and returns to the unchanged chat.

When the filter has no matches, the view displays `No sessions found`.

## Scope

The picker lists only TUI sessions whose recorded workspace matches the
current workspace. It does not list sessions from the Wails application or
other workspaces.

The first version uses title search only. It does not add mouse interaction,
session deletion, sorting controls, previews, pagination, or workspace
switching.

## Architecture

### Session Metadata

`session.SessionSummary` will expose the durable last-activity time in addition
to the existing ID, title, and workspace fields. Both store implementations
must preserve the same ordering and timestamp semantics.

The SQLite store will persist an activity timestamp for events and migrate
existing databases without losing events. The session's last activity is the
timestamp of its most recently persisted event. Legacy events receive a stable
migration value so old sessions remain selectable.

The memory store will track the timestamp associated with the latest append for
each session. Tests may inject or control time through the smallest existing
store boundary that keeps ordering deterministic.

### Engine Boundary

The TUI engine will replace the sequential `ResumePrevious` operation with two
explicit operations:

1. List resumable sessions for the current workspace.
2. Load a selected session by ID.

The load operation validates that the requested session is a TUI session in the
current workspace before returning its events and restored build/plan mode.
It must not permit changing sessions while the current session has an active
run.

### TUI State

The Bubble Tea model will add a dedicated resume-picker state rather than
reusing the composer's slash/file completion menu. This state owns:

- The complete session list returned by the engine.
- The current search query.
- The filtered rows.
- The selected row index.
- Loading and error state.

Opening `/resume` clears the command from the composer and requests the session
list asynchronously. The existing chat remains in memory behind the picker so
Escape can restore it without reconstruction.

Confirming a row requests that exact session asynchronously. Only a successful
load replaces the active session ID, transcript, prompt history, and build/plan
mode. A failed load leaves the picker open and renders the error without
discarding the current chat.

### Rendering

The picker is a full-screen TUI view with the repository's existing dark
palette and accent styles. Its top search field uses a rounded border and the
available terminal width with horizontal margins. Rows are truncated safely so
neither long titles nor timestamps wrap and corrupt layout height.

The selected row uses the existing accent color. The current-session marker and
unselected timestamps use the existing muted status style. Narrow terminals
prioritize a readable title and omit or truncate secondary metadata when both
cannot fit on one line.

## Data Flow

1. The user submits `/resume`.
2. The model enters the picker loading state and invokes the engine list
   operation.
3. The engine reads session summaries, filters them to the current workspace,
   and returns them ordered by last activity.
4. The model renders the rows and applies local title filtering as the user
   types.
5. The user confirms a selected row.
6. The engine validates and loads that session's events and mode.
7. The model replaces the active conversation only after the load succeeds.

## Error Handling

- If listing sessions fails, the picker shows the error and remains closable
  with Escape.
- If no sessions match the query, the picker shows `No sessions found`.
- If a session disappears between listing and selection, loading reports an
  error and preserves the current chat.
- If the current session has an active run, `/resume` returns the existing stop-
  the-run error and does not open the picker.
- Invalid or cross-workspace session IDs are rejected by the engine boundary.

## Testing

Store contract tests will verify that summaries expose durable last-activity
times, remain ordered by activity, and survive SQLite reopen and migration.

Engine tests will verify workspace filtering, exact-session loading, mode
restoration, active-run rejection, missing-session handling, and cross-workspace
rejection.

Model tests will exercise the user-visible flow end to end at the Bubble Tea
boundary: submit `/resume`, wait for rows, filter by title, navigate with arrow
keys, confirm with Enter, cancel with Escape, render `No sessions found`, and
preserve the current chat on errors. View assertions will cover title and
date/time alignment, current-session marking, terminal-width truncation, and
the full-screen layout.

## Documentation

Implementation will update `.okf/architecture/tui.md` to describe the picker,
its engine boundary, and the durable last-activity metadata.

---
updated_at: 2026-07-11
summary: Architecture and behavior of the Atenea terminal user interface.
---

# atenea-tui: the terminal interface

`atenea-tui` is the second frontend of the agent: a Claude Code-style TUI that
runs in the terminal. Reuse the SAME agent loop as the Wails app (the
runner, the tools, the ask-before-run, the skills and the subagents); the only thing that
changes is the presentation border.

The composer also owns two built-in session commands that never become model
messages: `/new` creates a session and `/compact` requests durable context
compaction for the active session.

```
wails app:  runner -> EmittingStore -> Bus -> EmitFunc(runtime.EventsEmit) -> frontend web
atenea-tui: runner -> EmittingStore -> Bus -> EmitFunc(chan tea.Msg)       -> Model Bubble Tea
```

## Pieces

- `cmd/atenea-tui/main.go` — the thin border (equivalent to `main.go` from
 Wails): loads `.env`, opens the global provider service from
 `os.UserConfigDir()/atenea/providers.json`, and preserves the previous
 environment fallback (`OPENROUTER_API_KEY` present = OpenRouter with
 `OPENROUTER_MODEL`; absent = demo without network) when no valid global
 selection is available. It diverts
 the standard log to a temporary file (do not paint over the alternative screen),
 opens the SQLite SHARED with the app via `session.OpenDefault` (fallback to
 memory if it fails, with `Close` on exit) and runs `tea.NewProgram` with
 alt-screen. Without its own testable logic.
- `internal/tui/engine.go` — coordinates `/compact` per session. An idle session
  starts immediately; an active run records one deduplicated pending request and
  drains it after normal completion or cancellation. Prompt execution and
  compaction share a per-session mutex so a later prompt observes the committed
  compacted baseline.
- `internal/tui/model.go`, `fold.go`, and `view.go` — render transient
  `Compaction queued` / `Compacting context` feedback without persisting it.
  Successful completion is replaced by the durable `Context.Compacted` event;
  insufficient history is informational and provider failures use the normal
  error styling.
- `internal/tui/engine.go` — the headless assembly of the agent. It creates
 inbox/gate/snapshots in memory, decorates the store with `EmittingStore` on a
 `event.Bus` whose `EmitFunc` bridges each `session.SessionEvent` to the
 TUI channel, and delegates the runner wiring to `wiring.Build` (the same source of
 truth as the app). Saves the mode per session (`modes` + `modeFor`, the hook
 `Mode` of `wiring.Build` that the runner consults each turn): `SendPrompt`
 sets normal mode and `SendPlanPrompt` sets plan-mode before queuing, mirror
 of `App.SendPrompt`/`App.SendPlanPrompt`. Both support inbox and run
 `Run` in a session-cancellable goroutine (mirror of `App.start`); at
 finish publishes `RunDoneMsg`. Satisfies the `Agent` interface of Model and
 exposes catalog, refresh, current-selection, and transactional selection
 operations to the optional model-selector boundary.
- `internal/checkpoint/git.go` — prompt-level workspace snapshots for the TUI.
  It stores Git trees in a private bare repository below
  `session.DefaultCheckpointPath()` (`ATENEA_CHECKPOINTS` overrides it), using
  the user's workspace only as `--work-tree`. Durable tree references include
  the canonical workspace-path hash and are rejected from any other root.
  Snapshots include tracked files
  and non-ignored untracked files, executable modes, and symlinks. Ignored
  files remain untouched, and the workspace's main `.git` directory, index,
  branch, HEAD, refs, and staged changes are never mutated.
- `internal/tui/model.go` + `fold.go` + `view.go` + `reveal.go` — the Model of
  Bubble Tea. `fold.go` projects durable `SessionEvent` to
  conversation inputs (streaming assistant text, collapsible
  thought blocks, user messages, stateful tool calls, pending permissions,
  errors) and keeps live token usage: estimated request input from
  `Step.Started`, generated tokens estimated from streaming deltas, and exact
  provider usage from `Step.Ended`; `model.go` handles keyboard and channel
  event pump;
  `view.go` renders with a height-bounded viewport and smart following:
  incoming events and reveal ticks follow the queue only while the user is at
  the bottom; scrolling upward preserves the reading position during streaming
  and shows a passive `↓` at the lower-right when new agent activity arrives;
  returning manually to the bottom clears the indicator and resumes following.
  PgUp/PgDn and the mouse wheel navigate the transcript,
  job status line, composer box with rounded edge and a compact
  `↑ input ↓ output ctx used/window` label in its top-left border, and
  footer with agent/model, all with lipgloss styles. Live estimates carry a
  `~` prefix and lose it when the step closes; when `Step.Ended` omits usage,
  the last estimate remains visible without the approximation marker. The
  built-in `/new` command clears both exact and live token usage so a new
  session never inherits the previous session's counters;
  `reveal.go` is the smooth
 streaming of the text that arrives by deltas, assistant and thought (parity
 with `frontend/src/lib/reveal.ts`): the view reveals a prefix by runes that
 advances with a loop of ticks, with catch-up proportional to the backlog; an
  assistant renders that revealed prefix as Markdown while live, then renders
  its complete Markdown once the reveal drains.

- `internal/wiring` — the shared assembly extracted from `app.go`: registry de
 tools, skills and slash-commands, catalog of subagents with the propagated gate,
 system prompts (normal/plan/local) and the configured runner. `App.wire` and
 `NewEngine` consume it; a tools/skills change reaches both frontends.

### User messages

User prompts render as full-width `#242424` transcript blocks inset two cells
from the chat edges. Each block has one blank row of vertical padding and three
cells of inner horizontal padding; its content starts with a faint `❯` marker
and normal-weight text. The TUI does not render message timestamps.

### Activity rail

Tool calls, pending permissions, and hard step errors render as activity
entries with a continuous visual column at column 0: a status marker (`●`
running, `✓` success, `✗` failure, `?` pending permission) followed by the
tool name padded to an 8-column name field and the summarized input
(`✓ bash     ls`). Detail lines under a header carry the `│ ` rail: output
preview, diff lines, the failure reason, and the truncation mark. Successful
edits/writes append a `+N -M` stat computed from the unified diff (file
headers excluded). Adjacent activity entries join without a blank line into
one contiguous block, while narrative keeps its own paragraph; the shared
predicate `compactActivityJoin` keeps `renderTranscript` and `entryLines`
(click targeting) in lockstep. Full contract:
[TUI transcript activity hierarchy](../specs/2026-07-11-tui-transcript-activity-hierarchy.md).

### Root Canvas

`Model.View` routes every chat, explorer, and file-viewer layout through one
root Lip Gloss canvas. Its background is the exact dark color `#141414`; after
the first `WindowSizeMsg`, the canvas fills the complete reported width and
height so empty terminal cells cannot fall back to the user's terminal theme.
Before that first size arrives the background paints line by line instead:
a multi-line Lip Gloss render would pad every line to the longest one, an
arbitrary rectangle that would also hang trailing spaces after activity
headers.
Child styles remain responsible for explicit functional highlights such as
the tree cursor, diffs, statuses, and selection states.
Child styles can emit complete SGR resets inside the root render, so the
canvas immediately restores `#141414` after each reset before any following
cells. Styled prompts, cursors, panels, and Markdown therefore cannot expose
the terminal's default background mid-line.

### Top bar

`Model.View` prepends a fixed top-bar chrome above every layout once the first
`WindowSizeMsg` arrives: one blank row, the bar row, one blank row
(`topBarHeight = 3`, with `topBarMargin = 1` above and below). The margin is one
row, not `composerOuterMargin`: a single blank row is the project's vertical
rhythm and visually balances the two-cell horizontal inset, and two rows would
overflow short terminals. Every chrome row shares the `#141414` canvas
background (and restores it after inner resets, like the rest of the view). The
bar row shows, left to right, inset by `composerOuterMargin` on both sides so it
aligns with the composer box: the git branch (a powerline branch glyph `` in
green plus the branch name), the working directory (home abbreviated to `~`,
faint) and, aligned to the right inset, the context usage `used / window` (e.g.
`16k / 200k`) taken from the last `Step.Ended` input tokens and
`llm.ContextWindow(model)`. When the model's window is unknown only the used
count shows, and without any usage the right side is empty. Branch and directory
enter once through `WithWorkspace(branch, dir)`, fed by `cmd/atenea-tui/main.go`
(branch via `git rev-parse --abbrev-ref HEAD`, `""` outside a repo; directory
home-abbreviated). On a width too narrow for both sides the left segment
truncates with `…` so the context label always fits.

Because the chrome owns the top `topBarHeight` rows, the body (chat, explorer,
viewer) sizes against `bodyHeight = height - topBarHeight` rather than the full
terminal height, and mouse events subtract `topBarHeight` from their row before
the body handlers read it, so a click anywhere in the chrome is inert. Total
rendered height is unchanged: the chrome comes out of the body, never adds extra
rows.

## Contracts that the TUI establishes with tests

- The fold is pure: the text deltas accumulate in a live block and `Step.Ended`
 closes without duplicating against the coalesced Message; tool-input is not transcribed.
- The reasoning folds to its own thinking block (parity with the
 ThinkingBlock of the desktop): while it flows it shows the header
 `[pensando]` and the last 4 non-empty lines of the revealed text (sliding
 window, with the same smooth reveal of the assistant). Every rendered
 thinking line keeps the two-cell chat inset, including physical lines created
 by wrapping an expanded block. Closed and drained collapses to the line
 `[penso <duracion>] ⇧Tab` with that same inset. `Shift+Tab` expands or
 collapses every settled thinking block
 regardless of whether chat, explorer, or viewer owns panel focus; a left
 click on a settled summary in the visible chat transcript toggles that block
 without stealing explorer focus. Pending permission and plan-approval gates
 retain precedence, and live thinking stays unchanged.
 resolves via the gate with the `SessionID` of the EVENT (a surface
 request from a subagent is resolved with the child id).
- Enter sends via the active mode path (`Agent.SendPrompt` in build,
 `Agent.SendPlanPrompt` in plan); Ctrl+C cuts and exits; Esc only shorts;
 `RunDoneMsg` turns off the work flag. `Ctrl+J` inserts a newline without
 submitting; the composer grows to five visible lines and then scrolls while
 preserving literal newlines in the submitted prompt.
- `/model` is a local command intercepted before slash expansion, prompt
  history, inbox admission, and durable events. `/model <query>` reuses the
  composer popup to search every provider/model pair. The first Enter or Tab
  completes `/model <provider-id> <model-id> `; the next Enter persists and
  applies that pair.
- `/undo` is a local command intercepted before prompt history, inbox
  admission, and durable user-message events. It cancels and finalizes an
  active run, restores the latest prompt's pre-run workspace tree, removes the
  prompt range from effective session projections, rebuilds the transcript,
  and returns the reverted literal prompt to the composer. Repeated `/undo`
  walks backward through prompt boundaries. A finished prompt is undoable only
  while the current non-ignored workspace still matches its captured after
  tree; later workspace changes make undo fail without changing files or the
  effective conversation. Ignored-file changes do not block undo and survive
  restore. This control exists only in `atenea-tui`; the desktop frontend has
  no undo control.
- Tab toggles the build/plan agent mode: it's sticky between submissions (each
 Enter routes down the active mode path, without resetting it) and inert with a
 pending permission, and the composer footer reflects this live. In plan-mode the
 runner announces `present_plan` without `bash`/`write`; the next `SendPrompt`
 returns the session to normal mode.
- With the explorer open, the split layout has direct mouse focus. Clicking the
  explorer focuses its navigation; clicking the right side focuses the active
  file viewer, or the chat transcript/composer when no viewer is open. The
  focused panel title includes a cyan `*`; the chat remains the
  composer/transcript input and scrolling surface when it owns focus. Keyboard
  navigation follows focus: explorer receives `j`/Down, `k`/Up, `h`, `l`, and
  Enter; viewer receives `j`/Down, `k`/Up, PgUp, and PgDn. Mouse-wheel
  scrolling instead follows the panel under the pointer without changing
  keyboard focus: explorer moves its tree, viewer moves the file, and chat
  moves the transcript. A tree file click opens or replaces the viewer without
  moving focus away from the explorer. `Esc` from a focused viewer closes it and
  returns focus to chat. Ctrl+C and pending permission/plan approval gates
  keep precedence over panel routing; `Shift+Tab` still toggles settled thinking
  globally, and `Tab` continues to control build/plan mode rather than panel
  focus.
- A successful `present_plan` adds the offer `[plan] plan presentado
  (y ejecutar / n seguir en plan)` to the end; with the offer pending the keyboard does not
 feed the input. `y` accepts via `Agent.AcceptPlan` (the Engine returns the
 session to normal mode and promotes the fixed implementation prompt, mirroring
 `App.AcceptPlan`), turns off the plan-mode and marks the run as working;
 `n` discards the offer and the mode remains as is. A failed `present_plan`
 offers nothing.
- The viewport respects the height of the terminal, follows new events only
  while it remains at the bottom, preserves the user's offset while reading
  history during streaming, and survives lowercase terminals (0x0/1 line:
  dimensions bounded to >= 0;
 real bubbles/viewport panic found in smoke E2E under pty).
- The composer box measures the terminal width, starts at three total rows
 including borders, and grows to seven total rows for five visible input
 lines. Longer multiline prompts scroll vertically; long individual lines
 scroll horizontally within the input. Its native textarea cursor blinks while
 chat and the terminal window own keyboard focus, and hides while the window is
 unfocused or explorer, viewer, permission, or plan approval owns input. The
 footer shows `<agente> · <modelo>`: the model enters once via
 `WithStatus` and the agent reflects the active mode (build/plan).

## Global provider configuration

Provider declarations live in `providers.json` under Atenea's user config
directory. Each provider has a stable `id`, display `name`,
`type: "openai-compatible"`, normalized `base_url`, optional `api_key_env`,
optional `openrouter_reasoning`, and configured model identifiers. Only the
environment-variable name is persisted; secret values never enter the file.

The selected pair is saved by atomic rename before the running provider
snapshot is swapped. Missing keys, provider-construction errors, and save
failures leave both runtime and persisted state unchanged. Discovered models
are stored separately in `models-cache.json`; refresh failures retain selected,
configured, and previously cached models.
- The composer keeps the latest 100 submitted TUI prompts in the shared durable
 store, so Up/Down history survives process restarts and spans previous TUI
 sessions. New prompts are recorded as non-conversation `Composer.Prompt`
 events, preserving literal slash commands without adding them twice to model
 context; older sessions fall back to their user-message projection. Startup
 reads sessions from newest to oldest and stops after collecting 100 prompts;
 failure to persist this auxiliary history does not cancel an already admitted
 prompt. With an
 empty composer, Up recalls older prompts and Down moves toward newer ones;
 moving past the newest prompt clears the composer. History navigation does
 not start while the composer already contains text, and autocomplete menus
 retain priority over history keys.
- With the composer empty, `Space` builds a one-second leader and `Space e` opens
 or closes the `explorer` panel. The panel lists the workspace as a tree with
 Nerd Font icons; `j`/Down and `k`/Up move the cursor, `l`/Enter expands a
 folder or opens a file in the viewer, `h` collapses or moves up to the parent, and
 Esc/`q` closes without inserting. The mouse wheel over the explorer moves its
 selection by three rows without moving the transcript; a left click anywhere in
 a visible row activates it (toggle a folder or open/replace a file in the viewer). While the explorer is
 open its keys do not reach the composer; permissions and plan approval retain priority.
- The explorer occupies a bounded left column and transcript, menus and
 composer are recalculated to the remaining width. If `listFiles` fails or the workspace
 is empty, the panel remains usable and displays the non-panic status.
- In split layout, direct mouse clicks focus explorer, chat, or viewer. The
  focused panel has the cyan `*` in its title; explorer row activation retains
  explorer focus, chat restores the transcript/composer target, and viewer
  receives `j`/`k` and PgUp/PgDn. The mouse wheel follows the hovered panel
  without changing keyboard focus. `Tab` still switches
  build/plan, permission and plan approval gates win, a full-width tree owns
  focus, and viewer `Esc` returns focus to chat.

### File Viewer

- `Enter` on a file opens a read-only view in the main area;
 does not add `@ruta` or close the explorer. The view shows path,
 line numbers and highlighting when Chroma recognizes the language. The viewer
 owns keyboard and mouse-wheel scrolling while active; clicks cannot alter the
 hidden transcript, and `Esc` restores its saved scroll position. On a terminal
 too narrow for the explorer column, the viewer takes the full screen until the
 terminal is wide enough again. Syntax highlighting is reset at every rendered
 file row, so multiline tokens (such as comments) cannot leak styles into the
 explorer or another terminal row. Tabs are expanded to four spaces before
 highlighting because terminal tab stops and ANSI width measurement disagree;
 every source row therefore maps to exactly one terminal row while scrolling.
- Does not allow editing or saving. Binaries, files larger than 1 MiB, empty or
 read errors show an explicit status.

## Persistence shared with the app

The TUI opens the SAME SQLite as the Wails app via `session.OpenDefault`: the
path is resolved by `session.DefaultDBPath` (`ATENEA_DB` if set; otherwise
`<config>/atenea/atenea.db`). The WAL + busy_timeout pragmas (by connection,
via DSN) allow two processes at the same time on the same file: concurrent readers
 and a writer that waits for the write-lock instead of failing with
SQLITE_BUSY; The Seq per session is assigned in an atomic INSERT (subquery
MAX(seq)+1 with RETURNING), so two processes do not race the sequence. Each
TUI session records `Session.Cwd` in its first prompt and appears in the
sidebar of the app grouped by that folder. The app refreshes itself: a
watcher polls the store's `PRAGMA data_version` (it changes only with
writes from ANOTHER connection) and emits `sessions:changed`, and the frontend re-requests
`ListSessions` upon receipt. If opening SQLite fails, `OpenDefault` returns
a store in usable memory along with the error: the TUI still works, just
without persisting.

Prompt checkpoint metadata shares that SQLite event log, while workspace tree
objects live separately under `session.DefaultCheckpointPath`. The feature has
no redo, retention service, background cleanup, transaction framework, or
desktop adapter.

## Run

```bash
go build -o build/bin/atenea-tui ./cmd/atenea-tui
./build/bin/atenea-tui          # demo sin red si no hay OPENROUTER_API_KEY
OPENROUTER_API_KEY=... ./build/bin/atenea-tui
```

## Known Pending (v1)

- Plan-mode now alternates with Tab, the AcceptPlan flow now executes the approved
 plan and the composer now autocompletes slash-commands and @-files. The composer
 keeps token usage in its upper border and the active model in its lower-right
 border, with two cells of outer spacing on its sides and below; plan mode appends
 `· plan` to that model label. Changing the model
 from the TUI is still pending, so the label reflects the environment model fixed
 per run.
- A new prompt while an activity is running cancels the previous run
 (same behavior as the Wails app today).

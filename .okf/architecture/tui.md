---
updated_at: 2026-07-20
summary: Architecture and behavior of the Atenea terminal user interface.
---

# atenea: the terminal interface

`atenea` is the second frontend of the agent: a Claude Code-style TUI that
runs in the terminal. Reuse the SAME agent loop as the Wails app (the
runner, the tools, the ask-before-run, the skills and the subagents); the only thing that
changes is the presentation border.

The executable lives in `cmd/atenea`. Running `atenea` uses the process current
working directory as the workspace root, so the shell remains the only project
selector and no launcher or project-discovery layer is needed.

On exit, the executable calls `Engine.Shutdown` before closing the shared
session store. Shutdown stops active runs, cancels and waits for context
compactions, and disables further Bubble Tea messages once its event loop has
ended. This preserves final events and prompt checkpoints before SQLite closes.

The composer also owns built-in commands that never become model messages:
`/new` stops any in-flight run and then creates a session (otherwise the old
session would keep collecting events and win the resume-on-startup ordering),
`/resume` opens a searchable picker of TUI sessions
from the same workspace, `/compact` requests durable context compaction for the
active session, and `/model` opens a full-screen two-column picker with
providers on the left and the selected provider's models on the wider right.

At startup, the TUI allocates a fresh session ID: launching never shows
transcripts from previous runs. Earlier `tui-` sessions whose persisted
`Session.Cwd` matches the current workspace stay reachable through `/resume`,
which rehydrates the selected transcript and restores its last submitted build
or plan mode. The composer prompt history does persist across launches.

Workspace globbing for the explorer and `@` completion, plus file reading and
Chroma highlighting for the viewer, run as `tea.Cmd` work. The model renders
loading/error states and applies only results whose generation still matches
the latest request, so slow disk work cannot block or overwrite newer input.

All model, tool, filesystem, Git, provider, and error text crosses a terminal
text boundary before Markdown, Chroma, or Lip Gloss render it. The boundary
removes pre-existing ANSI/OSC sequences and C0/C1 controls (preserving line
breaks and expanding tabs), so only styles generated inside the TUI reach the
terminal.

```
wails app:  agent.Service -> runner -> EmittingStore -> Bus -> runtime.EventsEmit -> frontend web
atenea-tui: agent.Service -> runner -> EmittingStore -> Bus -> chan tea.Msg       -> Bubble Tea
```

## Pieces

- `cmd/atenea-tui/main.go` — the thin border (equivalent to `main.go` from
 Wails): loads `.env` (development builds only — `-tags production` compiles
 `dotenv.Load` to a no-op), opens the global provider service from
 `os.UserConfigDir()/atenea/providers.json` with the shared credential store
 (`credentials.json`), and preserves the previous
 environment fallback (`OPENROUTER_API_KEY` present = OpenRouter with
 `OPENROUTER_MODEL`; absent = demo without network) when no valid global
 selection is available. Starting on the demo provider seeds the transcript
 with a notice pointing at `/connect`. It diverts
 the standard log to a temporary file (do not paint over the alternative screen),
 opens the SQLite SHARED with the app via `session.OpenDefault` (fallback to
 memory if it fails, with `Close` on exit), resumes the latest TUI session for
 the current workspace, and runs `tea.NewProgram` with alt-screen. Without its
 own testable logic.
- `internal/tui/engine/engine.go` — coordinates `/compact` per session. An idle session
  starts immediately; an active run records one deduplicated pending request and
  drains it after normal completion or cancellation. Prompt execution and
  compaction share a per-session mutex so a later prompt observes the committed
  compacted baseline.
- `internal/tui/model.go`, `fold.go`, and `view.go` — render transient
  `Compaction queued` / `Compacting context` feedback without persisting it.
  Successful completion is replaced by the durable `Context.Compacted` event;
  insufficient history is informational and provider failures use the normal
  error styling.
- `internal/agent/service.go` — the UI-independent turn lifecycle shared with
  Wails. It owns modes, slash-command expansion, prompt admission, stable run
  IDs, replacement/cancellation ordering, completion hooks, and stale-run
  cleanup. Its `Mode` method is the mode hook passed to `wiring.Build`.
- `internal/tui/engine/` (package `engine`) — the terminal adapter and headless
 assembly, built with `engine.New(engine.Config{...})`. It creates
 inbox/gate/snapshots in memory, decorates the store with `EmittingStore` on a
 `event.Bus` whose `EmitFunc` bridges each `session.SessionEvent` to the
 TUI channel, and delegates runner wiring to `wiring.Build`. `SendPrompt`,
 `SendPlanPrompt`, `AcceptPlan`, and `Stop` delegate their common behavior to
 `agent.Service`; hooks retain TUI-only CWD persistence, checkpoints, literal
 prompt history, durable session mode, `RunDoneMsg`, and pending compaction. It
 satisfies the `Agent` interface of Model and
 exposes catalog, refresh, current-selection, and transactional selection
 operations to the optional model-selector boundary. The event and lifecycle
 types it produces (`EventMsg`, `RunHandle`, `RunDoneMsg`, `CompactionStatusMsg`,
 `HistoryLimit`) are the engine↔Model contract and live in
 `internal/tui/engine/protocol.go`; the `tui` package re-exports them as type
 aliases so the dependency only ever points `tui -> engine`.
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

- `internal/tui/theme/theme.go` (package `theme`) — the color palette: the
  single source of truth for every color the presentation layer paints with.
  `view.go` and the widgets compose their lipgloss styles (and the markdown
  glamour theme) from it, so changing the visual identity is a one-line edit.

- `internal/wiring` — the shared assembly extracted from `app.go`: registry de
 tools, skills and slash-commands, catalog of subagents with the propagated gate,
 system prompts (normal/plan/local) and the configured runner. `App.wire` and
 `engine.New` consume it; a tools/skills change reaches both frontends.

### User messages

User prompts render as full-width `#242424` transcript blocks inset two cells
from the chat edges. Each block has one blank row of vertical padding and three
cells of inner horizontal padding; its content starts with a faint `❯` marker
and normal-weight text. Wrapped and explicit multiline messages keep the marker
only on the first visual row and align continuation rows under the text. The TUI
does not render message timestamps.

The composer follows the same marker alignment and grows from the textarea's
visual wrapped row count, up to five rows, so narrow or `Ctrl+J` input remains
visible until scrolling begins.

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

### Tool permission panel

An ask-before-run request keeps its compact `? <tool> <summary>` activity in
the transcript and opens a blocking, borderless panel immediately above the
composer. The runner emits `Tool.Called` (running `● <tool>`) immediately
before `Tool.Permission.Requested` (`? <tool>`) for the same call, so while the
gate is open the transcript hides the running header and shows only the `?`
ask; the same call is never duplicated on two adjacent rows. Approving reveals
the running header again (the tool proceeds); denial settles it to the neutral
`– <tool> Denied by user` state. `renderTranscript` and `entryLines` share the
gating predicate so line numbering — and therefore click targeting — stays in
lockstep. The panel uses the existing two-cell composer inset and width, a
`#303030` surface, and a `#3A3A3A` command surface.

Bash permissions use a dedicated compact presentation modeled after the
terminal-native permission prompt: a full-width olive-green `Permission
required` title bar, a command surface containing only `Bash <command>`, and
the `Deny` / `Allow` buttons. The selected button uses the same green surface
with dark text; the gap between actions explicitly retains the `#303030` panel
surface. The `Bash` label uses muted `#999999` text so the command remains the
primary focus, and `Deny` remains selected by default. Request origin, working
directory, keyboard help, the queue counter, and the `once` qualifier are not
rendered for Bash. This is presentation-only: approval still applies to the
single pending execution.

Other tools retain the detailed generic panel: ANSI-green title text, the
`<tool> request` label, request origin, working directory, queue count, help,
and `Deny` / `Allow once` actions with the `›` selection marker.

Permissions are processed FIFO. The detailed generic panel shows `1 of N` for
multiple pending requests and omits the redundant `1 of 1`; the compact Bash
panel intentionally omits the counter. The generic panel identifies a request
surfaced from a child session as `Requested by subagent`, and every panel
resolves through the event's `SessionID` so a child gate is never answered on
the parent session. Bash input renders the exact command; other tools fall back
to pretty JSON. The command wraps to the available width and exposes up to four
lines; `Up`/`Down` or the mouse wheel over the command scrolls longer input and
`↓ more` marks hidden rows.

`Deny` is selected by default. `Left`/`Right` or `Tab` selects an action,
`Enter` confirms it, `Esc` denies immediately, and `y`/`n` remain silent direct
shortcuts. Clicking either action resolves it directly. While the panel owns
input, the composer stays visible and preserves its draft but is blurred and
read-only; `PgUp`/`PgDn` and the mouse wheel outside the panel continue to
scroll the transcript. The run's `working` status line is suppressed while a
permission is pending (`showsWorking`): the agent is blocked on the user, not
working, and the panel takes that row. On short terminals the composer drops its Git-summary
margin and the panel progressively omits help, command rows, and secondary
metadata while preserving the title and actions and leaving at least one
transcript row when possible.

Resolving removes the panel immediately to prevent duplicate decisions and
reveals the next queued request. Approval leaves the existing tool activity
running. Denial changes it to the neutral `– <tool> Denied by user` state; the
durable `Tool.Failed` event with `tool denied by the user` does not repaint that
expected user decision as a red system error.

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
start through `WithWorkspaceRoot(branch, dir, root)`, fed by `cmd/atenea-tui/main.go`
(branch via `git rev-parse --abbrev-ref HEAD`, `""` outside a repo; directory
home-abbreviated). After every successful `bash` tool call the model refreshes
the branch asynchronously from that workspace, so checkouts and newly created
branches update the bar without polling. On a width too narrow for both sides
the left segment truncates with `…` so the context label always fits.

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
 `Agent.SendPlanPrompt` in plan); Ctrl+C cuts and exits. During an active run,
 the first Esc shows `Esc again to cancel` for two seconds and a second Esc
 inside that window stops the run without exiting. Any other key or expiration
 disarms the confirmation and is processed normally; without an active run Esc
 does not arm it. Contextual Esc handling for menus, panels, permissions,
 explorer, and file viewer retains precedence.
 Only a `RunDoneMsg` matching the active `sessionID + runID` turns off the work
 flag, so a late close from a canceled run is ignored. `Ctrl+J` inserts a
 newline without
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
 lower-right border shows the active model and appends `· plan` in plan mode.
- The first row below the composer shows the temporary Esc cancellation prompt
 left-aligned when armed and the current Git workspace summary right-aligned to
 the same two-cell horizontal margin: unique changed files,
 additions, and deletions (`4 files changed  +128  −36`). It combines staged,
 unstaged, and non-ignored untracked files; new text files count as additions
 and binary files only affect the file count. A clean workspace, a non-Git
 directory, or a Git failure renders no summary. Narrow terminals progressively
 fall back to `4 files  +128  −36`, then `+128  −36`, then nothing, without
 wrapping. The final bottom-margin row remains blank.
- Git state is loaded asynchronously at TUI startup and refreshed after
 successful `bash`, `edit`, and `write` tools and after `/undo`. The previous
 summary remains visible until the refresh result arrives, so the composer does
 not flicker or block on Git work.

## Global provider configuration

Provider declarations live in `providers.json` under Atenea's user config
directory. Each provider has a stable `id`, display `name`,
`type: "openai-compatible"`, normalized `base_url`, optional `api_key_env`,
optional `openrouter_reasoning`, and configured model identifiers. Only the
environment-variable name is persisted; secret values never enter the file.

API keys resolve with a fixed precedence: the real environment variable wins
(the explicit, ephemeral override), then the credential stored by `/connect`
in `credentials.json` next to `providers.json` (0600, atomic rename, keyed by
provider id with a `type` discriminator so OAuth grants can join later without
a migration). Both binaries share the store: connecting from the TUI makes the
key available to the Wails app. Model discovery in the catalog uses the same
resolution.

The `/connect` command (v1: OpenRouter only) opens a full-screen panel that
lists connectable providers with their stored-credential state; selecting one
opens a masked key input — the key never touches the composer nor its
persisted history. Submitting validates the key against the provider
(`GET {base_url}/key` for OpenRouter) before storing; a rejected or failed
validation persists nothing. On success, with no active selection the provider
activates on its default model (the first curated entry, `openrouter/free`);
when it is already the selected provider the live delegate is rebuilt so a
rotated key applies without restart; a selection on another provider is left
alone. Re-running `/connect` rotates the key. There is no `/disconnect` in v1.

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
- A new prompt while an activity is running uses the shared service to cancel
  the previous run and waits for its complete shutdown before entering the runner.

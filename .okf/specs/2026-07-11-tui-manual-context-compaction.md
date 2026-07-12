---
updated_at: 2026-07-11
summary: Specification for manually queuing context compaction from the terminal UI with the /compact command.
---

# TUI manual context compaction

## Goal

Let a user explicitly compact the active session from `atenea-tui` with the
`/compact` slash command. Manual compaction must reuse the existing runner
compactor, remain isolated per session, and never become a model-visible user
message.

## User experience

- `/compact` appears in the slash-command menu as a built-in command beside
  `/new`.
- Submitting exact `/compact` does not append a user message and does not enter
  composer prompt history.
- When the session is idle, compaction starts immediately and the transcript
  shows its in-progress state followed by the durable result.
- When a turn is active, the transcript immediately shows
  `Compaction queued` and compaction starts after that turn finishes or is
  cancelled.
- Repeated `/compact` submissions while one is pending are deduplicated. The UI
  keeps one pending entry and the engine performs one compaction.
- A successful compaction replaces the transient queued state with the durable
  `Context.Compacted` result already produced by the compaction subsystem.
- If the session has insufficient history, the pending state resolves to an
  informational `Not enough context to compact` result rather than a fatal
  error.
- A real compaction failure resolves to an error entry. The previous context
  remains valid and usable.

## Architecture

### Runner boundary

`runner.Runner` exposes a public manual-compaction operation backed by its
configured `Compactor`. The operation returns a typed outcome that distinguishes
successful compaction, insufficient history, and failure. It does not create a
prompt or run the normal agent loop.

The existing compactor remains the single implementation for summary
generation, checkpoint validation, compare-and-swap persistence, and emission
of `Context.Compacted`.

### Engine coordination

`tui.Engine` recognizes exact `/compact` as a reserved built-in command. It
tracks at most one pending manual compaction per session.

If no run is active, the engine schedules compaction immediately. If a run is
active, the engine marks the session pending and emits a transient queued
message. The run cleanup path drains the pending operation after both normal
completion and cancellation.

Manual compaction and prompt submission use the existing per-session operation
mutex. This guarantees that a prompt submitted as compaction begins waits until
the compacted context is committed, so its turn observes the new baseline.

Pending state is keyed by session ID. Switching the visible session does not
cancel or redirect the operation, and completion remains associated with the
originating session.

### TUI projection

Queue and in-progress notifications are transient Bubble Tea messages. They are
not durable session events because rehydrating an old queued indicator after a
restart would be incorrect.

The model folds repeated queued messages into one transcript entry. It updates
that entry when compaction starts, when an informational no-op is returned, or
when an error occurs. The existing durable `Context.Compacted` event is the
source of truth for successful completion and replaces the transient entry.

## Command semantics

- Only exact `/compact` is reserved.
- `/compact anything` follows the normal slash-command or prompt path.
- The command is available in both build and plan modes because it modifies
  stored context rather than agent permissions.
- `/compact` is scoped to the active session at submission time.
- A second request while compaction is pending or running is a successful
  deduplicated no-op.
- `/new` behavior remains unchanged.

## Failure and concurrency behavior

- Cancelling the current run with `Esc` still drains a queued compaction after
  the run exits.
- Provider failure while generating the summary is surfaced in the transcript
  and does not commit a partial checkpoint.
- Compare-and-swap conflicts use the compactor's existing reconciliation
  behavior; the engine does not bypass store validation.
- A prompt submitted after `/compact` cannot overtake the manual compaction for
  the same session.
- Runs and compactions in different sessions may proceed independently.
- Closing the process may abandon a transient pending request; no false durable
  queued state is written.

## Testing strategy

Follow the repository TDD cycle with evidence:

1. **Safety net:** run `go test ./...` in the isolated worktree.
2. **RED:** add TUI-level tests that submit `/compact` through the model and
   engine, observe the queued state during an active run, and observe completion
   after the run ends.
3. **GREEN:** expose manual compaction and add the minimum per-session engine
   queue needed to pass the main scenario.
4. **TRIANGULATE:** cover idle execution, duplicate requests, cancellation,
   insufficient history, provider failure, session switching, exact-command
   matching, and serialization before the next prompt.
5. **REFACTOR:** consolidate command handling and transient entry folding while
   keeping `/new` behavior unchanged.
6. **Evidence:** run `go test -race ./...`, `gofmt -l .`, and `go vet ./...`,
   then inspect the rendered TUI for command-menu alignment and transcript
   spacing.

## Documentation impact

Update `.okf/architecture/tui.md` when implementation lands so the command list,
engine lifecycle, transient messages, and manual-compaction behavior match the
code.

## Out of scope

- Configurable compaction prompts or thresholds.
- Persisting queued manual operations across process restarts.
- Adding manual compaction to the Wails frontend.
- Supporting multiple queued compactions for one session.

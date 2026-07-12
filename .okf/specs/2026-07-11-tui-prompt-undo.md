---
updated_at: 2026-07-11
summary: Approved design for prompt-level undo in the TUI with durable Git-backed workspace checkpoints.
---

# Design: TUI prompt undo

## Objective

Add a native `/undo` command to `atenea-tui`. One invocation removes the most
recent TUI prompt and every response produced from it, restores the workspace
content from immediately before that prompt, and places the literal prompt back
in the composer for editing or resubmission.

The implementation follows OpenCode's proven boundary: snapshots use a private
Git repository and restore workspace content, but never mutate the user's main
Git repository metadata.

## Scope

- Implement `/undo` only in `atenea-tui`.
- Do not add a desktop command, button, keyboard shortcut, or frontend code.
- Keep shared session projections correct so opening the same session from the
  desktop does not reintroduce reverted conversation into model context.
- Support repeated `/undo` calls, one prompt checkpoint at a time.
- Do not implement `/redo`.

## User interaction

- `/undo` appears in the existing slash-command completion menu as a native
  command.
- Submitting exact `/undo`, allowing surrounding whitespace but no arguments,
  runs locally and is never sent to the model or prompt history.
- On success, the transcript is rebuilt without the reverted prompt and its
  subsequent assistant, reasoning, tool, permission, plan, and failure entries.
- The reverted literal prompt is restored into the composer with the cursor at
  the end.
- Repeating `/undo` restores the preceding prompt until no checkpoint remains.
- When no checkpoint exists, the TUI reports a local `nothing to undo` error and
  leaves the workspace and transcript unchanged.
- `/undo` is available in normal and plan mode. The restored composer text is
  the exact text originally submitted, including a literal slash command that
  was expanded before reaching the agent.

## Active turns

- If the latest prompt is still running, `/undo` first cancels that run through
  the existing engine stop path.
- Undo waits until the run has fully stopped before restoring files or changing
  the effective conversation.
- The per-session prompt-finalization and undo paths are serialized so a late
  event or checkpoint finalization cannot race with restoration.
- An unfinished checkpoint does not require an after-snapshot match; after the
  run stops, undo restores its before-snapshot directly.

## Workspace snapshots

- Add an internal checkpoint service backed by a private Git repository in the
  Atenea data directory, namespaced by a stable hash of the workspace root.
- The private repository uses its own Git directory and index while pointing
  its work tree at the user's workspace.
- Before admitting each TUI prompt, snapshot all non-ignored workspace files by
  staging them in the private index and recording the resulting tree hash.
- Include files tracked by the user's repository and non-ignored untracked
  files. Exclude files matched by the workspace's normal Git ignore rules.
- After a completed or failed run, capture an after-snapshot tree hash.
- Restoring a snapshot makes the set and content of non-ignored files equal to
  the target tree: remove paths absent from the target and check out paths in
  the target. Ignored paths remain untouched.
- Snapshot and restore preserve executable bits and symbolic links supported by
  Git.
- Snapshot data is outside the workspace and must not appear in user diffs.

## Main Git repository boundary

Checkpoint operations must not execute mutating commands against the user's
main `.git` directory. In particular, undo does not restore or change:

- the active branch;
- `HEAD` or commits;
- local references;
- the user's index or staging state;
- stashes, reflogs, remotes, or Git configuration.

This means `/undo` guarantees workspace-content restoration, not complete Git
repository-state restoration.

## Divergence safety

- A finalized checkpoint stores both its before and after tree hashes.
- Before undoing a finalized checkpoint, capture the current workspace tree and
  require it to equal the stored after tree.
- If it differs, reject `/undo` with a local error explaining that the workspace
  changed after the prompt. Do not cancel history, restore files, or alter the
  composer.
- This comparison includes non-ignored untracked files and therefore protects
  manual changes made after the agent finished.
- Ignored files do not participate in divergence detection and are never
  restored or deleted.

## Durable session model

- Record prompt checkpoints durably with the session event stream: checkpoint
  identity, literal prompt, sequence boundary, before tree, completion state,
  and optional after tree.
- Recording the before checkpoint and admitting the prompt is one serialized
  engine operation. If snapshot creation fails, the prompt is not sent.
- Finalization appends the after tree only after the run has stopped writing.
- Undo appends a durable revert marker for the latest effective checkpoint
  rather than deleting historical events or reusing sequence numbers.
- Message, runner-context, session-history, and TUI-event projections omit every
  event range covered by a revert marker.
- Shared backend projections may be updated, but no desktop frontend behavior or
  controls are added in this change.
- A new prompt after undo starts a new branch from the effective projected
  conversation; reverted ranges remain durable but invisible.
- The context epoch changes on undo so stale runner requests are discarded.

## TUI architecture

- Extend the TUI agent boundary with a local undo operation returning the
  reverted literal prompt.
- The engine owns checkpoint creation, run cancellation/waiting, divergence
  validation, snapshot restoration, durable revert, and effective-history
  reload.
- The Bubble Tea model owns command recognition, local error presentation,
  transcript replacement, and composer restoration.
- `/undo` is a native command and is not represented as a prompt-template
  command from `internal/command`.
- Existing permission and plan-approval gates retain priority. Undo is admitted
  only through normal composer submission, not while a modal decision is
  awaiting its dedicated key handling.

## Failure and atomicity

- Snapshot failure before a prompt leaves session history and workspace
  untouched and does not start the run.
- Divergence rejection leaves session history and workspace untouched.
- Restore failure does not append the revert marker; the transcript remains
  effective and the error is visible locally.
- If workspace restore succeeds but persisting the revert marker fails, the
  engine immediately attempts to restore the stored after snapshot and reports
  the persistence failure. If compensation also fails, the error must name both
  failures rather than claiming success.
- Checkpoint metadata from one workspace must never be applied to another
  workspace, even when session identifiers collide.

## Acceptance scenarios

1. Start with pre-existing tracked and untracked changes, submit a prompt that
   edits files, then `/undo`: the exact pre-prompt workspace content returns,
   the conversation range disappears, and the prompt fills the composer.
2. Submit two prompts and undo twice: each invocation restores the corresponding
   workspace tree and conversation boundary in reverse order.
3. Create or edit a non-ignored file after a completed prompt, then `/undo`: it
   is rejected and no state changes.
4. Modify ignored build output after a prompt, then `/undo`: ignored output is
   untouched and does not block undo.
5. Run `/undo` during an active tool-writing turn: the run stops before restore,
   no late events or writes survive, and the prompt returns to the composer.
6. Change the user's branch, `HEAD`, staging state, or local refs during a prompt:
   undo restores workspace content without intentionally mutating that Git
   metadata.
7. Restart the TUI after an undo: reverted conversation remains absent and the
   next prompt uses only effective context.
8. Open the shared session through the desktop backend after a TUI undo: backend
   history/context projections exclude the reverted range, with no new desktop
   UI control.

## Quality gates

- Begin with the current relevant Go tests as the safety net.
- Drive implementation from an end-to-end TUI engine scenario matching the
  first acceptance case.
- Triangulate with consecutive undo, divergence, ignored files, active
  cancellation, restart persistence, and Git-metadata preservation.
- Run concurrency-sensitive tests with `go test -race`.
- Before completion, require empty `gofmt -l .`, clean `go vet ./...`, and green
  `go test ./...`.

## TDD Cycle Evidence

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Existing tests checked before implementation | Relevant Go package tests, then `go test ./...` | Pending implementation |
| Understand | TUI command, engine, cancellation, event store, projections, and OpenCode snapshot design reviewed | `internal/tui`, `internal/session`, `internal/command`, `.okf/architecture/tui.md`, OpenCode snapshot/revert source | Behavior identified |
| RED | End-to-end prompt checkpoint and undo test written first | TUI/engine test selected during planning | Pending implementation |
| GREEN | Minimum checkpoint and undo path | Production files selected during planning | Pending implementation |
| TRIANGULATE | Safety and repeated-undo cases | Additional focused tests | Pending implementation |
| REFACTOR | Cleanup with full quality gates | `gofmt -l .`, `go vet ./...`, `go test ./...` | Pending implementation |

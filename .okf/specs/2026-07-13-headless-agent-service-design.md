---
updated_at: 2026-07-13
summary: Design for a shared headless agent service used by the Wails app and terminal UI.
---

# Shared Headless Agent Service

Status: implemented on 2026-07-13.

## Context

The Wails `App` and the TUI `Engine` independently implement the same core
conversation behavior:

- session mode storage and lookup;
- slash-command expansion;
- prompt admission into the inbox;
- starting, replacing, cancelling, and cleaning up runs;
- switching from plan mode back to normal mode after plan approval; and
- admitting the fixed implementation prompt for an approved plan.

The two adapters already contain comments describing each other as mirrors.
Keeping these implementations synchronized manually creates an increasing risk
of behavioral divergence, especially around concurrency and plan-mode state.

## Goals

- Provide one headless implementation of the shared turn lifecycle.
- Preserve the public APIs currently exposed by Wails and the TUI.
- Keep UI-specific persistence and presentation behavior in its adapter.
- Make run replacement and cancellation semantics identical in both clients.
- Give `wiring.Build` one authoritative session-mode source.

## Non-goals

- Moving TUI `/new`, `/compact`, undo, checkpoints, or composer history into the
  shared service.
- Moving Wails session titling or native workspace behavior into the service.
- Unifying Wails events and TUI messages.
- Changing runner, inbox, command, or frontend public contracts.

## Selected Approach

Create a concrete service in the UI-independent `internal/agent` package. Its
`Service` type owns shared mutable state and depends only on headless domain
components:

- `session.Inbox`;
- `runner.Runner` or a narrow run interface;
- `command.Set`; and
- internal synchronization and cancellation primitives.

The service owns session modes and active run handles. Both `App` and `Engine`
delegate the common turn lifecycle to it and remain responsible for translating
their own inputs, hooks, and completion notifications.

## Public Service Contract

The service exposes operations equivalent to:

- `Send`: execute a normal-mode user turn;
- `SendPlan`: execute a plan-mode user turn;
- `AcceptPlan`: execute the approved plan in normal mode;
- `Stop`: cancel the active run for one session;
- `StopAll`: cancel all active runs;
- `Mode`: return the current mode for `wiring.Build`; and
- a run completion handle or callback carrying stable run identity and error.

Exact Go names may be adjusted during implementation to match repository style,
but the responsibilities and dependency direction are fixed by this design.

## Turn Lifecycle

For every regular or plan-mode send, the service performs the following ordered
steps:

1. Serialize admission setup for the session.
2. Set the requested session mode.
3. Run the adapter's optional pre-admission hook.
4. Expand a registered slash command exactly once.
5. Admit the expanded prompt to `session.Inbox`.
6. Run the adapter's optional post-admission hook.
7. Create and register a new cancellable run with stable identity.
8. Cancel the previous run for the same session, if present.
9. Wait for the previous run to finish before entering the runner.
10. Execute the runner.
11. Run the adapter's completion hook.
12. Remove the run only if it remains the current run.

Deliberate cancellation and deadline expiration are clean completion states, not
user-visible execution errors. Hard errors remain available to each adapter so
Wails can publish them and the TUI can include them in `RunDoneMsg`.

## Hooks

The common service accepts narrowly scoped optional hooks instead of learning
about Wails or TUI concepts.

### Before admission

Runs synchronously before inbox admission and may fail the send.

- Wails determines whether a title job is needed and captures the session CWD.
- TUI captures the session CWD and creates the pre-prompt checkpoint.

### After admission

Runs synchronously after successful admission.

- TUI persists the literal composer prompt used by prompt history.
- Wails currently needs no post-admission action.

A post-admission persistence failure that is currently best-effort remains
best-effort in the adapter and must not prevent an already admitted turn from
running.

### After run

Runs after the runner exits and receives the run identity and result.

- Wails performs deferred first-message titling and publishes hard errors.
- TUI captures the post-run checkpoint, emits `RunDoneMsg`, and schedules any
  pending manual compaction.

The hook contract must document whether it executes before or after the service
closes the run's completion signal. TUI replacement and undo behavior require a
single unambiguous ordering.

## Plan Approval

The approved-plan implementation prompt becomes a shared constant owned by the
headless service. `AcceptPlan` always:

1. switches the session to normal mode;
2. admits the fixed implementation prompt without command expansion; and
3. starts a normal run through the same replacement lifecycle.

An approved plan is not treated as a literal composer prompt, so it does not
create TUI prompt history or an undo checkpoint and does not trigger Wails
first-message titling.

Requests to revise a plan continue to use `SendPlan` with user feedback.

## Adapter Responsibilities

### Wails `App`

The Wails adapter retains:

- frontend bindings and Wails runtime integration;
- workspace selection and live rewiring;
- session CWD capture;
- first-message title generation;
- event-bus publication;
- permission resolution;
- session history and sidebar projections; and
- terminal management.

`App.SendPrompt`, `App.SendPlanPrompt`, `App.AcceptPlan`, and `App.Stop` become
thin delegations that configure the appropriate service hooks.

### TUI `Engine`

The TUI adapter retains:

- `/new` and `/compact` interception;
- undo and workspace checkpoints;
- composer prompt history;
- run-done message publication;
- pending compaction scheduling;
- TUI event translation; and
- file, command, model, and session projections.

`Engine.SendPrompt`, `Engine.SendPlanPrompt`, `Engine.AcceptPlan`, and
`Engine.Stop` delegate common turn behavior after handling TUI-only commands.

## Wiring and Reconfiguration

`wiring.Build` receives the service's `Mode` method as its runner mode hook.
This removes the separate `modes` maps from `App` and `Engine`.

Workspace or provider rewiring may replace the runner and command set while runs
are active. The service must expose an atomic reconfiguration operation that:

1. prevents a concurrent send from capturing partially updated dependencies;
2. cancels runs using the previous runner; and
3. installs the new runner and command set together.

This preserves the existing Wails guarantee that a concurrent send cannot use a
runner wired to the old workspace. The TUI normally constructs these components
once, but uses the same contract.

## Concurrency Invariants

- Mode is set before the runner reads it for the turn.
- At most one runner invocation per session actively executes at a time.
- A replacement run waits for the previous run to finish after cancelling it.
- Completion of an old run cannot remove a newer run handle.
- `Stop` affects only the run current at the moment of lookup.
- Reconfiguration cannot expose mismatched runner and command dependencies.
- Hooks for one session are ordered with that session's admission lifecycle.
- Independent sessions may execute concurrently.

## Error Semantics

- Pre-admission hook, expansion infrastructure, or inbox admission failures are
  returned synchronously and no run starts.
- Post-admission best-effort persistence failures are logged by the adapter.
- Runner cancellation and deadline errors are normalized as clean completion.
- Other runner errors are passed to the adapter completion hook.
- Completion-hook errors that affect domain state, such as checkpoint capture,
  are merged with the runner result according to current TUI behavior.

## Testing Strategy

The implementation will not follow the repository's formal TDD evidence cycle,
as explicitly requested for this refactor. Validation still covers:

- service-level mode switching and command expansion;
- normal, plan, and approved-plan sends;
- cancellation and replacement ordering;
- stable run identity and stale-run cleanup;
- concurrent sessions;
- atomic reconfiguration;
- Wails title, event, and error adapter behavior;
- TUI checkpoint, history, compaction, and `RunDoneMsg` behavior; and
- the complete Go test, race, formatting, and vet quality gates.

Existing adapter tests remain behavioral compatibility tests. Shared behavior
should move to focused service tests where practical, while adapter-specific
assertions remain next to `App` and `Engine`.

## Documentation Impact

Implementation updates must describe the shared service and dependency direction
in:

- `.okf/architecture/agent-loop.md`; and
- `.okf/architecture/tui.md`.

The documentation must make clear that Wails and TUI are adapters over one
headless turn lifecycle rather than independent loop implementations.

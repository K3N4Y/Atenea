---
updated_at: 2026-07-22
summary: Ownership, seams, and internal structure of the durable session module.
---

# Session module

`internal/session` is the durable domain module for an agent conversation. Its
interface contains the event model, the `Store`, `CompactionStore`, and
`UndoStore` persistence contracts, and the input and permission contracts used
by the runner. `internal/session/runner` consumes those contracts; Wails, TUI,
and event packages adapt them to their respective interfaces.

## Persistence seam

`Store` is a real seam with two adapters:

- `MemoryStore`, used by isolated agents, tests, and the non-durable fallback;
- `SQLiteStore`, used by Wails and the TUI for shared durable history.

Contract suites exercise both adapters through `Store`, `CompactionStore`, and
`UndoStore`. The event log is their source of truth. Message history, effective
events after undo, pending tools, session summaries, runner context, and
compaction validity are projections of that log.

The adapters intentionally remain in the `session` package. They share private
projection, compaction-validation, and cloning rules. Moving them to a sibling
package would require exporting those implementation details or duplicating
them, enlarging the interface and risking semantic drift. The package keeps an
internal seam instead:

- `projection.go` owns adapter-independent folds and compaction reference
  validation;
- `clone.go` owns defensive copies at in-memory and projection seams;
- `memstore.go` and `sqlitestore.go` own adapter-specific state and I/O;
- `compaction.go` and `undo.go` own their domain types and validation;
- `open.go` owns process-level default paths and durable-to-memory fallback.

This arrangement preserves one small external persistence interface while
keeping shared correctness rules private and local.

## Inbox and permissions

`Inbox` describes how durable user input reaches a session run. Its current
adapter is in memory, but the contract belongs beside the session identifiers,
delivery semantics, and runner-facing persistence interface it coordinates.

Ask-before-run lives in its own module: `internal/permission` owns the
`Policy` (Allow/Ask/Deny verdict per tool call) and the `Gate` that correlates
a session tool call with a user decision. The gate is not part of `Store`:
pending approvals are intentionally ephemeral, and interrupted durable tool
calls are reconciled by the runner after restart. The runner consumes both as
optional dependencies and keeps ownership of the durable event order
(`Tool.Permission.Requested` before the outcome). Design and classification:
[single permission gate](../specs/2026-07-23-single-permission-gate.md).

## Package seams

The existing child packages are the useful behavioral seams:

- `session/runner` owns provider turns, tool settlement, continuation, and
  compaction orchestration;
- `session/prompt` owns embedded system-prompt selection;
- `session/subagent` owns child-agent execution over isolated session adapters.

These packages depend on `session`; the domain module does not depend on them.
Wails-specific history, deletion, titling, and SQLite watching live in
`internal/wailssession`, keeping UI lifecycle behavior outside the durable
domain.

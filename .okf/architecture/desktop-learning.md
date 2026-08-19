---
updated_at: 2026-08-18
summary: Durable learning runs, /agents model configuration, human approval, TUI audit controls, and bounded workspace lesson selection.
---

# Workspace learning

Workspace learning is owned by `internal/learning`; it is intentionally absent
from `agentcore` but shared by the desktop and terminal hosts. `/learn` captures
the latest completed durable session sequence into a bounded immutable input
before returning. A
workspace FIFO worker then makes one tool-free provider request using the
provider/model snapshot captured at enqueue time.

Runs, candidates, decisions, provenance, usage, and approved lessons are stored
outside session events. Production uses the private `learning.json` data file,
serialized across desktop and terminal processes with an OS file lock; each
operation reloads disk state while holding that lock before atomically replacing
the file. Stable per-run advisory leases are held from before a run is published
until it completes or leaves its queue, so startup recovery only interrupts
orphaned work and never work owned by another live process. Service shutdown
durably settles all owned queued and active runs before returning. Tests use the
same store contract in memory. Approval atomically
creates one workspace-scoped lesson and is idempotent. Rejected, disabled, or
deleted lessons cannot be selected.

In the TUI, `/agents` exposes a `learn` model role alongside the subagents. Its
provider, model, and reasoning effort are persisted through the same agent-model
configuration; clearing the override makes learning inherit the active
conversation provider. `/learn` does not open a picker: it resolves that role
and queues extraction through the engine without blocking Bubble Tea. The
resolved provider is captured as an isolated run snapshot, so learning never
changes the model serving the conversation. `/learned` opens a workspace audit
overlay: Enter approves a ready candidate, retries a failed/interrupted run with
the current `learn` role model, cancels active work, or toggles an approved
lesson; `d` rejects a ready candidate or deletes a lesson. Background learning
transitions invalidate the open overlay through the engine event channel and
trigger an asynchronous refresh.

Selection is lexical, deterministic, limited to five lessons and an estimated
1,500 tokens, and never calls a provider. The rendered section is explicitly
user-approved guidance and belongs after project instructions and before the
runtime environment in a future turn's system prompt.

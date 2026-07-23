---
updated_at: 2026-07-22
summary: Durable session lifecycle and projections used by the Wails desktop adapter.
---

# Wails session lifecycle

`internal/wailssession.Manager` is the seam between Wails bindings and durable
session behavior. The module owns history projections, deletion coordination,
first-prompt metadata, automatic titles, and observation of writes made by
another process to the shared SQLite store.

A prompt creates a `Turn`. Its `BeforeAdmit` method runs while
`wailsworkspace.Manager` holds the workspace lifecycle lock, determines whether
the session is new, and records the active workspace before the agent promotes
the prompt. `AfterRun` generates a title only for that first prompt, only after
the current agent run has finished, and preserves the store's first-message
fallback when generation is empty or fails.

The interface deliberately exposes session operations rather than the raw
store: `List`, `History`, `Delete`, `Turn`, and `Watch`. The manager receives the
active-root function and agent forget function at construction, so it can keep
ordering and durability rules local without depending on Wails runtime or the
complete agent implementation. The production provider titler receives a live
provider/model snapshot function, allowing provider changes without coupling
the session lifecycle to provider configuration.

`main.App` retains the public Wails bindings, error publication for completed
agent runs, and composition of agent hooks. Wails startup retains the runtime
context and delegates cross-process session observation to the manager.

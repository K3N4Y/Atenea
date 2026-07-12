---
updated_at: 2026-07-11
summary: TDD implementation plan for real manual context compaction in the terminal UI.
---

# TUI Manual Context Compaction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a built-in `/compact` TUI command that performs real context compaction immediately or once after the active turn, with deduplicated queued feedback.

**Architecture:** Complete the existing durable compaction seam with a provider-backed compactor installed by shared wiring, then expose manual compaction through `runner.Runner`. Keep queue ownership and per-session serialization in `tui.Engine`; keep transient queued/running feedback in the Bubble Tea model while successful completion continues to use durable `Context.Compacted` events.

**Tech Stack:** Go 1.23+, Bubble Tea, existing `llm.Provider`, session stores, runner compaction checkpoint contracts, table-driven Go tests.

---

### Task 1: Provider-backed compactor and manual runner API

**Files:**
- Create: `internal/session/runner/compactor.go`
- Create: `internal/session/runner/compactor_test.go`
- Modify: `internal/session/runner/runner.go`
- Modify: `internal/session/runner/turn.go`

- [ ] **Step 1: Write failing compactor tests**

Add tests that seed `old user -> old assistant -> current user`, run manual
compaction, and assert one provider summary request, one durable
`Context.Compacted`, preservation of the current user activity, and a typed
`session.ErrNoCompactableHistory` result when no earlier completed activity
exists. Add a failure test with malformed summary JSON and assert no checkpoint
is committed.

- [ ] **Step 2: Run RED**

Run: `go test -run 'TestRunner_CompactNow|TestCompactor_' -v ./internal/session/runner`

Expected: FAIL because `Runner.CompactNow`, the real compactor, and compactor
installation do not exist.

- [ ] **Step 3: Implement minimal compactor**

Implement `SetCompactor(Compactor)` and `CompactNow(ctx, sessionID)` on
`Runner`. Add a provider-backed compactor that:

```go
type ManualCompactionResult int

const (
    ManualCompactionCompleted ManualCompactionResult = iota
    ManualCompactionNotNeeded
)
```

The compactor reads `session.ContextForRunner`, finds the last user activity,
summarizes only the completed prefix, validates the exact nine-field structured
JSON using `session.DecodeStructuredSummary`, and commits a
`session.CompactionCheckpoint` through `session.CompactionStore`. It returns
`session.ErrNoCompactableHistory` without calling the provider when the prefix
is empty. Summary generation must require `llm.StepEnded` and use the active
epoch model.

- [ ] **Step 4: Preserve automatic compaction compatibility**

Keep `Compactor.NeedsCompaction(req)` for the existing automatic runner seam.
`turn.go` continues calling the same `Compact` method. The concrete compactor
uses `llm.ContextWindow` and `llm.EstimateRequestTokens` for its preventive
decision and returns false for unknown model windows.

- [ ] **Step 5: Run GREEN and package gate**

Run: `go test -run 'TestRunner_CompactNow|TestCompactor_' -v ./internal/session/runner && go test ./internal/session/runner`

Expected: PASS.

- [ ] **Step 6: Commit**

Commit message: `feat(runner): add manual context compaction`

### Task 2: Install compaction in shared wiring

**Files:**
- Modify: `internal/wiring/wiring.go`
- Modify: `internal/wiring/wiring_test.go`

- [ ] **Step 1: Write failing wiring test**

Build the shared agent with a recording provider and a memory store, seed a
compactable session, call `built.Runner.CompactNow`, and assert the provider is
called and `Context.Compacted` is durable.

- [ ] **Step 2: Run RED**

Run: `go test -run TestBuild_InstallsContextCompactor -v ./internal/wiring`

Expected: FAIL because `Build` does not install a real compactor.

- [ ] **Step 3: Install the concrete compactor**

Construct the compactor from `cfg.Store` and `cfg.Provider` in `Build`, then
call `runner.SetCompactor`. Do not create a second provider or store wrapper.

- [ ] **Step 4: Run GREEN**

Run: `go test -run TestBuild_InstallsContextCompactor -v ./internal/wiring && go test ./internal/wiring`

Expected: PASS.

- [ ] **Step 5: Commit**

Commit message: `feat(wiring): install context compactor`

### Task 3: Queue and deduplicate `/compact` in Engine

**Files:**
- Modify: `internal/tui/engine.go`
- Modify: `internal/tui/engine_test.go`

- [ ] **Step 1: Write the E2E-style RED test**

Drive `Engine.SendPrompt("s1", "/compact")` during a blocked provider turn.
Assert the command does not enter the inbox or composer history, emits exactly
one `CompactionStatusMsg{State: queued}`, deduplicates repeated submissions,
and invokes compaction once after the run finishes. Add an idle-session test
that starts compaction immediately.

- [ ] **Step 2: Run RED**

Run: `go test -run 'TestEngine_Compact' -v ./internal/tui`

Expected: FAIL because `/compact` follows the normal prompt path and no status
message or pending queue exists.

- [ ] **Step 3: Implement per-session coordination**

Add `pendingCompactions map[string]bool` and reuse the existing per-session
operation mutex. Exact `/compact` calls a dedicated request method. Active runs
set one pending bit and emit `queued`; idle sessions launch compaction. Run
cleanup drains the bit after normal completion or cancellation. Prompt launch
and compaction acquire the same session operation lock so the next prompt
cannot overtake compaction.

- [ ] **Step 4: Emit typed transient outcomes**

Define a Bubble Tea message carrying session ID and one of `queued`, `running`,
`not-needed`, or `failed`. Successful completion relies on the existing durable
`Context.Compacted`; no synthetic durable event is appended by the engine.

- [ ] **Step 5: Run GREEN**

Run: `go test -run 'TestEngine_Compact' -v ./internal/tui && go test ./internal/tui`

Expected: PASS.

- [ ] **Step 6: Commit**

Commit message: `feat(tui): queue manual context compaction`

### Task 4: Built-in command and transcript feedback

**Files:**
- Modify: `internal/tui/complete.go`
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/fold.go`
- Modify: `internal/tui/view.go`
- Modify: `internal/tui/model_test.go`

- [ ] **Step 1: Write failing model tests**

Assert `/compact` appears in the slash menu with a description, Enter submits
it immediately like `/new`, the command is not stored in prompt history, one
`Compaction queued` entry is shown for duplicates, `running` updates the same
entry, `not-needed` resolves it informationally, `failed` resolves it as an
error, and `Context.Compacted` replaces it with the successful durable result.

- [ ] **Step 2: Run RED**

Run: `go test -run 'TestModel_Compact|TestModel_SlashMenu.*Compact' -v ./internal/tui`

Expected: FAIL because the menu and fold do not recognize manual compaction.

- [ ] **Step 3: Implement built-in and fold**

Treat `/compact` as a built-in exact command. Add one compact-status entry kind
with stable rendering and mutate it by session-scoped status messages. Clear the
input without setting `working` as a normal agent prompt; the status entry owns
the visible progress.

- [ ] **Step 4: Run GREEN and visual regression tests**

Run: `go test -run 'TestModel_Compact|TestModel_SlashMenu.*Compact' -v ./internal/tui && go test ./internal/tui`

Expected: PASS with menu alignment and transcript snapshots unchanged except
for the new command/status behavior.

- [ ] **Step 5: Commit**

Commit message: `feat(tui): render manual compaction status`

### Task 5: Triangulate concurrency, errors, and documentation

**Files:**
- Modify: `internal/session/runner/compactor_test.go`
- Modify: `internal/tui/engine_test.go`
- Modify: `internal/tui/model_test.go`
- Modify: `.okf/architecture/tui.md`

- [ ] **Step 1: Add edge cases**

Cover cancellation draining, prompt serialization after compaction, independent
sessions, `/compact anything` remaining a normal prompt, duplicate requests
while already running, provider failure preserving the epoch, insufficient
history, and changing the visible session before completion.

- [ ] **Step 2: Run TRIANGULATE gate**

Run: `go test -race -run 'TestRunner_CompactNow|TestCompactor_|TestEngine_Compact|TestModel_Compact' -v ./internal/session/runner ./internal/tui`

Expected: PASS without races.

- [ ] **Step 3: Update architecture**

Document `/compact`, transient status messages, engine-owned deduplication,
per-session serialization, and durable `Context.Compacted` completion in
`.okf/architecture/tui.md`.

- [ ] **Step 4: Commit**

Commit message: `docs: document TUI manual compaction`

### Task 6: Refactor and final evidence

**Files:**
- Modify only files already touched when cleanup is necessary.

- [ ] **Step 1: Refactor without behavior changes**

Remove duplicate command checks, keep compactor selection helpers focused, and
preserve existing `/new`, plan-mode, follow-up cancellation, and event folding.

- [ ] **Step 2: Run focused and broad gates**

Run:

```bash
gofmt -l .
go vet ./...
go test -race ./...
```

Expected: `gofmt -l .` has no output; vet and all tests pass.

- [ ] **Step 3: Inspect the TUI output**

Run the relevant model/view tests with `-v` and inspect stripped rendered output
for command-menu columns, status spacing, and replacement of queued state.

- [ ] **Step 4: Commit final cleanup**

Commit message: `refactor(tui): finalize manual compaction flow`

- [ ] **Step 5: Push and open pull request**

Push `posthog-code/tui-manual-compact` and create a pull request against the
repository default branch. Include the TDD evidence table and the required
PostHog Code footer in the description.

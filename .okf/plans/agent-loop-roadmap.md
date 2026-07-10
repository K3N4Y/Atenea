---
updated_at: 2026-07-09
summary: Roadmap for implementing Atenea’s main agent loop.
---

# Roadmap: Athena's agent main loop

Plan to build the loop described in `../architecture/agent-loop.md`. Each milestone is
small, testable and is attacked with the TDD loop of `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).

Order by dependencies: each milestone is supported by the previous one and leaves something verifiable
with `go test`. You don't touch Wails or a real supplier until you have the green loop
against fakes.

## Principles of the plan

- Build from the inside out: types -> store -> provider -> publisher ->
 tools -> turn -> loop -> control -> faults -> Wails.
- Everything concurrent (`runTurn`, tools settle) is tested with `-race`.
- The provider and the store start as fakes in memory; the real thing (Anthropic,
 SQLite) arrives at the end, when the loop is already green.
- A milestone does not close without its table `TDD Cycle Evidence`.

## Milestone View

```text
M0 Scaffolding
M1 Tipos + Store en memoria
M2 Provider + fake scriptable
M3 Publisher (eventos)
M4 Tool registry + settle
M5 Un turno (runTurn) feliz
M6 Loop externo (Run) + MaxSteps
M7 Senales de control (rebuild / compaction)
M8 Interrupcion + manejo de fallos
M9 Cableado Wails (EventBus + SendPrompt)
M10 Store SQLite + provider real
```

M1..M8 are the heart and are made 100% with tests, without UI. M9 and M10 change
fakes for real implementations without touching the loop logic.

---

## M0 — Scaffolding

**Meta**: Package structure and dependencies ready.

- Create `internal/{session,session/runner,llm,tool,event}`.
- `go get golang.org/x/sync/errgroup`.
- A trivial per-package test to set the package name.

**Done when**: `go test ./...` and `go vet ./...` pass clean; `gofmt -l .` empty.

## M1 — Types + Store in memory

**Goal**: durable backbone. `Session`, `Message`, `Seq`, `SessionEvent` and an in-memory
`Store` that aggregates events and reprojects messages.

- NETWORK: `AppendEvent` assigns `Seq` monotonic and `Messages(sinceSeq)` returns in
 order.
- TRIANGULATE: non-existent session; `sinceSeq` greater than the last; concurrency
 of appends (`-race`).

**Done when**: the store is the only source of truth and you can rebuild the
history from scratch.

## M2 — Provider + fake scriptable

**Meta**: interface `Provider` and a fake that emits a sequence of `llm.Event`
per channel (text, reasoning, tool-call, step-finish).

- NETWORK: `Stream` returns scripted events and **closes** the channel upon
 completion.
- TRIANGULATE: cancel `ctx` cuts the stream before completion; empty stream.

**Done when**: the loop will be able to run against deterministic scenarios without a network.

## M3 — Publisher (events)

**Goal**: `publish.go` translates `llm.Event` to durable `SessionEvent` and
buffers deltas.

- NETWORK: a sequence `Text.Started/Delta/Delta/Ended` produces the events and a final
 event with the full text concatenated.
- TRIANGULATE: reasoning same as text; `step-finish` -> `Step.Ended` with
 tokens; tool input deltas -> `Tool.Input.*`.
- Maintains `assistantMessageID` of the turn and map of tool calls by `callID`.

**Done when**: given a fake stream, the persisted events match 1:1 with
the contract names (`Step.* / Text.* / Reasoning.* / Tool.*`).

## M4 — Tool registry + settle

**Goal**: `Registry.Materialize(perms)` returns `Definitions` and `Settle`.

- NETWORK: `Settle` from a known tool executes and returns `Result`.
- TRIANGULATE: tool denied by permissions does not appear in `Definitions`; tool
 unknown/stale returns error **no** side effects; large output is
 bounded via `ToolOutputStore`.
- First simple builtin (e.g. `echo` or `read`) to have something executable.

**Done when**: the registry validates against the announced set before acting.

## M5 — A happy turn (`runTurn`)

**Goal**: assemble M1..M4 in one turn: build `llm.Request` from history,
call `Stream` once, consume, seat concurrent tools with `errgroup`,
return `needsContinuation`.

- RED: turn with text only -> persist events and return `needsContinuation =
  false`.
- TRIANGULATE: turn with a local tool-call -> register `Tool.Called` before
 execute, publish `Tool.Success`, return `true`; **two** tool calls run
 concurrently and the turn waits for both (`-race`); tool fails -> `Tool.Failed`.
- Provider-executed vs local: provider-executed is only persisted.

**Done when**: an isolated turn is correct against fake, including
tool concurrency.

## M6 — External Loop (`Run`) + MaxSteps

**Goal**: the `Inbox` (queue/steer) and the double loop with `MaxSteps = 25`.

- NETWORK: `Admit(queue)` + `Run` processes a prompt and remains idle.
- TRIANGULATE: continues as long as there are tool calls; `steer` admitted during the
 run enters the following continuation; wizard text **not** continuous
 only; second `queue` opens new activity; exceed steps -> `StepLimitExceededError`.

**Done when**: the loop drains the inbox with the same semantics as the pseudocode of
the architecture.

## M7 — Control signals

**Goal**: `errRebuildTurn` and `errContinueAfterCompaction` with `errors.Is`, plus
checks from `ContextEpoch`.

- RED: if the agent/model switches between prepare and call, the turn rebuilds
 (does not call the provider with request stale).
- TRIANGULATE: epoch mismatch forces rebuild; overflow before wizard message
 compact and retry once.

**Done when**: no request is executed representing old session state.

## M8 — Interrupt + fault handling

**Goal**: cancellation by `context` and clean closure of ambiguous state.

- RED: cancel `ctx` mid-shift -> in-flight tools are interrupted and `Tool.Failed` is marked
.
- TRIANGULATE: provider error; checked tool executed by provider that
 never resolves; `failInterruptedTools` at the beginning cleans remains of a previous run
 (resume after crash).

**Done when**: after any failure, the history is not left with hanging tools.

## M9 — Wails Wiring

**Goal**: connect the loop to the real app. The loop logic **does not change**.

- `internal/event`: `EventBus` forwards `SessionEvent` with `runtime.EventsEmit`.
- `app.go`: `SendPrompt` does `Admit(queue)` and starts `Run` in goroutine;
 stop button cancels `ctx`.
- Frontend listens to `session:<id>` and maps `Step.* / Text.* / Tool.*` streaming.

**Done when**: a prompt from the UI produces visible streaming; the runner is
tested against a `EventBus` fake, not against Wails.

## M10 — Store SQLite + provider real

**Goal**: change fakes for real implementations behind the same
interfaces.

- `Store` SQLite in the app's data directory (resumeable after reboot).
- Real `Provider` adapter (Claude / Anthropic) that maps its stream to
 `llm.Event`.

**Done when**: M1..M8 tests remain green with the real store; an
end-to-end prompt works against the actual provider.

---

## Critical path

`M1 -> M2 -> M3 -> M5 -> M6` is the minimum for a loop that responds with text and
tools against fakes. M4 is needed before M5 for tool calls. M7 and M8
toughen the loop. M9 makes it visible. M10 makes it real and persistent.

## How to measure progress

Each closed milestone leaves:

- your green tests (`go test ./...`, with `-race` where applicable);
- empty `gofmt -l .` and clean `go vet ./...`;
- your `TDD Cycle Evidence` table in the response/PR.

## Sources

- Architecture: `../architecture/agent-loop.md`
- Way of working: `AGENTS.md`
- Reference loop: `../architecture/opencode-agent-loop.md`

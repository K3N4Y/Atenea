---
updated_at: 2026-07-09
summary: Reference architecture for the OpenCode agent loop.
---

# Agent loop architecture in OpenCode

Researched on 2026-06-19 on the official OpenCode documentation and the
upstream source code `anomalyco/opencode` in the `dev` branch.

This document goes one level down from `opencode-architecture.md`: it describes
how the internal loop that processes an agent session runs.

## Short Summary

The agent loop lives mainly in:

- `packages/core/src/session/runner/llm.ts`
- `packages/core/src/session/runner/publish-llm-event.ts`
- `packages/core/src/session/input.ts`
- `packages/core/src/session/history.ts`
- `packages/core/src/session/context-epoch.ts`
- `packages/core/src/tool/registry.ts`

The orchestration unit is `SessionRunner.run({ sessionID, force? })`. That
runner drains durable work from a session until there is no open activity:
promotes pending prompts, sets up a supplier shift, streams events from the
LLM, runs local tools, persists results, and decides if it needs another shift.

OpenCode does not implement a simple in-memory loop like `while model wants tools`.
It implements a durable loop around events, session tables, projected
history, and retries upon concurrent context changes.

## High level view

```text
API / TUI / SDK
  |
  | admite prompt durable: delivery = queue | steer
  v
SessionInput
  |
  | SessionRunner.run(sessionID)
  v
Loop externo de actividad
  |
  | mientras haya actividad abierta
  v
Loop de pasos, max 25
  |
  | runTurn(sessionID, promotion)
  v
Turno del proveedor
  |
  | arma request: system + history + tools + model
  | llm.stream(request)
  v
Publicador de eventos
  |
  | Step/Text/Reasoning/Tool events
  v
Tool settlement
  |
  | ejecuta tools locales en fibers
  | publica tool-result sintetico
  v
Continuacion
  |
  | si hubo tool calls locales o steer pendiente, otro turno
  | si no, idle
```

## Key concepts

### Session

The session is the durable aggregate. It has ID, workspace location, agent,
model, messages, events and related state. The runner always validates that the
session still belongs to the same directory/workspace before running a turn.

### Input durable

The prompts do not enter directly into the model. They are first supported as
`SessionInput.Admitted` with a `delivery`:

- `queue`: main prompt in queue. The runner processes one per open activity.
- `steer`: user addressing accepted while there is already activity. It is
 promoted to affect the next continuation.

The input has two important sequences:

- `admitted_seq`: sequence of the event that the prompt admitted.
- `promoted_seq`: sequence assigned when the prompt becomes a message
 visible to the runner.

This allows the loop to be replayable and to accept concurrent steering
without unsafely mixing it with a turn that has already been prepared.

### Context epoch

`SessionContextEpoch` maintains a snapshot of the system context for the session:
baseline, snapshot, agent, `baselineSeq` and revision. The loop uses it to:

- initialize the context if it does not exist;
 - reconcile file/instruction/context changes;If the revision or agent no longer match, the turn is discarded and rebuilt
from durable state.

### Projected History

`SessionHistory.entriesForRunner()` reads persisted messages in
sequence order. Take into account:

- the last compaction message;
- the `baselineSeq` of the context;
- `system` messages after the baseline;
- no-system messages necessary for the model.

The runner converts that history with `toLLMMessages(context, model)` before
constructing the request to the provider.

### Tool materialization

`ToolRegistry.materialize(permissions)` produces two things:

- `definitions`: schemas of tools that are announced to the model;
- `settle(input)`: closed function on the announced set to execute and
 set a tool call.

The registry brings together built-in tools and locally registered tools. If a tool is
completely denied for permissions, it is removed from `definitions`. If the model
attempts to call an unknown tool or stale, the settlement returns an
error result instead of executing side effects.

## Loop pseudocode

```ts
run({ sessionID, force }) {
  hasSteer = SessionInput.hasPending(sessionID, "steer")
  hasQueue = hasSteer ? false : SessionInput.hasPending(sessionID, "queue")

  if (!force && !hasSteer && !hasQueue) return

  failInterruptedTools(sessionID)

  promotion = hasSteer ? "steer" : hasQueue ? "queue" : undefined
  openActivity = force || hasSteer || hasQueue

  while (openActivity) {
    needsContinuation = true

    for (step = 0; step < MAX_STEPS; step++) {
      needsContinuation = runTurn(sessionID, promotion)
      promotion = "steer"

      if (!needsContinuation) {
        needsContinuation = SessionInput.hasPending(sessionID, "steer")
      }

      if (!needsContinuation) break
    }

    if (needsContinuation) throw StepLimitExceededError(MAX_STEPS)

    openActivity = SessionInput.hasPending(sessionID, "queue")
    promotion = openActivity ? "queue" : undefined
  }
}
```

`MAX_STEPS` is `25`. That limit prevents infinite model/tool/continuation loops.

## What does a turn make (`runTurnAttempt`)

A shift is an explicit call to the supplier. In practical terms:

1. Load the session and validate that the local workspace matches.
2. Select the configured agent.
3. Load system context, skills guidance and references.
4. Initialize or prepare `SessionContextEpoch`.
5. Promotes durable inputs:
 - `steer`: promotes all steering inputs until a cutoff.
 - `queue`: promotes the next queued prompt and then steering until cutoff.
6. Reread the session and abort the turn if agent or model changed.
7. Solve the model.
8. Read projected history for the runner.
9. Materialize tools with agent permissions.
10. Build `LLM.request`:
    - `model`;
    - provider options, incluyendo prompt cache key para OpenAI;
    - system parts: prompt del agente + baseline de contexto;
    - messages: historial convertido a formato LLM;
    - tools: schemas materializados.
11. Run compaction if the request needs it and rebuild the shift if
    cambia el estado.
12. Create an event publisher.
13. Verify that the context epoch is still valid.
14. Call `llm.stream(request)` exactly once.
15. For each event in the stream:
    - publica el evento como evento durable de sesion;
    - si es `tool-call` local, ejecuta `toolMaterialization.settle(...)` en un
      fiber;
    - publica un `LLMEvent.toolResult(...)` sintetico con el resultado de la
      tool.
16. Wait for the tools fibers to finish.
17. Handles failures, interrupts, context overflow and unresolved tools.
18. Returns `true` if continuation is needed; `false` if the turn left the session
    estable.

The important rule: the provider produces one shift, not the entire session. The runner's loop
 decides whether to invoke another turn after setting tools or steering.

## Published events

`publish-llm-event.ts` translates provider events to durable
session events. Maintains the `assistantMessageID` of the turn and an internal map of tool
calls by `callID`.

Main events:

- `SessionEvent.Step.Started`
- `SessionEvent.Step.Ended`
- `SessionEvent.Step.Failed`
- `SessionEvent.Text.Started`
- `SessionEvent.Text.Delta`
- `SessionEvent.Text.Ended`
- `SessionEvent.Reasoning.Started`
- `SessionEvent.Reasoning.Delta`
- `SessionEvent.Reasoning.Ended`
- `SessionEvent.Tool.Input.Started`
- `SessionEvent.Tool.Input.Delta`
- `SessionEvent.Tool.Input.Ended`
- `SessionEvent.Tool.Called`
- `SessionEvent.Tool.Success`
- `SessionEvent.Tool.Failed`

Fragments of text, reasoning and input from tools are buffered to be able to
emit deltas and also a final event with the complete content. Provider event
`step-finish` terminates at `SessionEvent.Step.Ended` with tokens from
input/output/reasoning/cache.

## Execution of tools

There are two kinds of tool result:

- **Provider-executed**: The provider executes the tool or returns the result. The
 runner persists it, but does not launch local `settle()`.
- **Local tool call**: OpenCode executes it through `ToolRegistry.settle()`.

For local tool calls:

1. The publisher permanently registers `Tool.Called`.
2. The runner obtains the `assistantMessageID` associated with `callID`.
3. Run `toolMaterialization.settle(...)`.
4. The settlement validates that the tool exists and is the same as what was announced.
5. Run the implementation of the tool.
6. Convert structured output to `ToolResultValue`.
7. Limit/store large output with `ToolOutputStore.bound(...)`.
8. Post `Tool.Success` or `Tool.Failed`.
9. Mark `needsContinuation = true` for the model to see the result in a
 next turn.

Local execution occurs at `FiberSet`, so several tool calls can
run concurrently. Before continuing, the runner waits for everyone to settle
.

## Continuation conditions

The turn returns `true` when:

- there was a local `tool-call` and there was no supplier error;
- or, upon completion, there is pending steering that must enter the next turn.

The loop does not continue by simple wizard text. If the model responds to text and
does not ask for tools, the shift ends and the activity is closed, unless there is a
`steer` pending.

After closing an activity, the loop checks if there is another prompt `queue`
 pending. If it exists, open a new activity with `promotion = "queue"`.

## Internal transitions

`runTurn` uses internal errors as control signals:

- `RebuildPreparedTurn`: something changed while the shift was being prepared. Examples:
 different agent/model, epoch mismatch, or concurrent promotion.
 is prepared again from DB/events.
- `ContinueAfterOverflowCompaction`: there was context overflow before starting the
 wizard message, it is compacted and retried once by the
 post-compaction route.

This avoids continuing with a request that no longer represents the actual state of the
session.

## Fault Handling

The runner has explicit paths to:

- provider errors;
- context overflow;
- interruptions;
- question rejected by the user;
- tool fibers interrupted;
- tool failures;
- tools that the provider marked as executed but never resolved;
- step limit exceeded.

When a turn fails, the publisher attempts to close unresolved tools with
`Tool.Failed` to avoid leaving the history in an ambiguous state.

## Diagram of a shift with local tools

```text
runTurn
  |
  | materialize tools
  | build LLM.request
  v
llm.stream(request)
  |
  | text/reasoning deltas
  v
publish SessionEvent.*
  |
  | tool-call local
  v
ToolRegistry.settle(call)
  |
  | output/error
  v
publish LLMEvent.toolResult(...)
  |
  | SessionEvent.Tool.Success/Failed
  v
await all tool fibers
  |
  | needsContinuation = true
  v
next runTurn with updated history
```

## Architectural implication

The OpenCode loop is designed for a durable scheduling agent, not for
an ephemeral chat:

- source state is events/session tables;
- request to provider is rebuilt every turn;
- tool calls are logged before side effects;
- continuations are triggered only after posting results;
- concurrent agent/context changes force rebuild;
- step limit protects against loops productive;
- the server can expose progress by SSE because each relevant fragment
 is published as an event.

If this Wails project integrates OpenCode, the most stable frontier remains the
HTTP/OpenAPI server. To observe the loop, it is advisable to subscribe to the
event stream and map `Step.*`, `Text.*` and `Tool.*` to the UI instead of trying to invoke
the TypeScript core directly.

## Sources consulted

- Server docs: https://opencode.ai/../server/
- Tools docs: https://opencode.ai/../tools/
- Permissions docs: https://opencode.ai/../permissions/
- Main runner: https://github.com/anomalyco/opencode/blob/dev/packages/core/src/session/runner/llm.ts
- LLM Event Publisher: https://github.com/anomalyco/opencode/blob/dev/packages/core/src/session/runner/publish-llm-event.ts
- Session input: https://github.com/anomalyco/opencode/blob/dev/packages/core/src/session/input.ts
- Session history: https://github.com/anomalyco/opencode/blob/dev/packages/core/src/session/history.ts
- Context epoch: https://github.com/anomalyco/opencode/blob/dev/packages/core/src/session/context-epoch.ts
 - Tool registry: https://github.com/anomalyco/opencode/blob/dev/packages/core/src/tool/registry.ts

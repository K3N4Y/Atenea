---
updated_at: 2026-07-09
summary: Design specification for agent context compaction.
---

# Spec: agent context compaction

Date: 2026-07-09

## Status

Approved design to convert the existing `runner.Compactor` seam into a real, durable, preventive and reactive compaction. This document defines
behavior and contracts; It is not an implementation plan.

## Problem

The runner rebuilds each turn from the durable history and can now ask
a `Compactor` to reduce the context before retrying. The
real implementation does not exist yet. A long session may approach the model window
or receive an overflow from the provider, without a safe way to preserve the
useful state of the conversation and continue.

Compaction should reduce the request sent to the model without deleting the
event log, without breaking tool calls and without hiding from the user that the context
effectively changed.

## Objectives

- Preventively compact when the estimated request reaches 80% of the
 total model window.
- Reactively recover in the event of a real overflow reported by the provider.
- Generate a structured summary with the same provider and agent model.
- Literally preserve the activity started by the last message from the
 user as long as it enters the budget.
- Persist each compaction as a durable, atomic and auditable checkpoint.
- Keep all original events visible and available for audit.
- Rebuild the same effective context after restarting the application.
- Avoid compaction loops and partial writes in the event of failures.

## Non-objectives

- Delete, rewrite or archive historical events.
- Reduce costs using an auxiliary model.
- Automatically retry a failed summary generation.
- Display the summary as if it were a normal wizard response.
- Define a manual compaction initiated by the user.
- Change the semantics of the `MaxSteps` limit of the loop agent.

## Product decisions

- Hybrid policy: preventive and reactive.
- Fixed preventive threshold: 80% of the total model window.
- Structured summary, not free text.
- Same provider and model as the main shift.
- Durable checkpoint within the session log.
- Current activity preserved from the last user message.
- Fallback by budget, respecting semantic groups complete.
- Event visible as a discrete and expandable card.
- If the summary fails or is invalid, the effective history is not modified.

## Chosen architecture

A transactional durable checkpoint will be used. The event log remains the
source of truth and is never mutilated. The checkpoint records the summary and the historical
range it replaces for the model. In the same atomic operation, `ContextEpoch.BaselineSeq` is advanced
 and its `Revision` is increased.

The projection for the model is formed by:

1. The structured summary of the last current checkpoint.
2. The user's last message as a literal anchor, when the fallback has left it within the summarized range.
3. Messages after the range covered by that checkpoint.
4. The current activity preserved verbatim or, if it does not fit, its recent
 window selected by budget.

The UI projection continues reading the entire log. That's why events covered
by the summary do not disappear from the chat.

## Components

### Budget policy

An independent unit calculates whether a `llm.Request` needs compaction. The
calculation includes:

- system prompt;
- tool definitions;
- projected messages;
- model output reservation;
- a conservative margin for tokenizer differences and vendor framing.

The model boundary and tokenizer must be resolved by
model metadata. When there is no exact tokenizer, a conservative estimator is used.
A model without a known context limit cannot use the preventive threshold;
in that case only the reactive overflow path is activated.

The preventive condition is:

```text
tokens_estimados_request >= floor(ventana_total_modelo * 0.80)
```

The departure reservation is part of `tokens_estimados_request`;
 is not subtracted again from the total window.

### History selector

The selector works on messages with their source `Seq` and produces groups that
cannot be separated:

- a normal message forms a group;
- an assistant message with tool calls is grouped with all its results;
- a local tool call with no result is not eligible for compaction while
 is pending;
- a result can never be kept without its corresponding call.

In the normal route, the summary candidate ends just before the user's last message
. From that message onwards the activity is literally preserved.

If that activity does not enter after creating the summary, the fallback is applied:

1. The user's last message is set as the mandatory literal anchor.
2. The subsequent groups are moved from the most recent to the oldest.
3. A contiguous suffix of complete groups is retained until the budget is exhausted.
4. The leg between the anchor message and that suffix is ​​included in a second
 compaction of the same checkpoint before confirming it.
5. If the anchor message itself does not come in, the compaction fails with an explicit
 non-compactable activity error.

The checkpoint records the `Seq` of the anchor message. When projecting, that
literal message is reinjected before the recent suffix even if its `Seq` falls within the
covered range. Thus the baseline remains a contiguous slice and the reconstruction does not
depend on an arbitrary list of preserved messages.

No text or tool output is silently truncated in this layer. Tools
can continue using their own mechanisms for bounded or referenced outputs.

### Summary Generator

The generator makes an isolated call to `Provider.Stream` with the same model of the
epoch. It does not include tools and does not publish deltas as a wizard response. Use your own
timeout so that an incomplete stream does not block the runner.

The entry contains the previous checkpoint, if it exists, plus the new groups that
will be covered. This allows successive compactions without resending all
of the original history.

The output must represent this logical scheme:

```text
objetivo_actual
restricciones_e_instrucciones
decisiones_tomadas
trabajo_completado
estado_de_archivos_y_cambios
resultados_relevantes_de_tools
errores_e_intentos_fallidos
pendientes_y_siguiente_paso
hechos_que_no_deben_reinterpretarse
```

Each field is required, even though its value may indicate that there are no elements.
The concrete serialization can be JSON or an equivalent type, but must
be validated before being persisted and rendered stably for the model.

The summary is rejected when:

- the stream fails or ends incomplete;
 - the response is empty;### Checkpoint durable

`Context.Compacted` is added to the event taxonomy. Its conceptual payload is:

```go
type CompactionCheckpoint struct {
    Summary              StructuredSummary
    ExpectedEpoch        ContextEpoch
    CoveredThroughSeq    Seq
    AnchorUserSeq        Seq
    PreservedFromSeq     Seq
    Model                string
    Reason               CompactionReason
    InputTokensBefore    int
    EstimatedTokensAfter int
}
```

`Reason` only supports `preventive` and `overflow`. `PreservedFromSeq` identifies the
first event of the recent suffix preserved literally. `AnchorUserSeq`
 identifies the last message of the user who started the activity. Without fallback,
`AnchorUserSeq` and `PreservedFromSeq` belong to the same literal leg and
`CoveredThroughSeq` comes before both. With fallback, the baseline can advance
beyond the anchor: the projection retrieves that message for its `Seq` and then
adds the suffix from `PreservedFromSeq`.

A timestamp is not required within the payload: the durable order is set
by the `Seq` of the event. The event does not materialize a normal `session.Message`.

### Atomic store contract

The `Store` exposes an operation equivalent to:

```go
CommitCompaction(ctx context.Context, sessionID string, checkpoint CompactionCheckpoint) (Seq, error)
```

The operation must execute in a single critical section or transaction:

1. Verify that the session exists.
2. Compare the current epoch with `ExpectedEpoch`.
3. Verify that `CoveredThroughSeq` exists and does not regress the current baseline.
4. Verify that `AnchorUserSeq` corresponds to the last chosen user message
 and that `PreservedFromSeq` delimits a valid suffix of complete groups.
5. Add `Context.Compacted` to the log.
6. Save `BaselineSeq = CoveredThroughSeq`.
7. Increment `Revision` exactly once.
8. Confirm everything or confirm nothing.

A conflict returns an error distinguishable from an obsolete checkpoint. The caller
discards the generated summary and rebuilds from the durable state; it does not attempt
to force the checkpoint or reuse it against another range.

`MemoryStore` and `SQLiteStore` must meet the same contract test. SQLite should
persist the epoch per session, not derive it only from the last process in memory.

### Projection for the runner

The reading used by the runner must be explicit and different from the rehydration
of UI. A contract can be entered as `ContextForRunner`, which returns:

- current epoch;
 - last current checkpoint;The summary becomes a context part clearly labeled as
summary generated from previous turns. You should not fake a new message from the
user or wizard. The chosen representation must be supported in a
uniform manner by the providers; if `llm.Request.System` is still a string, the
summary is added to a delimited section of the system prompt.

The UI continues using `Events(sessionID, 0)` and therefore preserves the entire
 history, including compaction events.

## Preventive flow

1. The runner takes the epoch and builds the candidate request.
2. Politics estimates their occupation.
3. Below 80%, the shift continues without compaction.
4. When reaching 80%, the selector determines the summarizable range.
5. The generator produces and validates the summary.
6. A candidate request is reconstructed with the checkpoint not yet confirmed.
7. If there is no reduction or it does not fit, the budget fallback is applied.
8. The store confirms event and epoch atomicly.
9. The attempt returns the internal post-compaction reconstruction signal.
10. The runner rereads the epoch, projects the durable checkpoint and calls
 once.    provider para el turno normal.

## Reactive flow

Reactive routing is used when the provider reliably identifies a
context window error, even if the estimate was below 80%.

1. The provider adapter normalizes the error as `ContextOverflowError`.
2. If the attempt was not previously compacted, execute the same pipeline with
 `Reason = overflow`.
3. Confirm the checkpoint and rebuild the turn.
4. Allow a single post-compaction retry for that run of `runTurn`.
5. A second overflow ends the turn with an explicit error and does not compact again.

Network, authentication, rate limit or cancellation errors do not activate compaction.

## Concurrency and idempotence

- The epoch is taken before selecting the range.
- Any change in model, agent, baseline or revision invalidates the work.
- Two compactions can generate summary in parallel, but only one can
 commit for the same epoch.
- Repeating `CommitCompaction` with an epoch already consumed returns conflict and does not
 add another event.
- The runner never calls the provider main with a request constructed from a
 epoch that changed during compaction.
- The implementation must be safe under `go test -race`.

## Fault Handling

- Failure or timeout of the summary: no event is written or the baseline is advanced.
- Invalid summary: same behavior, with diagnostic error.
- Epoch conflict: discard summary and rebuild; do not present it as a user failure
 if the reconstruction can continue.
- Without resumable range: continue only if the request fits; otherwise fail
 explicitly.
- Minimum activity too large: fail indicating that the last message does not
 fit even without previous history.
- Atomic store error: retain the complete previous state.
- User cancellation: also cancel the isolated compaction call.

There is no automatic retry of summary generation. The user can retry
the turn and trigger a new compaction from the unchanged durable state.

## UI and rehydration

`Context.Compacted` creates a non-conversational card with the title `Contexto
compactado`. The card:

- appears in the position of the event within the log;
- starts collapsed;
- allows the summary sections to be expanded;
- shows the reason and the range covered;
- does not alter the text of historical messages;
- is not mixed with reasoning blocks or assistant responses;
- is rebuilt the same when reloading the session.

During the generation, deltas of the summary are not shown. The card appears only
after the atomic commit. If compaction fails, the UI does not display a partial
card.

## Observability

The durable event contains enough information to audit why it occurred and whether progress was made. Compaction errors must preserve cause and phase in the
shift error or development log, without persisting partial content of the summary.

The minimum derivable metrics are:

- number of compactions per session and reason;
- estimated tokens before and after;
- epoch conflicts;
- generation or validation failures;
- repeated overflows after compacting.

No remote telemetry is added as part of this scope.

## Security and privacy

- The summary is saved in the same store and with the same scope as the session.
- No context is sent to a provider other than the one already configured.
- Secrets or outputs excluded by the tools should not be reintroduced from
 external snapshots.
- Errors should not print the complete summary by default.

## Compatibility and migration

- Sessions without checkpoints maintain `BaselineSeq = 0` and are projected as today.
- SQLite migration adds epoch state per session and columns or payload for
 `Context.Compacted` without rewriting previous events.
- Providers that do not normalize overflow retain preventive compaction, but
 an unrecognized overflow is treated as a normal error.
- The `Compactor` nil can be maintained in unit tests focused on the history
 path, but the actual wiring must install the implementation.

## Testing strategy

The implementation will follow the TDD cycle defined in `AGENTS.md`.

### Policy and estimation

- Does not compact below 80%.
- Compacts exactly when reaching 80%.
- Includes system, tools, messages and output reservation.
- Uses conservative fallback for model without exact tokenizer.
- Model without known window does not compact preventively.

### Selection and summary

- Keeps from the last user message.
- Includes the previous checkpoint in successive compactions.
- Keeps tool call and results in the same group.
- Never keeps an orphan result.
- The fallback chooses complete recent groups.
- Rejects a minimum activity that does not fit.
- Rejects empty, incomplete, huge or missing summary reduction.

### Store

- Commit adds event, advances baseline and increments revision.
- Intermediate failure does not allow partial writing.
- Obsolete checkpoint does not modify state.
- Restart of SQLite rebuilds epoch and checkpoint.
- `MemoryStore` and `SQLiteStore` pass the same contract test.
- Successive compactions advance monotonically.

### Runner and provider

- Preventive route rebuilds before normal `Provider.Stream`.
- Happy path does not generate auxiliary call.
- Normalized overflow compacts and retries only once.
- Second overflow fails without loop.
- Other errors do not trigger compaction.
- Short generator cancellation and turn.
- Concurrent epoch change discards the summary.

### UI

- Event creates collapsed and expandable card.
- Rehydration preserves position and content.
- Does not create assistant message.
- Compaction failure does not create partial card.
- Covered history remains visible.

### Closing doors

```bash
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
cd frontend && npm test
cd frontend && npm run lint
```

The exact frontend commands will fit into existing scripts during the
plan, without introducing a new tool just for this functionality.

## Acceptance criteria

1. A session that reaches 80% generates a single durable checkpoint before the normal
 turn and continues with less context.
2. The postcompaction context contains the structured summary and the literal recent
 activity, except for the documented fallback, which preserves the user's last prompt
 and a recent suffix of complete groups.
3. All original events remain visible and searchable.
4. Restarting Athena produces the same effective context for the next turn.
5. No summary or store failure leaves an advanced baseline without a valid event.
6. A run between compactions confirms at most one checkpoint per epoch.
7. A real overflow allows exactly one post-compaction retry.
8. Tool calls and results are never separated by the window selection.
9. The UI displays a discreet, expandable and durable card.
10. All tests and quality gates pass, including the suite with `-race`.

## Risks and mitigations

- **Inaccurate estimation:** conservative margin and reactive fallback.
- **Summary that loses facts:** mandatory schema, previous checkpoint such as
 input and validation of fields.
- **Summary too large:** own budget and real reduction requirement.
- **Races with new events:** compare-and-swap by epoch within the store.
- **Tools separate:** semantic grouping before budgeting.
- **Overflow loop:** a single post-compaction retry.
- **UI/model divergence:** separate contracts for full log and context of the
 runner.

## Expected impact on the code

The implementation will probably touch, without yet fixing the order of the plan:

- `internal/llm`: context metadata, estimation and normalized error.
- `internal/session`: event, checkpoint, epoch and atomic operation of the store.
- `internal/session/runner`: real compactor, selection, generation and retries.
- `internal/wiring` or `app.go`: installation of the real implementation.
- `frontend/src`: projection and `Context.Compacted` card.
- contract, runner, providers and UI tests.

These are not assumed to live in existing large files. The plan should
prefer small units for policy, selection, generation and persistence.

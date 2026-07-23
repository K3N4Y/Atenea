---
updated_at: 2026-07-21
summary: Architecture of Atenea’s Go agent execution loop.
---

# Athena agent (Go) loop architecture

Designed on 2026-06-19. It takes as reference the production loop described in
`opencode-agent-loop.md` (OpenCode, TypeScript) and translates it into an
idiomatic design in Go for this Wails app (`atenea`).

The goal is not to port the TypeScript code line by line, but to keep
the architectural decisions that make that loop robust in production
and express them with Go primitives: `context.Context`, goroutines, channels,
`errgroup` and a durable store behind an interface.

## Why copy this design

The OpenCode loop is not an in-memory `while model wants tools`. It is a
durable loop around events and persisted state. That gives four properties
that we want in Athena:

- is **resumable**: the source state is events/tables, not local variables;
- accepts **concurrent steering** without corrupting an already prepared turn;
- registers **tool calls before side effects**;
- exposes **streaming progress**, ideal for emitting events to the Wails UI.

## OpenCode concept mapping -> Go

| OpenCode (TS) | Athena (Go) |
| --- | --- |
| `SessionRunner.run()` | `func (r *Runner) Run(ctx, sessionID string, force bool) error` |
| `SessionInput` (queue/steer) | `Inbox` with `Admit` / `HasPending` / `Promote` |
| `SessionContextEpoch` | `ContextEpoch` (snapshot + `BaselineSeq` + `Revision`) |
| `SessionHistory.entriesForRunner()` | `History.EntriesForRunner(ctx, sessionID)` |
| `ToolRegistry.materialize()` | `Registry.Materialize(perms) (defs, settle)` |
| `llm.stream(request)` | `Provider.Stream(ctx, req) (<-chan Event, error)` |
| fibers / `FiberSet` | goroutines + `golang.org/x/sync/errgroup` |
| session events + SSE | `EventBus` + `runtime.EventsEmit` by Wails |
| `RebuildPreparedTurn` (control error) | `errRebuildTurn` (sentinel error) |
| `ContinueAfterOverflowCompaction` | `errContinueAfterCompaction` (sentinel) |

## Package layout

```text
atenea/
  app.go                      // adapter Wails sobre agent.Service
  main.go                     // wails.Run(...)
  internal/
    agent/
      service.go              // lifecycle shared by Wails and TUI
    session/
      session.go              // agregado durable: Session, Message, Seq
      inbox.go                // input durable: queue | steer
      projection.go           // proyecciones puras compartidas por los stores
      epoch.go                // ContextEpoch
      store.go                // interfaces Store y CompactionStore
      memstore.go             // adaptador en memoria
      sqlitestore.go          // adaptador SQLite durable
      runner/
        runner.go             // Run(): loop externo + loop de pasos
        turn.go               // runTurn(): un turno de proveedor
        publish.go            // traduce eventos de provider a eventos de sesion
    llm/
      provider.go             // interface Provider, Request, Event
      anthropic.go            // adaptador concreto (Claude)
    tool/
      registry.go             // Materialize(), settle()
      builtin.go              // bash, read, edit, write, grep, glob...
    event/
      bus.go                  // EventBus: publish + suscripcion (para Wails/SSE)
```

The key separation is the same as in OpenCode: `session` concentrates the durable
domain of the agent, `llm` and `tool` are I/O layers, and `event` is the
observability contract towards the UI.

## Main types

```go
// internal/llm/provider.go
type Provider interface {
    // Stream produce exactamente un turno. El channel se cierra al terminar.
    // Cancelar ctx interrumpe el turno (equivalente a una interrupcion de usuario).
    Stream(ctx context.Context, req Request) (<-chan Event, error)
}

type Request struct {
    Model         string
    System        []Part        // prompt del agente + baseline de contexto
    Messages      []Message     // historial proyectado convertido a formato LLM
    Tools         []ToolDef     // schemas materializados
    ProviderOpts  map[string]any // p.ej. prompt cache key
}

// Event es el stream del proveedor: text/reasoning deltas, tool-call, step-finish...
type Event struct {
    Kind     EventKind // TextDelta, ReasoningDelta, ToolCall, StepFinish, ...
    CallID   string
    ToolName string
    Input    json.RawMessage
    Text     string
    Usage    *Usage // solo en StepFinish
}
```

```go
// internal/tool/registry.go
type Materialized struct {
    Definitions []llm.ToolDef
    // Settle ejecuta y asienta una tool call. Esta cerrada sobre el set
    // anunciado: una tool desconocida o stale devuelve error, no efectos.
    Settle func(ctx context.Context, call Call) (Result, error)
}

func (r *Registry) Materialize(perms Permissions) Materialized
```

The registry is also the single source of truth for the default permission set:
`Registry.Permissions()` derives it from the registered tool names. Assembly
must not repeat those names in a second allowlist. Mode-only tools are explicit
exclusions from that default (`present_plan` is excluded from normal mode), and
restricted modes still pass an explicit subset to `Materialize`.

```go
// internal/session/inbox.go
type Delivery int
const (
    DeliveryQueue Delivery = iota // prompt principal, uno por actividad abierta
    DeliverySteer                 // direccionamiento aceptado durante actividad
)

type Inbox interface {
    Admit(ctx context.Context, sessionID string, p Prompt, d Delivery) error
    HasPending(ctx context.Context, sessionID string, d Delivery) (bool, error)
    Promote(ctx context.Context, sessionID string, d Delivery) (Promotion, error)
}
```

## External loop (`Run`)

Same structure as the OpenCode pseudocode, expressed in Go. `MaxSteps`
remains 25 to cut unproductive model/tool/continuation loops.

```go
const MaxSteps = 25

func (r *Runner) Run(ctx context.Context, sessionID string, force bool) error {
    hasSteer, err := r.inbox.HasPending(ctx, sessionID, DeliverySteer)
    if err != nil {
        return err
    }
    hasQueue := false
    if !hasSteer {
        if hasQueue, err = r.inbox.HasPending(ctx, sessionID, DeliveryQueue); err != nil {
            return err
        }
    }
    if !force && !hasSteer && !hasQueue {
        return nil // sesion idle, nada que hacer
    }

    if err := r.failInterruptedTools(ctx, sessionID); err != nil {
        return err
    }

    promotion := pick(hasSteer, DeliverySteer, hasQueue, DeliveryQueue)
    openActivity := force || hasSteer || hasQueue

    for openActivity {
        needsContinuation := true

        for step := 0; step < MaxSteps; step++ {
            needsContinuation, err = r.runTurn(ctx, sessionID, promotion)
            if err != nil {
                return err
            }
            promotion = DeliverySteer // tras el primer turno solo se promueve steer

            if !needsContinuation {
                needsContinuation, err = r.inbox.HasPending(ctx, sessionID, DeliverySteer)
                if err != nil {
                    return err
                }
            }
            if !needsContinuation {
                break
            }
        }
        if needsContinuation {
            return &StepLimitExceededError{Max: MaxSteps}
        }

        if openActivity, err = r.inbox.HasPending(ctx, sessionID, DeliveryQueue); err != nil {
            return err
        }
        if openActivity {
            promotion = DeliveryQueue
        }
    }
    return nil
}
```

The loop does not continue through wizard text. It only continues if there was a local tool call
 or if `steer` was pending. When closing an activity check if there is another
prompt in `queue` and, if there is, open a new activity.

## What does a turn make (`runTurn`)

A shift is **one** call to the provider. The provider produces a shift, not the entire
session; the loop decides whether to invoke another turn after setting tools/steer.

```go
func (r *Runner) runTurn(ctx context.Context, sessionID string, promotion Delivery) (bool, error) {
    for { // reintenta ante senales de control internas
        cont, err := r.runTurnAttempt(ctx, sessionID, promotion)
        switch {
        case errors.Is(err, errRebuildTurn):
            continue // algo cambio mientras se preparaba: reconstruir desde DB/eventos
        case errors.Is(err, errContinueAfterCompaction):
            promotion = DeliverySteer
            continue // hubo overflow: se compacto, reintentar una vez por la ruta post-compaction
        default:
            return cont, err
        }
    }
}
```

`runTurnAttempt` does, in order:

1. Load the session and validate that the local workspace still matches.
2. Select the configured agent.
3. Load system context, skills guidance and references.
4. Initialize or prepare `ContextEpoch`.
5. Promotes durable inputs (`steer`: all until cutoff; `queue`: the next
 prompt and then steering until cutoff).
6. Reread the session; if agent or model changed, return `errRebuildTurn`.
7. Solve the model.
8. Read projected history (`history.EntriesForRunner`).
9. Materialize tools with the agent's permissions.
10. Build `llm.Request` (model, provider opts, system parts, messages, tools).
11. If the request needs compaction, compact and rebuild the shift.
12. Create the event publisher.
13. Verify that `ContextEpoch` is still valid; if not, `errRebuildTurn`.
14. Call `provider.Stream(ctx, req)` **once**.
15. Consumes the stream (see next section).
16. Wait for the tools goroutines to finish.
17. Handles failures, interrupts, context overflow and unresolved tools.
18. Returns `needsContinuation`.

## Streaming of events and execution of tools

Here is the big linguistic difference with TypeScript: where OpenCode uses a
`FiberSet`, Atenea uses goroutines coordinated with `errgroup`. The channel of the
provider is the equivalent of the stream.

```go
func (r *Runner) consume(ctx context.Context, in <-chan llm.Event,
    pub *Publisher, settle SettleFunc) (needsContinuation bool, err error) {

    g, gctx := errgroup.WithContext(ctx)

    for ev := range in {
        // 1. Publica el evento como evento durable de sesion (y hacia la UI).
        if err := pub.Publish(ctx, ev); err != nil {
            return false, err
        }

        // 2. Si es una tool-call local, ejecutarla concurrentemente.
        if ev.Kind == llm.ToolCall {
            ev := ev // captura para la goroutine
            needsContinuation = true
            g.Go(func() error {
                res, err := settle(gctx, ev.CallID, ev.ToolName, ev.Input)
                if err != nil {
                    return pub.ToolFailed(ctx, ev.CallID, err)
                }
                // Publica un tool-result sintetico para que el modelo lo vea
                // en el siguiente turno.
                return pub.ToolSuccess(ctx, ev.CallID, res)
            })
        }
    }

    // 3. Antes de continuar, esperar a que TODAS las tools se asienten.
    if err := g.Wait(); err != nil {
        return false, err
    }
    return needsContinuation, nil
}
```

Rules retained from OpenCode:

- The publisher registers `Tool.Called` **before** executing the implementation; continued.

### Provider-executed vs local

Same as upstream, there are two kinds of tool results:

- **Provider-executed**: the provider returns the result; the runner only persists it
, it does not launch local `settle`.
- **Local tool call**: executed by Atenea via `settle`.

## Published events

`publish.go` translates provider events to session durable events and
maintains the `assistantMessageID` of the shift and a map of tool calls by `callID`.
The names are preserved so that the frontend can map them 1:1:

```text
Step.Started / Step.Ended / Step.Failed
Text.Started / Text.Delta / Text.Ended
Reasoning.Started / Reasoning.Delta / Reasoning.Ended
Tool.Input.Started / Tool.Input.Delta / Tool.Input.Ended
Tool.Called / Tool.Success / Tool.Failed
```

Text, reasoning and tool input are buffered to emit deltas and also
a final event with the complete content. The provider's `step-finish` ends
in `Step.Ended` with input/output/reasoning/cache tokens.

## Control signals via errors

Instead of handling exceptions, Go uses wrapped sentinel errors and
`errors.Is`:

```go
var (
    errRebuildTurn             = errors.New("rebuild prepared turn")
    errContinueAfterCompaction = errors.New("continue after overflow compaction")
)
```

- `errRebuildTurn`: change the agent/model, epoch mismatch or concurrent
 promotion. It is rebuilt from DB/events.
- `errContinueAfterCompaction`: there was context overflow before starting the wizard
 message; it is compacted and retried once via the post-compaction path.

This avoids continuing with a request that no longer represents the actual state of the session.

## Interrupts and fault handling

The user interruption is modeled by canceling the `ctx` of the turn (e.g. from
a "stop" button in the Wails UI). The runner has explicit paths to:

- provider errors;
 - context overflow;
 - interruptions (`ctx.Err() == context.Canceled`);When a turn fails, the publisher attempts to close unresolved tools with
`Tool.Failed` to avoid leaving the history in an ambiguous state. `failInterruptedTools`
at the beginning of `Run` it cleans tools that were left halfway from a previous run
(resumption after crash).

## Durable persistence

The source state is events/tables, not memory. Behind an interface `Store`
to be able to start simple (local SQLite) without coupling the runner:

```go
type Store interface {
    AppendEvent(ctx context.Context, sessionID string, ev SessionEvent) (Seq, error)
    LoadSession(ctx context.Context, sessionID string) (Session, error)
    Messages(ctx context.Context, sessionID string, sinceSeq Seq) ([]Message, error)
    Epoch(ctx context.Context, sessionID string) (ContextEpoch, error)
    // ...inbox: admit/promote/pending
}
```

For Wails, a single SQLite DB in the app's data directory is enough
 to boot. The important point is that `Run` always rebuilds the request
from the store, never from a live state between turns.

## Shared headless service and Wails integration

`internal/agent.Service` owns session modes, slash-command expansion, inbox
admission, stable run identity, replacement, cancellation, and stale-run
cleanup. It serializes admission and completion hooks per session, while
independent sessions remain concurrent. `wiring.Build` receives
`Service.Mode`, so Wails and the TUI use one authoritative mode source.

The UI adapters retain only their presentation and persistence hooks. Wails
captures the session CWD before admission, publishes hard errors, and performs
deferred first-message titling. The TUI records checkpoints and literal
composer history, publishes `RunDoneMsg`, and schedules manual compaction.

The `EventBus` is the only point that knows Wails. The runner publishes neutral
events and the bus forwards them to the frontend with `runtime.EventsEmit`:

```go
// internal/event/bus.go
func (b *Bus) Publish(ctx context.Context, ev session.SessionEvent) {
    runtime.EventsEmit(b.appCtx, "session:"+ev.SessionID, ev)
}
```

On the frontend, each fragment (`Step.*`, `Text.*`, `Tool.*`) is mapped to the
streaming UI. `app.go` delegates prompts to the shared service:

```go
func (a *App) SendPrompt(sessionID, text string) error {
    job := a.titleJob(sessionID, text)
    _, err := a.agent.Send(sessionID, text, a.turnHooks(sessionID, job))
    return err
}
```

The service admits to `Inbox`, cancels an older run for the same session, waits
for it to finish, and then enters `Runner.Run`. Cancellation and deadlines are
clean completion states; other errors return through the adapter hook.

## Key differences Go vs TS

- **Fibers -> goroutines + errgroup**: same concurrency semantics, with
 cancellation propagated by `context`.
- **Stream -> channel**: `<-chan Event` closed at the end of the turn; range with
 `for ev := range in`.
- **Control errors -> sentinel + `errors.Is`**: no exceptions, explicit and testable
 flow control.
- **Interruption -> `context.CancelFunc`**: the "stop" button of the UI cancels the
 `ctx` of the turn.
- **Bus of events -> `runtime.EventsEmit`**: the border with Wails is in a single
 packet.

## Architectural implication

The loop is designed for a durable scheduling agent, not for a chat
ephemeral:

- source state is events/session tables;
- request to provider is rebuilt every turn;
- tool calls are logged before side effects;
- continuations are triggered only after posting results;
- concurrent agent/context changes force rebuild;
- step limit protects against loops productive;
- the Wails UI observes progress by events, without coupling to the runner.

## Sources

- Reference loop: `opencode-agent-loop.md`
- Reference architecture: `opencode-architecture.md`
- Wails runtime (events): https://wails.io/../reference/runtime/events
- `golang.org/x/sync/errgroup`: https://pkg.go.dev/golang.org/x/sync/errgroup
- `context`: https://pkg.go.dev/context

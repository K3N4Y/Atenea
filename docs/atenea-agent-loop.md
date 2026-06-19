# Arquitectura del loop del agente de Atenea (Go)

Disenado el 2026-06-19. Toma como referencia el loop de produccion descrito en
`docs/opencode-agent-loop.md` (OpenCode, TypeScript) y lo traduce a un diseno
idiomatico en Go para esta app Wails (`atenea`).

El objetivo no es portar el codigo TypeScript linea por linea, sino quedarse con
las decisiones arquitectonicas que hacen que ese loop sea robusto en produccion
y expresarlas con primitivas de Go: `context.Context`, goroutines, channels,
`errgroup` y un store durable detras de una interface.

## Por que copiar este diseno

El loop de OpenCode no es un `while model wants tools` en memoria. Es un loop
durable alrededor de eventos y estado persistido. Eso le da cuatro propiedades
que queremos en Atenea:

- es **reanudable**: el estado fuente son eventos/tablas, no variables locales;
- acepta **steering concurrente** sin corromper un turno ya preparado;
- registra **tool calls antes de los efectos laterales**;
- expone **progreso por streaming**, ideal para emitir eventos a la UI Wails.

## Mapeo de conceptos OpenCode -> Go

| OpenCode (TS) | Atenea (Go) |
| --- | --- |
| `SessionRunner.run()` | `func (r *Runner) Run(ctx, sessionID string, force bool) error` |
| `SessionInput` (queue/steer) | `Inbox` con `Admit` / `HasPending` / `Promote` |
| `SessionContextEpoch` | `ContextEpoch` (snapshot + `BaselineSeq` + `Revision`) |
| `SessionHistory.entriesForRunner()` | `History.EntriesForRunner(ctx, sessionID)` |
| `ToolRegistry.materialize()` | `Registry.Materialize(perms) (defs, settle)` |
| `llm.stream(request)` | `Provider.Stream(ctx, req) (<-chan Event, error)` |
| fibers / `FiberSet` | goroutines + `golang.org/x/sync/errgroup` |
| eventos de sesion + SSE | `EventBus` + `runtime.EventsEmit` de Wails |
| `RebuildPreparedTurn` (error de control) | `errRebuildTurn` (sentinel error) |
| `ContinueAfterOverflowCompaction` | `errContinueAfterCompaction` (sentinel) |

## Layout de paquetes

```text
atenea/
  app.go                      // bindings Wails, arranca/observa el Runner
  main.go                     // wails.Run(...)
  internal/
    session/
      session.go              // agregado durable: Session, Message, Seq
      inbox.go                // input durable: queue | steer
      history.go              // historial proyectado para el runner
      epoch.go                // ContextEpoch
      store.go                // interface Store (persistencia) + impl SQLite
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

La separacion clave es la misma que en OpenCode: `session` concentra el dominio
durable del agente, `llm` y `tool` son capas de I/O, y `event` es el contrato de
observabilidad hacia la UI.

## Tipos principales

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

## Loop externo (`Run`)

Misma estructura que el pseudocodigo de OpenCode, expresada en Go. `MaxSteps`
sigue siendo 25 para cortar loops improductivos de modelo/tool/continuacion.

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

El loop no continua por texto del asistente. Solo continua si hubo una tool call
local o si quedo `steer` pendiente. Al cerrar una actividad revisa si hay otro
prompt en `queue` y, si lo hay, abre una nueva actividad.

## Que hace un turno (`runTurn`)

Un turno es **una** llamada al proveedor. El proveedor produce un turno, no toda
la sesion; el loop decide si invocar otro turno despues de asentar tools/steer.

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

`runTurnAttempt` hace, en orden:

1. Carga la sesion y valida que el workspace local siga coincidiendo.
2. Selecciona el agente configurado.
3. Carga contexto de sistema, guidance de skills y referencias.
4. Inicializa o prepara `ContextEpoch`.
5. Promueve inputs durables (`steer`: todos hasta cutoff; `queue`: el siguiente
   prompt y luego steering hasta cutoff).
6. Relee la sesion; si agente o modelo cambiaron, devuelve `errRebuildTurn`.
7. Resuelve el modelo.
8. Lee historial proyectado (`history.EntriesForRunner`).
9. Materializa tools con los permisos del agente.
10. Construye `llm.Request` (model, provider opts, system parts, messages, tools).
11. Si el request necesita compaction, compacta y reconstruye el turno.
12. Crea el publicador de eventos.
13. Verifica que el `ContextEpoch` siga vigente; si no, `errRebuildTurn`.
14. Llama **una vez** a `provider.Stream(ctx, req)`.
15. Consume el stream (ver siguiente seccion).
16. Espera a que terminen las goroutines de tools.
17. Maneja fallos, interrupciones, context overflow y tools sin resolver.
18. Devuelve `needsContinuation`.

## Streaming de eventos y ejecucion de tools

Aqui esta la diferencia idiomatica grande con TypeScript: donde OpenCode usa un
`FiberSet`, Atenea usa goroutines coordinadas con `errgroup`. El channel del
proveedor es el equivalente al stream.

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

Reglas que se conservan de OpenCode:

- el publicador registra `Tool.Called` **antes** de ejecutar la implementacion;
- `settle` valida que la tool exista y sea la misma que fue anunciada;
- output grande se acota/almacena fuera del mensaje (un `ToolOutputStore`);
- varias tool calls corren concurrentemente, pero el turno **espera** a todas
  antes de decidir la continuacion.

### Provider-executed vs local

Igual que upstream, hay dos clases de resultado de tool:

- **Provider-executed**: el proveedor devuelve el resultado; el runner solo lo
  persiste, no lanza `settle` local.
- **Local tool call**: la ejecuta Atenea via `settle`.

## Eventos publicados

`publish.go` traduce eventos del proveedor a eventos durables de sesion y
mantiene el `assistantMessageID` del turno y un mapa de tool calls por `callID`.
Los nombres se conservan para que el frontend pueda mapearlos 1:1:

```text
Step.Started / Step.Ended / Step.Failed
Text.Started / Text.Delta / Text.Ended
Reasoning.Started / Reasoning.Delta / Reasoning.Ended
Tool.Input.Started / Tool.Input.Delta / Tool.Input.Ended
Tool.Called / Tool.Success / Tool.Failed
```

Texto, razonamiento e input de tools se bufferizan para emitir deltas y tambien
un evento final con el contenido completo. El `step-finish` del proveedor termina
en `Step.Ended` con tokens de input/output/reasoning/cache.

## Senales de control via errores

En vez de excepciones de control, Go usa sentinel errors envueltos y
`errors.Is`:

```go
var (
    errRebuildTurn             = errors.New("rebuild prepared turn")
    errContinueAfterCompaction = errors.New("continue after overflow compaction")
)
```

- `errRebuildTurn`: cambio el agente/modelo, mismatch del epoch o promocion
  concurrente. Se reconstruye desde DB/eventos.
- `errContinueAfterCompaction`: hubo context overflow antes de empezar el mensaje
  del asistente; se compacta y se reintenta una vez por la ruta post-compaction.

Esto evita seguir con un request que ya no representa el estado real de la sesion.

## Interrupciones y manejo de fallos

La interrupcion de usuario se modela cancelando el `ctx` del turno (p.ej. desde
un boton "stop" en la UI Wails). El runner tiene caminos explicitos para:

- errores de proveedor;
- context overflow;
- interrupciones (`ctx.Err() == context.Canceled`);
- pregunta rechazada por el usuario;
- goroutines de tools interrumpidas;
- fallos de tools;
- tools que el proveedor marco como ejecutadas pero nunca resolvio;
- limite de pasos excedido.

Cuando un turno falla, el publicador intenta cerrar tools no resueltas con
`Tool.Failed` para no dejar el historial en estado ambiguo. `failInterruptedTools`
al inicio de `Run` limpia tools que quedaron a medias de una corrida anterior
(reanudacion tras crash).

## Persistencia durable

El estado fuente son eventos/tablas, no memoria. Detras de una interface `Store`
para poder empezar simple (SQLite local) sin acoplar el runner:

```go
type Store interface {
    AppendEvent(ctx context.Context, sessionID string, ev SessionEvent) (Seq, error)
    LoadSession(ctx context.Context, sessionID string) (Session, error)
    Messages(ctx context.Context, sessionID string, sinceSeq Seq) ([]Message, error)
    Epoch(ctx context.Context, sessionID string) (ContextEpoch, error)
    // ...inbox: admit/promote/pending
}
```

Para Wails, una sola DB SQLite en el directorio de datos de la app es suficiente
para arrancar. El punto importante es que `Run` siempre reconstruye el request
desde el store, nunca desde estado vivo entre turnos.

## Integracion con Wails

El `EventBus` es el unico punto que conoce a Wails. El runner publica eventos
neutrales y el bus los reenvia al frontend con `runtime.EventsEmit`:

```go
// internal/event/bus.go
func (b *Bus) Publish(ctx context.Context, ev session.SessionEvent) {
    runtime.EventsEmit(b.appCtx, "session:"+ev.SessionID, ev)
}
```

En el frontend, cada fragmento (`Step.*`, `Text.*`, `Tool.*`) se mapea a la UI
en streaming. `app.go` arranca el runner en una goroutine cuando llega un prompt
desde el frontend:

```go
func (a *App) SendPrompt(sessionID, text string) error {
    if err := a.inbox.Admit(a.ctx, sessionID, session.Prompt{Text: text}, session.DeliveryQueue); err != nil {
        return err
    }
    go func() {
        if err := a.runner.Run(context.Background(), sessionID, false); err != nil {
            a.bus.PublishError(sessionID, err)
        }
    }()
    return nil
}
```

`Admit` es no bloqueante y durable; el loop drena el inbox. Steering en vivo es
el mismo `Admit` con `DeliverySteer` mientras el runner ya esta corriendo.

## Diferencias clave Go vs TS

- **Fibers -> goroutines + errgroup**: misma semantica de concurrencia, con
  cancelacion propagada por `context`.
- **Stream -> channel**: `<-chan Event` cerrado al terminar el turno; rango con
  `for ev := range in`.
- **Errores de control -> sentinel + `errors.Is`**: sin excepciones, control de
  flujo explicito y testeable.
- **Interrupcion -> `context.CancelFunc`**: el boton "stop" de la UI cancela el
  `ctx` del turno.
- **Bus de eventos -> `runtime.EventsEmit`**: la frontera con Wails queda en un
  solo paquete.

## Implicacion arquitectonica

El loop esta disenado para un agente de programacion durable, no para un chat
ephemeral:

- el estado fuente son eventos/tablas de sesion;
- el request al proveedor se reconstruye en cada turno;
- las tool calls se registran antes de los efectos laterales;
- las continuaciones se activan solo despues de asentar resultados;
- los cambios concurrentes de agente/contexto fuerzan reconstruccion;
- el limite de pasos protege contra loops no productivos;
- la UI Wails observa el progreso por eventos, sin acoplarse al runner.

## Fuentes

- Loop de referencia: `docs/opencode-agent-loop.md`
- Arquitectura de referencia: `docs/opencode-arquitectura.md`
- Wails runtime (eventos): https://wails.io/docs/reference/runtime/events
- `golang.org/x/sync/errgroup`: https://pkg.go.dev/golang.org/x/sync/errgroup
- `context`: https://pkg.go.dev/context

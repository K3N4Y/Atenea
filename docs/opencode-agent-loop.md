# Arquitectura del loop del agente en OpenCode

Investigado el 2026-06-19 sobre la documentacion oficial de OpenCode y el
codigo fuente upstream `anomalyco/opencode` en la rama `dev`.

Este documento baja un nivel respecto a `docs/opencode-arquitectura.md`: describe
como corre el loop interno que procesa una sesion de agente.

## Resumen corto

El loop del agente vive principalmente en:

- `packages/core/src/session/runner/llm.ts`
- `packages/core/src/session/runner/publish-llm-event.ts`
- `packages/core/src/session/input.ts`
- `packages/core/src/session/history.ts`
- `packages/core/src/session/context-epoch.ts`
- `packages/core/src/tool/registry.ts`

La unidad de orquestacion es `SessionRunner.run({ sessionID, force? })`. Ese
runner drena trabajo durable de una sesion hasta que queda sin actividad abierta:
promueve prompts pendientes, arma un turno de proveedor, streamea eventos del
LLM, ejecuta tools locales, persiste resultados y decide si necesita otro turno.

OpenCode no implementa un loop simple en memoria tipo `while model wants tools`.
Implementa un loop durable alrededor de eventos, tablas de sesion, historial
proyectado y reintentos ante cambios concurrentes de contexto.

## Vista de alto nivel

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

## Conceptos clave

### Sesion

La sesion es el agregado durable. Tiene ID, ubicacion del workspace, agente,
modelo, mensajes, eventos y estado relacionado. El runner siempre valida que la
sesion siga perteneciendo al mismo directorio/workspace antes de correr un turno.

### Input durable

Los prompts no entran directo al modelo. Primero se admiten como
`SessionInput.Admitted` con un `delivery`:

- `queue`: prompt principal en cola. El runner procesa uno por actividad abierta.
- `steer`: direccionamiento del usuario aceptado mientras ya hay actividad. Se
  promueve para afectar la siguiente continuacion.

El input tiene dos secuencias importantes:

- `admitted_seq`: secuencia del evento que admitio el prompt.
- `promoted_seq`: secuencia asignada cuando el prompt se convierte en mensaje
  visible para el runner.

Esto permite que el loop sea replayable y que pueda aceptar steering concurrente
sin mezclarlo de forma insegura con un turno que ya fue preparado.

### Context epoch

`SessionContextEpoch` mantiene una foto del contexto de sistema para la sesion:
baseline, snapshot, agente, `baselineSeq` y revision. El loop lo usa para:

- inicializar el contexto si no existe;
- reconciliar cambios de archivos/instrucciones/contexto;
- reemplazar el baseline si hace falta;
- bloquear reemplazos de agente cuando el cambio no es seguro;
- detectar si el agente/contexto cambio entre preparar el request y llamar al
  proveedor.

Si la revision o el agente ya no coinciden, el turno se descarta y se reconstruye
desde estado durable.

### Historial proyectado

`SessionHistory.entriesForRunner()` lee los mensajes persistidos en orden de
secuencia. Toma en cuenta:

- el ultimo mensaje de compaction;
- el `baselineSeq` del contexto;
- mensajes `system` posteriores al baseline;
- mensajes no-system necesarios para el modelo.

El runner convierte ese historial con `toLLMMessages(context, model)` antes de
construir el request al proveedor.

### Tool materialization

`ToolRegistry.materialize(permissions)` produce dos cosas:

- `definitions`: schemas de tools que se anuncian al modelo;
- `settle(input)`: funcion cerrada sobre el set anunciado para ejecutar y
  asentar una tool call.

El registry junta tools built-in y tools registradas localmente. Si una tool esta
completamente denegada por permisos, se elimina de `definitions`. Si el modelo
intenta llamar una tool desconocida o stale, el settlement devuelve un resultado
de error en vez de ejecutar efectos laterales.

## Pseudocodigo del loop

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

`MAX_STEPS` es `25`. Ese limite evita loops infinitos de modelo/tool/continuacion.

## Que hace un turno (`runTurnAttempt`)

Un turno es una llamada explicita al proveedor. En terminos practicos:

1. Carga la sesion y valida que el workspace local coincida.
2. Selecciona el agente configurado.
3. Carga contexto de sistema, guidance de skills y referencias.
4. Inicializa o prepara `SessionContextEpoch`.
5. Promueve inputs durables:
   - `steer`: promueve todos los steering inputs hasta un cutoff.
   - `queue`: promueve el siguiente queued prompt y luego steering hasta cutoff.
6. Relee la sesion y aborta el turno si agente o modelo cambiaron.
7. Resuelve el modelo.
8. Lee historial proyectado para el runner.
9. Materializa tools con permisos del agente.
10. Construye `LLM.request`:
    - `model`;
    - provider options, incluyendo prompt cache key para OpenAI;
    - system parts: prompt del agente + baseline de contexto;
    - messages: historial convertido a formato LLM;
    - tools: schemas materializados.
11. Ejecuta compaction si el request lo necesita y reconstruye el turno si
    cambia el estado.
12. Crea un publicador de eventos.
13. Verifica que el context epoch siga vigente.
14. Llama exactamente una vez a `llm.stream(request)`.
15. Por cada evento del stream:
    - publica el evento como evento durable de sesion;
    - si es `tool-call` local, ejecuta `toolMaterialization.settle(...)` en un
      fiber;
    - publica un `LLMEvent.toolResult(...)` sintetico con el resultado de la
      tool.
16. Espera que terminen los fibers de tools.
17. Maneja fallos, interrupciones, context overflow y tools sin resolver.
18. Devuelve `true` si necesita continuacion; `false` si el turno dejo la sesion
    estable.

La regla importante: el proveedor produce un turno, no toda la sesion. El loop
del runner decide si invocar otro turno despues de asentar tools o steering.

## Eventos publicados

`publish-llm-event.ts` traduce eventos del proveedor a eventos durables de
sesion. Mantiene el `assistantMessageID` del turno y un mapa interno de tool
calls por `callID`.

Eventos principales:

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

Los fragmentos de texto, razonamiento e input de tools se bufferizan para poder
emitir deltas y tambien un evento final con el contenido completo. El evento
`step-finish` del proveedor termina en `SessionEvent.Step.Ended` con tokens de
input/output/reasoning/cache.

## Ejecucion de tools

Hay dos clases de tool result:

- **Provider-executed**: el proveedor ejecuta la tool o devuelve el resultado. El
  runner lo persiste, pero no lanza `settle()` local.
- **Local tool call**: OpenCode la ejecuta por medio de `ToolRegistry.settle()`.

Para tool calls locales:

1. El publicador registra durablemente `Tool.Called`.
2. El runner obtiene el `assistantMessageID` asociado al `callID`.
3. Ejecuta `toolMaterialization.settle(...)`.
4. El settlement valida que la tool exista y sea la misma que fue anunciada.
5. Ejecuta la implementacion de la tool.
6. Convierte output estructurado a `ToolResultValue`.
7. Acota/almacena output grande con `ToolOutputStore.bound(...)`.
8. Publica `Tool.Success` o `Tool.Failed`.
9. Marca `needsContinuation = true` para que el modelo vea el resultado en un
   siguiente turno.

La ejecucion local ocurre en `FiberSet`, por lo que varias tool calls pueden
correr concurrentemente. Antes de continuar, el runner espera que todas se
asienten.

## Condiciones de continuacion

El turno devuelve `true` cuando:

- hubo una `tool-call` local y no hubo error de proveedor;
- o, al terminar, hay steering pendiente que debe entrar al siguiente turno.

El loop no continua por simple texto del asistente. Si el modelo responde texto y
no pide tools, el turno termina y la actividad queda cerrada, salvo que haya un
`steer` pendiente.

Despues de cerrar una actividad, el loop revisa si hay otro prompt `queue`
pendiente. Si existe, abre una nueva actividad con `promotion = "queue"`.

## Transiciones internas

`runTurn` usa errores internos como senales de control:

- `RebuildPreparedTurn`: algo cambio mientras se preparaba el turno. Ejemplos:
  agente/modelo distinto, mismatch del epoch, o promocion concurrente. Se vuelve
  a preparar desde DB/eventos.
- `ContinueAfterOverflowCompaction`: hubo context overflow antes de empezar el
  mensaje del asistente, se compacta y se reintenta una vez por la ruta
  post-compaction.

Esto evita seguir con un request que ya no representa el estado real de la
sesion.

## Manejo de fallos

El runner tiene caminos explicitos para:

- provider errors;
- context overflow;
- interrupciones;
- pregunta rechazada por el usuario;
- tool fibers interrumpidos;
- tool failures;
- tools que el proveedor marco como ejecutadas pero nunca resolvio;
- limite de pasos excedido.

Cuando un turno falla, el publicador intenta cerrar tools no resueltas con
`Tool.Failed` para no dejar el historial en estado ambiguo.

## Diagrama de un turno con tools locales

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

## Implicacion arquitectonica

El loop de OpenCode esta disenado para un agente de programacion durable, no para
un chat ephemeral:

- el estado fuente son eventos/tablas de sesion;
- el request al proveedor se reconstruye en cada turno;
- las tool calls se registran antes de los efectos laterales;
- las continuaciones se activan solo despues de asentar resultados;
- los cambios concurrentes de agente/contexto fuerzan reconstruccion;
- el limite de pasos protege contra loops no productivos;
- el servidor puede exponer el progreso por SSE porque cada fragmento relevante
  se publica como evento.

Si este proyecto Wails integra OpenCode, la frontera mas estable sigue siendo el
servidor HTTP/OpenAPI. Para observar el loop, conviene suscribirse al stream de
eventos y mapear `Step.*`, `Text.*` y `Tool.*` a la UI en vez de intentar invocar
el core TypeScript directamente.

## Fuentes consultadas

- Server docs: https://opencode.ai/docs/server/
- Tools docs: https://opencode.ai/docs/tools/
- Permissions docs: https://opencode.ai/docs/permissions/
- Runner principal: https://github.com/anomalyco/opencode/blob/dev/packages/core/src/session/runner/llm.ts
- Publicador de eventos LLM: https://github.com/anomalyco/opencode/blob/dev/packages/core/src/session/runner/publish-llm-event.ts
- Session input: https://github.com/anomalyco/opencode/blob/dev/packages/core/src/session/input.ts
- Session history: https://github.com/anomalyco/opencode/blob/dev/packages/core/src/session/history.ts
- Context epoch: https://github.com/anomalyco/opencode/blob/dev/packages/core/src/session/context-epoch.ts
- Tool registry: https://github.com/anomalyco/opencode/blob/dev/packages/core/src/tool/registry.ts

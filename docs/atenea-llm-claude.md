# Integracion LLM: Claude (Go)

Disenado el 2026-06-19. Define como Atenea habla con el modelo. Es la capa
`internal/llm` que el loop (`docs/atenea-agent-loop.md`) consume via
`Provider.Stream`. Llena el hueco que el loop daba por hecho: como se mapea
`Provider`/`Request`/`Event` a la API real de Anthropic.

## Decisiones

- **Modelo por defecto**: `claude-opus-4-8` (Opus 4.8). Configurable.
- **SDK**: oficial de Go, `github.com/anthropics/anthropic-sdk-go`. No HTTP crudo.
- **Loop manual, no el tool runner del SDK**: el runner durable de Atenea ejecuta
  las tools y persiste estado. Usamos `Messages.NewStreaming` directo, una llamada
  por turno, y nosotros mismos asentamos las tools. El `BetaToolRunner` del SDK
  esconde justo el loop que nosotros queremos controlar.
- **Streaming siempre**: el turno puede ser largo; streaming evita timeouts HTTP y
  alimenta los eventos `Text.*`/`Tool.*` hacia la UI.

> Por que SDK y no HTTP: el SDK trae reintentos con backoff (429/5xx), tipos de
> error tipados, acumulacion de stream y los tipos de mensajes. Reimplementar eso
> a mano es trabajo perdido.

## Cliente y configuracion

```go
// internal/llm/anthropic.go
import (
    "github.com/anthropics/anthropic-sdk-go"
    "github.com/anthropics/anthropic-sdk-go/option"
)

// Por defecto lee ANTHROPIC_API_KEY del entorno.
client := anthropic.NewClient()

// O explicita:
client := anthropic.NewClient(option.WithAPIKey(key))
```

Config minima de Atenea (ver doc futuro de config/secretos):

| Clave | Default | Notas |
| --- | --- | --- |
| `model` | `claude-opus-4-8` | constante `anthropic.ModelClaudeOpus4_8` |
| `max_tokens` | `64000` | streaming; Opus 4.8 admite hasta 128K |
| `effort` | `high` | `high`/`xhigh` para trabajo agentico (ver Thinking) |
| API key | env `ANTHROPIC_API_KEY` | nunca en disco/prompt |

## Un turno = una llamada de streaming

El contrato del loop es: `Provider.Stream(ctx, req)` produce **un** turno y cierra
el channel. Con el SDK eso es exactamente una llamada a `Messages.NewStreaming`.
El adaptador traduce el `llm.Request` neutral a `MessageNewParams`, consume el
stream del SDK y emite nuestros `llm.Event` por un channel.

```go
func (p *AnthropicProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
    out := make(chan llm.Event)

    params := anthropic.MessageNewParams{
        Model:     anthropic.Model(req.Model),
        MaxTokens: req.MaxTokens,
        System:    toSystemBlocks(req.System), // []TextBlockParam, ver caching
        Messages:  toMessages(req.Messages),   // historial proyectado
        Tools:     toTools(req.Tools),          // schemas materializados
        Thinking:  anthropic.ThinkingConfigParamUnion{
            OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{},
        },
    }

    stream := p.client.Messages.NewStreaming(ctx, params)
    go func() {
        defer close(out)
        msg := anthropic.Message{} // acumulador para el mensaje final
        for stream.Next() {
            ev := stream.Current()
            msg.Accumulate(ev)           // reconstruye el assistant message
            emit(out, mapEvent(ev))      // traduce a llm.Event (abajo)
        }
        if err := stream.Err(); err != nil {
            emit(out, llm.ErrorEvent(err))
            return
        }
        // msg ya tiene Content + StopReason + Usage para persistir e historiar.
        emit(out, llm.StepFinished(msg))
    }()
    return out, nil
}
```

`msg.Accumulate(ev)` es clave: reconstruye el `assistant message` completo
(texto + bloques `tool_use` + `thinking`) que el runner persiste y vuelve a mandar
como historial en el siguiente turno. No re-parseamos el stream a mano.

### Mapeo de eventos del SDK a `llm.Event`

El SDK emite eventos de bloque (start/delta/stop) y de mensaje (delta/stop). Se
hace type-switch con `.AsAny()`. Lo unico documentado al 100% es el delta de texto;
los demas siguen el mismo patron y conviene confirmarlos contra los tipos del SDK.

```go
func mapEvent(ev anthropic.MessageStreamEventUnion) llm.Event {
    switch e := ev.AsAny().(type) {
    case anthropic.ContentBlockDeltaEvent:
        switch d := e.Delta.AsAny().(type) {
        case anthropic.TextDelta:
            return llm.TextDelta(d.Text)
        // ThinkingDelta    -> llm.ReasoningDelta(...)   (verificar nombre en SDK)
        // InputJSONDelta   -> llm.ToolInputDelta(...)   (verificar nombre en SDK)
        }
    // ContentBlockStartEvent -> Text/Reasoning/Tool.Input Started o ToolCall
    // ContentBlockStopEvent  -> Text/Reasoning/Tool.Input Ended
    // MessageDeltaEvent      -> stop_reason + usage parcial
    // MessageStopEvent       -> Step.Ended
    }
    return llm.Ignore
}
```

Correspondencia con los eventos de sesion del loop:

| Bloque/evento Anthropic | `llm.Event` | Evento de sesion |
| --- | --- | --- |
| content_block_start (text) | TextStarted | `Text.Started` |
| content_block_delta (TextDelta) | TextDelta | `Text.Delta` |
| content_block_stop (text) | TextEnded | `Text.Ended` |
| content_block_start (thinking) | ReasoningStarted | `Reasoning.Started` |
| content_block_delta (ThinkingDelta) | ReasoningDelta | `Reasoning.Delta` |
| content_block_start (tool_use) | ToolCall | `Tool.Called` |
| content_block_delta (InputJSONDelta) | ToolInputDelta | `Tool.Input.Delta` |
| message_delta (stop_reason, usage) | — | tokens del `Step` |
| message_stop | StepFinished | `Step.Ended` |

## Tool use: como encaja con el loop durable

Atenea **no** deja que el SDK ejecute tools. El flujo por turno:

1. El stream trae bloques `tool_use` (nombre + input JSON por `callID`).
2. Al cerrar el stream, `msg` tiene `StopReason == StopReasonToolUse` y los bloques
   `tool_use`.
3. El runner ejecuta las tools localmente (`settle`, con `errgroup`) y publica
   `Tool.Success`/`Tool.Failed`.
4. En el **siguiente** turno, el historial incluye:
   - el `assistant message` con los `tool_use` (`msg.ToParam()`), y
   - un `user message` con un `tool_result` por cada `callID`.

```go
// Construir el assistant turn previo para el historial:
messages = append(messages, msg.ToParam())

// Por cada tool ejecutada, un tool_result (el id es el del bloque tool_use):
results := []anthropic.ContentBlockParamUnion{
    anthropic.NewToolResultBlock(callID, resultText, isError),
}
messages = append(messages, anthropic.NewUserMessage(results...))
```

Esto es el "manual agentic loop" del SDK, pero gobernado por nuestro `Run`/`runTurn`
en vez de por un helper. `StopReasonToolUse` es la senal de continuacion del lado
del proveedor; el loop decide de verdad (ver `needsContinuation`).

Definicion de tools (desde `ToolRegistry.Materialize`):

```go
addTool := anthropic.ToolParam{
    Name:        "read",
    Description: anthropic.String("Lee un archivo..."),
    InputSchema: anthropic.ToolInputSchemaParam{Properties: map[string]any{ /* ... */ }},
}
tools := []anthropic.ToolUnionParam{{OfTool: &addTool}}
```

El input de cada `tool_use` se parsea con `json.Unmarshal` sobre el raw JSON
(`variant.JSON.Input.Raw()`) — nunca match de string sobre el JSON serializado:
Opus 4.8 puede escapar Unicode/slashes distinto.

## Thinking y effort

- **Adaptive only** en Opus 4.8: `Thinking: {OfAdaptive: &ThinkingConfigAdaptiveParam{}}`.
- `thinking: {type:"enabled", budget_tokens:N}` da **400**. Tampoco `temperature`,
  `top_p`, `top_k` (400). Se steerea por prompt/effort, no por sampling.
- **Contenido de thinking omitido por defecto** en Opus 4.8: el bloque llega vacio
  salvo que se pida `display: "summarized"`. Si la UI muestra razonamiento, hay que
  optar por summarized (mapear a `Reasoning.Delta`); si no, dejarlo omitido.
- **Effort**: `high`/`xhigh` para trabajo agentico/coding. Va en el output config
  del request; **verificar el binding exacto en `anthropic-sdk-go`** antes de fijar
  el campo (no inventarlo). Default `high`.

## Prompt caching

El orden de render es `tools -> system -> messages`. Mantener lo estable primero
(prompt del agente + baseline de contexto), lo volatil al final. Un breakpoint en
el ultimo bloque de system cachea tools + system juntos.

```go
System: []anthropic.TextBlockParam{{
    Text:         systemPromptEstable,
    CacheControl: anthropic.NewCacheControlEphemeralParam(),
}},
```

- El loop ya ayuda: el request se reconstruye desde estado durable cada turno, asi
  que con un system prompt congelado el prefijo se mantiene byte-identico.
- Encaja con el `prompt cache key` que el loop menciona para el turno.
- Verificar hits: `resp.Usage.CacheReadInputTokens` / `CacheCreationInputTokens`.
  Si `CacheReadInputTokens` es 0 entre turnos identicos, hay un invalidador silencioso
  (fecha/uuid en el system, tools reordenadas, JSON no determinista).

Invariante: no meter `time.Now()`, ids por request, ni tools que cambian de orden en
el prefijo. Ordenar tools de forma determinista (por nombre).

## Steering en vivo (mid-session system)

El `DeliverySteer` del loop encaja con los **mid-session system messages** (beta
`mid-conversation-system-2026-04-07`): en vez de editar el system top-level (que
invalida el cache), se agrega un mensaje `role: "system"` al final de `messages`.
Es el canal de operador no falsificable. Verificar el binding en el SDK de Go antes
de usarlo; si el modelo no lo soporta, fallback a un `<system-reminder>` en el turno
de usuario.

## Compaction

El loop tiene `ContinueAfterOverflowCompaction`. Del lado de Claude, la compaction
server-side (beta `compact-2026-01-12`) vive en `client.Beta.Messages.New` con
`ContextManagement`:

```go
params := anthropic.BetaMessageNewParams{
    Model:     anthropic.ModelClaudeOpus4_8,
    MaxTokens: 64000,
    Betas:     []anthropic.AnthropicBeta{"compact-2026-01-12"},
    ContextManagement: anthropic.BetaContextManagementConfigParam{
        Edits: []anthropic.BetaContextManagementConfigEditUnionParam{
            {OfCompact20260112: &anthropic.BetaCompact20260112EditParam{}},
        },
    },
    Messages: /* ... */,
}
```

Critico: persistir **todo** `resp.Content` (no solo el texto). Los bloques de
compaction se reusan en el siguiente request; si se guarda solo el texto, se pierde
el estado de compaction. Esto refuerza por que el runner historia el mensaje
completo (`msg`), no un string.

## Errores, reintentos y stop reasons

- El SDK reintenta 429/5xx con backoff (default `max_retries=2`). Para clasificar,
  usar el tipo de error del SDK (`.Type()`), no match de string.
- `StopReason` a manejar en `runTurn`:
  - `StopReasonToolUse` -> hay tool calls locales -> continuacion.
  - `StopReasonEndTurn` -> turno cerrado (salvo steer pendiente).
  - `StopReasonRefusal` -> hay `StopDetails` (categoria/explicacion); no reintentar
    con el mismo prompt.
  - `StopReasonMaxTokens` -> subir `max_tokens` o reanudar.
  - `model_context_window_exceeded` -> ruta de compaction/overflow del loop.

## Que NO usar

- `BetaToolRunner` del SDK: oculta el loop que el runner durable necesita controlar.
- `budget_tokens`, `temperature`, `top_p`, `top_k`: 400 en Opus 4.8.
- Prefills del ultimo turno assistant: 400. Para forzar formato, usar structured
  outputs (`output_config.format`), no prefill.
- `max_tokens` bajo sin razon: trunca a mitad. Default 64000 en streaming.

## Mapeo a los milestones del roadmap

- **M2** (Provider + fake): el fake imita el channel de `Stream`. El adaptador real
  de esta seccion entra en **M10**, detras de la misma interface.
- **M3** (Publisher): consume el mapeo `llm.Event` de aqui.
- **M5** (un turno): `Messages.NewStreaming` una vez + acumular + asentar tools.
- **M7** (control): `model_context_window_exceeded` + compaction.

## Fuentes

- SDK Go: https://github.com/anthropics/anthropic-sdk-go
- Referencia de API usada para este doc: skill `claude-api` (cache 2026-05-26):
  modelos, streaming, tool use, thinking adaptive, prompt caching, compaction,
  error codes.
- Loop que consume esta capa: `docs/atenea-agent-loop.md`
- Roadmap: `docs/atenea-agent-loop-roadmap.md`
- Manera de trabajo: `AGENTS.md`

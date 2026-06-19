# Integracion LLM para pruebas: OpenCode Go via SDK de OpenAI (Go)

Disenado el 2026-06-19. Para las **primeras pruebas** Atenea usa la suscripcion
**OpenCode Go** (gateway OpenCode Zen, OpenAI-compatible) en vez de pagar Anthropic
directo. Es otra implementacion del mismo `Provider` de `docs/atenea-agent-loop.md`,
asi que el loop no cambia: solo se enchufa otro adaptador.

El adaptador de Claude (`docs/atenea-llm-claude.md`) sigue siendo el destino final
(M10). Este es el camino barato para arrancar (M2/M5).

## Que es OpenCode Zen / OpenCode Go

- **OpenCode Zen**: gateway de modelos curados del equipo de OpenCode, con una API
  **OpenAI-compatible**.
- **OpenCode Go**: el plan de suscripcion de bajo costo sobre ese gateway (acceso a
  modelos de coding probados). Es lo que se usa aqui.
- Endpoint base: `https://opencode.ai/zen/v1`
  (chat completions en `https://opencode.ai/zen/v1/chat/completions`).
- Como es OpenAI-compatible, se usa el **SDK de OpenAI** apuntado a ese base URL.

## Configuracion

| Clave | Valor | Notas |
| --- | --- | --- |
| base URL | `https://opencode.ai/zen/v1` | gateway Zen |
| API key | desde `opencode.ai/auth` | header `Authorization: Bearer` |
| env var | `OPENCODE_API_KEY` | nombre nuestro; confirmar el que prefieras |
| model id | **a confirmar** | ver "Valores a confirmar" |

Atenea elige el provider por config: `provider = opencode` para pruebas,
`provider = anthropic` para produccion. Misma interface, distinto adaptador.

## Cliente (SDK de OpenAI para Go)

```go
// internal/llm/opencode.go
import (
    "github.com/openai/openai-go"
    "github.com/openai/openai-go/option"
)

client := openai.NewClient(
    option.WithAPIKey(os.Getenv("OPENCODE_API_KEY")),
    option.WithBaseURL("https://opencode.ai/zen/v1"),
)
```

`WithBaseURL` es lo unico que cambia respecto a usar OpenAI directo: el SDK habla
el mismo protocolo contra el gateway de OpenCode.

> Confirmar el path de import / version mayor del SDK (`github.com/openai/openai-go`
> publica majors como `/v2`) antes de fijar `go.mod`. No inventar la version.

## Un turno = una llamada de streaming

Igual que el contrato del loop: `Provider.Stream(ctx, req)` produce un turno y
cierra el channel. Con el SDK de OpenAI eso es `Chat.Completions.NewStreaming`,
consumido con el `ChatCompletionAccumulator` (reconstruye el mensaje final y los
tool calls del stream).

```go
func (p *OpenCodeProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
    out := make(chan llm.Event)

    params := openai.ChatCompletionNewParams{
        Model:    req.Model,                 // model id del gateway (a confirmar)
        Messages: toOpenAIMessages(req),     // system + historial + tool results
        Tools:    toOpenAITools(req.Tools),  // schemas materializados
        // MaxTokens / MaxCompletionTokens segun el SDK
    }

    stream := p.client.Chat.Completions.NewStreaming(ctx, params)
    go func() {
        defer close(out)
        acc := openai.ChatCompletionAccumulator{}
        for stream.Next() {
            chunk := stream.Current()
            acc.AddChunk(chunk)

            // Texto incremental:
            if len(chunk.Choices) > 0 {
                if d := chunk.Choices[0].Delta.Content; d != "" {
                    emit(out, llm.TextDelta(d))
                }
            }
            // Tool call completa (el accumulator la junta a partir de los deltas):
            if tc, ok := acc.JustFinishedToolCall(); ok {
                emit(out, llm.ToolCall(tc.Index, tc.Name, tc.Arguments))
            }
            if _, ok := acc.JustFinishedContent(); ok {
                emit(out, llm.TextEnded())
            }
        }
        if err := stream.Err(); err != nil {
            emit(out, llm.ErrorEvent(err))
            return
        }
        emit(out, llm.StepFinished(acc.ChatCompletion)) // mensaje final + finish_reason
    }()
    return out, nil
}
```

`acc` cumple el rol que `msg.Accumulate` cumple en el adaptador de Claude:
reconstruye el assistant message completo (texto + `tool_calls`) que el runner
persiste e historia para el siguiente turno.

### Mapeo a `llm.Event`

| Stream OpenAI | `llm.Event` | Evento de sesion |
| --- | --- | --- |
| `Delta.Content` (texto) | TextDelta | `Text.Delta` |
| `JustFinishedContent()` | TextEnded | `Text.Ended` |
| deltas de `tool_calls` → `JustFinishedToolCall()` | ToolCall | `Tool.Called` |
| `finish_reason` en el ultimo chunk | StepFinished | `Step.Ended` |

`finish_reason`:

- `tool_calls` -> hubo tool calls locales -> continuacion.
- `stop` -> turno cerrado (salvo steer pendiente).
- `length` -> equivalente a max_tokens (subir el limite o reanudar).

## Tool use estilo OpenAI (difiere de Claude)

El flujo en el loop es el mismo (el runner ejecuta y asienta), pero la **forma de
los mensajes** cambia respecto a Anthropic:

- El assistant turn trae un array `tool_calls` (cada uno con `id`, `function.name`,
  `function.arguments` como **string JSON**).
- El resultado de cada tool NO va como `tool_result` dentro de un user message.
  Va como un mensaje aparte con `role: "tool"` y `tool_call_id` = el `id` del call.

```go
// Siguiente turno: assistant con tool_calls + un mensaje role:"tool" por resultado.
messages = append(messages, assistantTurnConToolCalls) // del acc
messages = append(messages, openai.ToolMessage(resultText, toolCallID))
```

- Los `arguments` son JSON generado por el modelo: **validar antes de ejecutar**
  (puede venir JSON invalido o params inventados). Parsear con `json.Unmarshal`.
- Confirmar los constructores exactos del SDK (`openai.ToolMessage`,
  `openai.UserMessage`, `openai.ChatCompletionToolParam`, etc.) antes de codear.

## Diferencias clave vs el adaptador de Claude

| Tema | Claude (Anthropic) | OpenCode Go (OpenAI-compatible) |
| --- | --- | --- |
| Resultado de tool | `tool_result` en user message | mensaje `role:"tool"` + `tool_call_id` |
| Tool call args | objeto JSON parseado | **string** JSON (validar) |
| Reasoning/thinking | bloques `thinking` (adaptive) | normalmente ausente; depende del modelo |
| Prompt caching | `cache_control` explicito | sin control de breakpoints (cache del gateway) |
| Senal de continuacion | `StopReasonToolUse` | `finish_reason == "tool_calls"` |
| Steering | mid-session system (beta) | mensaje `role:"system"`/`developer` extra |

Implicacion para Atenea: el adaptador es fino y aislado; el resto del loop
(historial, eventos, settle de tools, continuacion) no nota la diferencia. Por eso
arrancar con OpenCode Go y migrar a Claude despues no toca el runner.

## Que NO esperar en pruebas

- **Sin `cache_control`**: no se controlan breakpoints de cache como en Claude.
- **Sin bloques de thinking** garantizados: muchos modelos OpenAI-compatible no los
  exponen; los eventos `Reasoning.*` pueden quedar vacios para el v1.
- **Descubrimiento de modelos**: OpenCode Zen puede no exponer `/v1/models`, asi que
  el model id se confirma a mano (dashboard), no por API.

## Encaje con el roadmap

- **M2** (Provider + fake): el fake imita el channel; este adaptador OpenCode es el
  primer provider **real** y barato para iterar.
- **M5** (un turno): `NewStreaming` + `ChatCompletionAccumulator` + asentar tools.
- **M10**: cambiar a Claude (`docs/atenea-llm-claude.md`) detras de la **misma**
  interface, sin tocar el loop.

## Valores a confirmar (no inventar)

1. **Model id exacto** que acepta el gateway (p.ej. `gpt-5.5`, `big-pickle`, ...).
   En la config de OpenCode el formato es `opencode/<id>`; en la API cruda confirmar
   si se manda con o sin prefijo `opencode/`. Verificar en el dashboard/cuenta.
2. **Nombre de la env var** de la API key (aqui `OPENCODE_API_KEY`).
3. **Version/import path** del SDK `github.com/openai/openai-go`.
4. **Constructores exactos** del SDK para mensajes/tools/params.
5. Si el modelo elegido expone reasoning, y `MaxTokens` vs `MaxCompletionTokens`.

## Fuentes

- OpenCode Zen: https://opencode.ai/docs/zen/
- OpenCode Providers: https://opencode.ai/docs/providers/
- SDK Go de OpenAI: https://github.com/openai/openai-go
- Loop que consume esta capa: `docs/atenea-agent-loop.md`
- Adaptador de produccion (Claude): `docs/atenea-llm-claude.md`
- Manera de trabajo: `AGENTS.md`

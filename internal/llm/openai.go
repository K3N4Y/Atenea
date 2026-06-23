package llm

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/option"
	"github.com/openai/openai-go/v2/packages/param"
)

// OpenAIProvider habla con un endpoint OpenAI-compatible (OpenAI/OpenRouter) via
// streaming SSE. Traduce el turno del proveedor a llm.Event: abre con StepStarted,
// envuelve el razonamiento incremental (delta.reasoning) entre
// ReasoningStarted/ReasoningEnded y el texto incremental (delta.content) entre
// TextStarted/TextEnded, emitiendo un Delta por fragmento, y cierra con StepEnded
// cargando el Usage. El razonamiento se cierra antes de abrir el texto. Las
// delta.tool_calls se acumulan por index y tambien se streamean en vivo: al primer
// delta de un index emite ToolInputStarted{CallID} y por cada fragmento de
// arguments un ToolInputDelta{CallID, Input}, cerrando el bloque de texto/razonamiento
// abierto cuando empiezan los tool_calls. Al cerrar el turno, tras texto/razonamiento
// y antes de StepEnded, vuelca en orden de aparicion un ToolInputEnded{CallID} seguido
// de un Event{Kind: ToolCall} por tool call (con CallID, ToolName e Input). Un fallo
// del stream se reporta como StepFailed (sin StepEnded).
type OpenAIProvider struct {
	client openai.Client
	model  string
}

var _ Provider = (*OpenAIProvider)(nil)

// toolAccum acumula los fragmentos de una tool call del stream: el id y el nombre
// llegan una vez y los argumentos se concatenan fragmento a fragmento.
type toolAccum struct {
	id, name string
	args     strings.Builder
	started  bool
}

// NewOpenAIProvider construye el provider apuntando al base URL dado, lo que
// permite inyectar un httptest.Server en los tests y OpenRouter en produccion.
func NewOpenAIProvider(apiKey, baseURL, model string) *OpenAIProvider {
	client := openai.NewClient(
		option.WithAPIKey(apiKey),
		option.WithBaseURL(baseURL),
	)
	return &OpenAIProvider{client: client, model: model}
}

// Stream abre un turno completo. Resuelve el modelo (req.Model con fallback al
// configurado), construye el request real (Messages/Tools mapeados, usage pedido
// en el stream) y emite el bracketing del turno por el channel respetando la
// cancelacion del ctx. Cierra el channel al terminar.
func (p *OpenAIProvider) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	out := make(chan Event)

	model := req.Model
	if model == "" {
		model = p.model
	}

	// The system prompt (turn baseline) goes as the first message with role system,
	// before the history. Empty = not prepended (no empty system message is sent).
	msgs := toOpenAIMessages(req.Messages)
	if req.System != "" {
		msgs = append([]openai.ChatCompletionMessageParamUnion{openai.SystemMessage(req.System)}, msgs...)
	}

	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(model),
		Messages: msgs,
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: openai.Bool(true),
		},
	}
	if tools := toOpenAITools(req.Tools); tools != nil {
		params.Tools = tools
	}
	// Pide razonamiento a OpenRouter: campo top-level `reasoning` (no tipado por el
	// SDK) que habilita el delta.reasoning del modelo.
	params.SetExtraFields(map[string]any{
		"reasoning": map[string]any{"enabled": true},
	})

	stream := p.client.Chat.Completions.NewStreaming(ctx, params)

	go func() {
		defer close(out)

		if !emit(ctx, out, Event{Kind: StepStarted}) {
			return
		}

		var usage *Usage
		textOpen := false
		reasoningOpen := false

		order := []int64{}
		accs := map[int64]*toolAccum{}

		for stream.Next() {
			chunk := stream.Current()

			// El chunk final con stream_options.include_usage trae el usage del
			// request entero (choices vacio).
			if chunk.Usage.PromptTokens != 0 || chunk.Usage.CompletionTokens != 0 {
				usage = &Usage{
					InputTokens:  int(chunk.Usage.PromptTokens),
					OutputTokens: int(chunk.Usage.CompletionTokens),
				}
			}

			if len(chunk.Choices) > 0 {
				// Cuando el modelo pasa a llamar tools, cierra primero el bloque de
				// texto o razonamiento que estuviera abierto.
				if len(chunk.Choices[0].Delta.ToolCalls) > 0 {
					if reasoningOpen {
						if !emit(ctx, out, Event{Kind: ReasoningEnded}) {
							return
						}
						reasoningOpen = false
					}
					if textOpen {
						if !emit(ctx, out, Event{Kind: TextEnded}) {
							return
						}
						textOpen = false
					}
				}
				for _, tc := range chunk.Choices[0].Delta.ToolCalls {
					a := accs[tc.Index]
					if a == nil {
						a = &toolAccum{}
						accs[tc.Index] = a
						order = append(order, tc.Index)
					}
					if !a.started && tc.ID != "" {
						a.id = tc.ID
						if !emit(ctx, out, Event{Kind: ToolInputStarted, CallID: a.id}) {
							return
						}
						a.started = true
					}
					if tc.ID != "" {
						a.id = tc.ID
					}
					if tc.Function.Name != "" {
						a.name = tc.Function.Name
					}
					if frag := tc.Function.Arguments; frag != "" {
						a.args.WriteString(frag)
						if a.started {
							if !emit(ctx, out, Event{Kind: ToolInputDelta, CallID: a.id, Input: json.RawMessage(frag)}) {
								return
							}
						}
					}
				}
				if r := reasoningText(chunk.Choices[0].Delta); r != "" {
					if !reasoningOpen {
						if !emit(ctx, out, Event{Kind: ReasoningStarted}) {
							return
						}
						reasoningOpen = true
					}
					if !emit(ctx, out, Event{Kind: ReasoningDelta, Text: r}) {
						return
					}
				}
				if c := chunk.Choices[0].Delta.Content; c != "" {
					// El razonamiento se cierra ANTES de abrir el texto: el bloque de
					// thinking termina cuando empieza el contenido visible.
					if reasoningOpen {
						if !emit(ctx, out, Event{Kind: ReasoningEnded}) {
							return
						}
						reasoningOpen = false
					}
					if !textOpen {
						if !emit(ctx, out, Event{Kind: TextStarted}) {
							return
						}
						textOpen = true
					}
					if !emit(ctx, out, Event{Kind: TextDelta, Text: c}) {
						return
					}
				}
				if chunk.Choices[0].FinishReason != "" {
					if reasoningOpen {
						if !emit(ctx, out, Event{Kind: ReasoningEnded}) {
							return
						}
						reasoningOpen = false
					}
					if textOpen {
						if !emit(ctx, out, Event{Kind: TextEnded}) {
							return
						}
						textOpen = false
					}
				}
			}
		}

		if reasoningOpen {
			if !emit(ctx, out, Event{Kind: ReasoningEnded}) {
				return
			}
		}
		if textOpen {
			if !emit(ctx, out, Event{Kind: TextEnded}) {
				return
			}
		}

		if err := stream.Err(); err != nil {
			emit(ctx, out, Event{Kind: StepFailed, Text: err.Error()})
			return
		}

		for _, idx := range order {
			a := accs[idx]
			if !emit(ctx, out, Event{Kind: ToolInputEnded, CallID: a.id}) {
				return
			}
			if !emit(ctx, out, Event{Kind: ToolCall, CallID: a.id, ToolName: a.name, Input: json.RawMessage(a.args.String())}) {
				return
			}
		}

		emit(ctx, out, Event{Kind: StepEnded, Usage: usage})
	}()

	return out, nil
}

// emit envia ev por out respetando la cancelacion del ctx. Devuelve false si el
// ctx se cancelo (el productor debe cortar y no colgarse), igual que el fake.
func emit(ctx context.Context, out chan<- Event, ev Event) bool {
	select {
	case <-ctx.Done():
		return false
	case out <- ev:
		return true
	}
}

// reasoningText extrae el fragmento de razonamiento del campo extendido de
// OpenRouter delta.reasoning, que el SDK no tipa y deja en ExtraFields como JSON
// crudo. Devuelve "" si el campo no vino, es null o no es un string.
func reasoningText(delta openai.ChatCompletionChunkChoiceDelta) string {
	f, ok := delta.JSON.ExtraFields["reasoning"]
	if !ok || f.Raw() == "" {
		return ""
	}
	var r string
	if err := json.Unmarshal([]byte(f.Raw()), &r); err != nil {
		return ""
	}
	return r
}

// toOpenAIMessages proyecta el historial al formato del SDK segun el Role. El
// assistant lleva su texto opcional mas los tool_calls (id, function.name y
// function.arguments como string JSON crudo) que la API requiere para el
// round-trip multi-paso; el rol "tool" se mapea a un tool result con su
// tool_call_id, que debe emparejar con el id de la tool call del assistant. Un
// Role desconocido se trata como user (defensivo: el modelo siempre recibe algo
// valido).
func toOpenAIMessages(msgs []Message) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case "assistant":
			asst := toAssistantMessage(m)
			out = append(out, openai.ChatCompletionMessageParamUnion{OfAssistant: &asst})
		case "tool":
			out = append(out, openai.ToolMessage(m.Text, m.ToolCallID))
		case "system":
			out = append(out, openai.SystemMessage(m.Text))
		default:
			out = append(out, openai.UserMessage(m.Text))
		}
	}
	return out
}

// toAssistantMessage proyecta un Message del assistant al param del SDK: el texto
// opcional mas los tool_calls (id, function.name y function.arguments JSON crudo)
// que la API requiere para el round-trip multi-paso.
func toAssistantMessage(m Message) openai.ChatCompletionAssistantMessageParam {
	asst := openai.ChatCompletionAssistantMessageParam{}
	if m.Text != "" {
		asst.Content.OfString = param.NewOpt(m.Text)
	}
	for _, tc := range m.ToolCalls {
		asst.ToolCalls = append(asst.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{
			OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
				ID: tc.ID,
				Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
					Name:      tc.Name,
					Arguments: string(tc.Arguments),
				},
			},
		})
	}
	return asst
}

// toOpenAITools materializa cada ToolDef como un function tool. El Schema crudo se
// parsea a FunctionParameters (map[string]any). Devuelve nil si no hay tools, para
// no enviar un campo tools vacio.
func toOpenAITools(tools []ToolDef) []openai.ChatCompletionToolUnionParam {
	if len(tools) == 0 {
		return nil
	}
	out := make([]openai.ChatCompletionToolUnionParam, 0, len(tools))
	for _, t := range tools {
		fn := openai.FunctionDefinitionParam{Name: t.Name}
		if t.Description != "" {
			fn.Description = openai.String(t.Description)
		}
		if len(t.Schema) > 0 {
			var params openai.FunctionParameters
			if err := json.Unmarshal(t.Schema, &params); err == nil {
				fn.Parameters = params
			}
		}
		out = append(out, openai.ChatCompletionFunctionTool(fn))
	}
	return out
}

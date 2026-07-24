package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/option"
	"github.com/openai/openai-go/v2/packages/param"
)

// defaultRequestTimeout acota cada intento de request del cliente OpenAI. Sin el,
// un SSE colgado deja la goroutine del Stream viva para siempre con el body abierto.
const defaultRequestTimeout = 60 * time.Second

// OpenAIProvider habla con un endpoint OpenAI-compatible (OpenAI/OpenRouter) via
// streaming SSE. Traduce el turno del proveedor a llm.Event: abre con StepStarted,
// envuelve el razonamiento incremental (delta.reasoning de OpenRouter o
// delta.reasoning_content de los locales: LM Studio, Ollama, vLLM) entre
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
	label  string
	// reasoning controla la inyeccion del campo top-level `reasoning` (extension de
	// OpenRouter). Los perfiles neutrales y OpenAI lo omiten.
	reasoning bool
	profile   compatibilityProfile
}

type compatibilityProfile uint8

const (
	compatibilityNeutral compatibilityProfile = iota
	compatibilityOpenAI
	compatibilityOpenRouter
)

var _ Provider = (*OpenAIProvider)(nil)

// Option ajusta un OpenAIProvider al construirlo. Mantiene el constructor estable
// (apiKey, baseURL, model) y deja extender el comportamiento sin romper callers.
type Option func(*OpenAIProvider)

// WithoutOpenRouterReasoning apaga la inyeccion del campo `reasoning` de OpenRouter.
// Se usa para apuntar el provider a un endpoint local OpenAI-compatible (LM Studio,
// Ollama), que rechaza o ignora esa extension propia de OpenRouter.
func WithoutOpenRouterReasoning() Option {
	return func(p *OpenAIProvider) { p.reasoning = false }
}

// WithOpenAICompatibility enables only fields supported by the official OpenAI
// API. Conversation affinity is mapped to prompt_cache_key.
func WithOpenAICompatibility() Option {
	return func(p *OpenAIProvider) {
		p.profile = compatibilityOpenAI
		p.reasoning = false
	}
}

// WithOpenRouterCompatibility enables OpenRouter's routing and reasoning fields.
func WithOpenRouterCompatibility() Option {
	return func(p *OpenAIProvider) {
		p.profile = compatibilityOpenRouter
		p.reasoning = true
	}
}

// toolAccum acumula los fragmentos de una tool call del stream: el id y el nombre
// llegan una vez y los argumentos se concatenan fragmento a fragmento.
type toolAccum struct {
	id, name string
	args     strings.Builder
	started  bool
}

// NewOpenAIProvider construye el provider apuntando al base URL dado, lo que
// permite inyectar un httptest.Server en los tests y OpenRouter en produccion. El
// cliente lleva un timeout por request (defaultRequestTimeout) para que un SSE
// colgado no deje la goroutine del Stream viva para siempre con el body abierto.
func NewOpenAIProvider(apiKey, baseURL, model string, opts ...Option) *OpenAIProvider {
	return newOpenAIProviderWithTimeout(apiKey, baseURL, model, defaultRequestTimeout, opts...)
}

// newOpenAIProviderWithTimeout es el constructor real, con el timeout por request
// inyectable para que los tests verifiquen el corte sin esperar el default largo.
// reasoning arranca en true (OpenRouter) y las opts pueden apagarlo para locales.
func newOpenAIProviderWithTimeout(apiKey, baseURL, model string, timeout time.Duration, opts ...Option) *OpenAIProvider {
	client := openai.NewClient(
		option.WithAPIKey(apiKey),
		option.WithBaseURL(baseURL),
		option.WithRequestTimeout(timeout),
	)
	p := &OpenAIProvider{client: client, model: model, label: providerLabel(baseURL)}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func providerLabel(baseURL string) string {
	parsed, _ := url.Parse(baseURL)
	switch {
	case strings.Contains(parsed.Host, "openrouter"):
		return "OpenRouter"
	case strings.Contains(parsed.Host, "openai"):
		return "OpenAI"
	default:
		return "Provider"
	}
}

// retryTiming keeps the SDK's built-in retry policy, but gives its two retries
// predictable delays and refuses provider-requested waits longer than 10s.
func retryTiming(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	resp, err := next(req)
	if resp == nil {
		return resp, err
	}
	if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusBadGateway &&
		resp.StatusCode != http.StatusServiceUnavailable && resp.StatusCode != http.StatusGatewayTimeout {
		resp.Header.Set("x-should-retry", "false")
	}
	if ms, parseErr := strconv.ParseFloat(resp.Header.Get("Retry-After-Ms"), 64); parseErr == nil && ms > 10_000 {
		resp.Header.Set("Retry-After-Ms", "10000")
	}
	retryAfter := resp.Header.Get("Retry-After")
	if seconds, parseErr := strconv.ParseFloat(retryAfter, 64); parseErr == nil {
		if seconds > 10 {
			resp.Header.Set("Retry-After", "10")
		}
	} else if at, dateErr := http.ParseTime(retryAfter); dateErr == nil && time.Until(at) > 10*time.Second {
		resp.Header.Set("Retry-After", "10")
	}
	if resp.Header.Get("Retry-After") == "" && resp.Header.Get("Retry-After-Ms") == "" {
		delays := [...]string{"2", "5"}
		attempt, _ := strconv.Atoi(req.Header.Get("X-Stainless-Retry-Count"))
		if attempt < len(delays) {
			resp.Header.Set("Retry-After", delays[attempt])
		}
	}
	return resp, err
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
	// SDK) que habilita el delta.reasoning del modelo. Se omite en locales
	// (WithoutOpenRouterReasoning) porque no entienden esa extension.
	extraFields := map[string]any{}
	if p.reasoning {
		extraFields["reasoning"] = map[string]any{"enabled": true}
	}
	if req.SessionKey != "" && p.profile == compatibilityOpenAI {
		extraFields["prompt_cache_key"] = req.SessionKey
	}
	if req.SessionKey != "" && p.profile == compatibilityOpenRouter {
		extraFields["session_id"] = req.SessionKey
	}
	if len(extraFields) > 0 {
		params.SetExtraFields(extraFields)
	}

	go func() {
		defer close(out)

		if !emit(ctx, out, Event{Kind: StepStarted}) {
			return
		}
		stream := p.client.Chat.Completions.NewStreaming(ctx, params,
			option.WithMiddleware(func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
				resp, err := retryTiming(req, next)
				attempt, _ := strconv.Atoi(req.Header.Get("X-Stainless-Retry-Count"))
				if attempt < 2 && retryableResponse(resp, err) {
					delay := [...]string{"2", "5"}[attempt]
					emit(ctx, out, Event{Kind: StepRetrying, Text: fmt.Sprintf("%s (%s): %s Retrying in %ss…", p.label, model, retryReason(resp), delay)})
				}
				return resp, err
			}),
		)

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
					InputTokens:          int(chunk.Usage.PromptTokens),
					OutputTokens:         int(chunk.Usage.CompletionTokens),
					CacheReadTokens:      int(chunk.Usage.PromptTokensDetails.CachedTokens),
					CacheableInputTokens: int(chunk.Usage.PromptTokens),
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
			emit(ctx, out, Event{Kind: StepFailed, Text: fmt.Sprintf("%s (%s): %v", p.label, model, err)})
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

func retryReason(resp *http.Response) string {
	if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
		return "Rate limit reached."
	}
	if resp == nil {
		return "Connection interrupted."
	}
	return "Provider temporarily unavailable."
}

func retryableResponse(resp *http.Response, err error) bool {
	if err != nil || resp == nil {
		return true
	}
	return resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusBadGateway ||
		resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout
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

// reasoningText extrae el fragmento de razonamiento del campo extendido de la delta,
// que el SDK no tipa y deja en ExtraFields como JSON crudo. Acepta las dos
// convenciones del ecosistema OpenAI-compatible: `reasoning` (extension de OpenRouter)
// y `reasoning_content` (convencion de DeepSeek que adoptan LM Studio, Ollama, vLLM y
// SGLang). Devuelve el primer campo presente con un string no vacio, o "" si ninguno
// vino, es null o no es un string.
func reasoningText(delta openai.ChatCompletionChunkChoiceDelta) string {
	for _, key := range []string{"reasoning", "reasoning_content"} {
		f, ok := delta.JSON.ExtraFields[key]
		if !ok || f.Raw() == "" {
			continue
		}
		var r string
		if err := json.Unmarshal([]byte(f.Raw()), &r); err != nil {
			continue
		}
		if r != "" {
			return r
		}
	}
	return ""
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

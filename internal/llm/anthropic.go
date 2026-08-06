package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
)

const defaultAnthropicMaxOutputTokens = 8192

type anthropicEffortSupport uint8

const (
	anthropicEffortThroughHigh anthropicEffortSupport = iota + 1
	anthropicEffortThroughMax
	anthropicEffortWithXHigh
)

var anthropicEffortModels = []struct {
	prefix  string
	support anthropicEffortSupport
}{
	{prefix: "claude-opus-4-8", support: anthropicEffortWithXHigh},
	{prefix: "claude-opus-4-7", support: anthropicEffortWithXHigh},
	{prefix: "claude-opus-5", support: anthropicEffortWithXHigh},
	{prefix: "claude-sonnet-5", support: anthropicEffortWithXHigh},
	{prefix: "claude-fable-5", support: anthropicEffortWithXHigh},
	{prefix: "claude-mythos-5", support: anthropicEffortWithXHigh},
	{prefix: "claude-opus-4-6", support: anthropicEffortThroughMax},
	{prefix: "claude-sonnet-4-6", support: anthropicEffortThroughMax},
	{prefix: "claude-mythos", support: anthropicEffortThroughMax},
	{prefix: "claude-opus-4-5", support: anthropicEffortThroughHigh},
}

var anthropicEfforts = map[ReasoningEffort]anthropic.OutputConfigEffort{
	ReasoningEffortLow:    anthropic.OutputConfigEffortLow,
	ReasoningEffortMedium: anthropic.OutputConfigEffortMedium,
	ReasoningEffortHigh:   anthropic.OutputConfigEffortHigh,
	ReasoningEffortXHigh:  anthropic.OutputConfigEffortXhigh,
	ReasoningEffortMax:    anthropic.OutputConfigEffortMax,
}

func supportsAnthropicEffort(support anthropicEffortSupport, effort ReasoningEffort) bool {
	switch effort {
	case ReasoningEffortLow, ReasoningEffortMedium, ReasoningEffortHigh:
		return true
	case ReasoningEffortMax:
		return support >= anthropicEffortThroughMax
	case ReasoningEffortXHigh:
		return support == anthropicEffortWithXHigh
	default:
		return false
	}
}

func matchesAnthropicModel(model, prefix string) bool {
	return model == prefix || strings.HasPrefix(model, prefix+"-")
}

func resolveAnthropicEffort(model string, preference *ReasoningPreference) (anthropic.OutputConfigEffort, error) {
	if preference == nil || preference.Effort == "" {
		return "", nil
	}
	effort, valid := anthropicEfforts[preference.Effort]
	for _, modelSupport := range anthropicEffortModels {
		if !matchesAnthropicModel(model, modelSupport.prefix) {
			continue
		}
		if valid && supportsAnthropicEffort(modelSupport.support, preference.Effort) {
			return effort, nil
		}
		break
	}
	return "", fmt.Errorf("Anthropic model %q does not support requested reasoning effort %q", model, preference.Effort)
}

// AnthropicProvider adapts Anthropic's native Messages API to Provider.
//
// It serves two endpoints that speak the same wire format with different
// credentials: Anthropic's own API, authenticated by the key the client was
// built with, and PostHog's LLM gateway, authenticated per request through
// tokens. The translation is identical either way, which is why this is one
// adapter with two constructors rather than two adapters.
type AnthropicProvider struct {
	client anthropic.Client
	model  string
	// tokens resolves the OAuth credential one request carries. nil means the
	// client itself holds a static key, which is the Anthropic-API case.
	tokens       OAuthTokenSource
	capabilities Capabilities
	label        string
	posthog      bool
}

var _ Provider = (*AnthropicProvider)(nil)

func NewAnthropicProvider(apiKey, baseURL, model string) *AnthropicProvider {
	return newAnthropicProviderWithTimeout(apiKey, baseURL, model, defaultRequestTimeout)
}

// NewAnthropicOAuthProvider builds the adapter for an endpoint that speaks
// anthropic-messages but authenticates with an OAuth login — PostHog's LLM
// gateway. tokens is required: an adapter with no way to resolve a credential
// could only ever produce 401s.
//
// The client is deliberately built with no API key — and with the SDK's
// env-derived one deleted, because NewClient reads ANTHROPIC_API_KEY on its own
// and a stray key in the environment must never end up authenticating a
// gateway request. authorize sets the bearer per request.
func NewAnthropicOAuthProvider(tokens OAuthTokenSource, baseURL, model string) *AnthropicProvider {
	opts := []option.RequestOption{
		option.WithHTTPClient(streamingHTTPClient(defaultRequestTimeout)),
		option.WithHeaderDel("X-Api-Key"),
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &AnthropicProvider{
		client:       anthropic.NewClient(opts...),
		model:        model,
		tokens:       tokens,
		capabilities: posthogClaudeCapabilities(),
		label:        "PostHog",
		posthog:      true,
	}
}

func newAnthropicProviderWithTimeout(apiKey, baseURL, model string, timeout time.Duration) *AnthropicProvider {
	opts := []option.RequestOption{option.WithAPIKey(apiKey), option.WithHTTPClient(streamingHTTPClient(timeout))}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &AnthropicProvider{client: anthropic.NewClient(opts...), model: model, capabilities: anthropicCapabilities, label: "Anthropic"}
}

// ValidateAnthropicKey verifies credentials against Anthropic's official Models API.
func ValidateAnthropicKey(ctx context.Context, baseURL, apiKey string) error {
	opts := []option.RequestOption{option.WithAPIKey(apiKey), option.WithRequestTimeout(defaultRequestTimeout)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	client := anthropic.NewClient(opts...)
	_, err := client.Models.List(ctx, anthropic.ModelListParams{Limit: param.NewOpt(int64(1))})
	return err
}

func (p *AnthropicProvider) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}
	if p.posthog && !posthogAnthropicModel(model) {
		return nil, fmt.Errorf("PostHog does not recognize model family %q", model)
	}
	effort, err := resolveAnthropicEffort(model, req.Reasoning)
	if err != nil {
		return nil, err
	}
	messages, err := toAnthropicMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	tools, err := toAnthropicTools(req.Tools)
	if err != nil {
		return nil, err
	}
	maxTokens := req.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = defaultAnthropicMaxOutputTokens
	}
	params := anthropic.MessageNewParams{
		Model:        anthropic.Model(model),
		MaxTokens:    int64(maxTokens),
		Messages:     messages,
		Tools:        tools,
		CacheControl: anthropic.NewCacheControlEphemeralParam(),
	}
	if effort != "" {
		params.OutputConfig.Effort = effort
	}
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}
	credential, err := p.authorize(ctx)
	if err != nil {
		return nil, err
	}

	out := make(chan Event)
	go p.runStream(ctx, out, params, model, credential)
	return out, nil
}

// authorize resolves the credential this request will carry, as request options
// rather than as a middleware — one resolution per request is what the seam
// promises, and a refresh that just failed would fail the same way on every SDK
// retry (see the same decision on CodexProvider.authorize).
//
// The gateway reads the token as a bearer, which is what its own tooling sends;
// the SDK's default x-api-key header is never set on this path.
func (p *AnthropicProvider) authorize(ctx context.Context) ([]option.RequestOption, error) {
	if p.tokens == nil {
		return nil, nil
	}
	token, err := p.tokens.OAuthToken(ctx)
	if err != nil {
		return nil, err
	}
	return []option.RequestOption{option.WithAuthToken(token.AccessToken)}, nil
}

type anthropicBlock struct {
	kind EventKind
	id   string
	name string
	args []byte
}

func (p *AnthropicProvider) runStream(ctx context.Context, out chan Event, params anthropic.MessageNewParams, model string, credential []option.RequestOption) {
	defer close(out)
	if !emit(ctx, out, Event{Kind: StepStarted}) {
		return
	}

	stream := p.client.Messages.NewStreaming(ctx, params, credential...)
	defer stream.Close()
	blocks := make(map[int64]*anthropicBlock)
	var usage *Usage
	for stream.Next() {
		event := stream.Current()
		switch e := event.AsAny().(type) {
		case anthropic.MessageStartEvent:
			mergeAnthropicUsage(&usage, e.Message.Usage)
		case anthropic.ContentBlockStartEvent:
			switch b := e.ContentBlock.AsAny().(type) {
			case anthropic.TextBlock:
				blocks[e.Index] = &anthropicBlock{kind: TextStarted}
				if !emit(ctx, out, Event{Kind: TextStarted}) {
					return
				}
			case anthropic.ThinkingBlock:
				blocks[e.Index] = &anthropicBlock{kind: ReasoningStarted}
				if !emit(ctx, out, Event{Kind: ReasoningStarted}) {
					return
				}
			case anthropic.ToolUseBlock:
				blocks[e.Index] = &anthropicBlock{kind: ToolInputStarted, id: b.ID, name: b.Name}
				if !emit(ctx, out, Event{Kind: ToolInputStarted, CallID: b.ID}) {
					return
				}
			}
		case anthropic.ContentBlockDeltaEvent:
			block := blocks[e.Index]
			switch d := e.Delta.AsAny().(type) {
			case anthropic.TextDelta:
				if !emit(ctx, out, Event{Kind: TextDelta, Text: d.Text}) {
					return
				}
			case anthropic.ThinkingDelta:
				if !emit(ctx, out, Event{Kind: ReasoningDelta, Text: d.Thinking}) {
					return
				}
			case anthropic.InputJSONDelta:
				if block != nil {
					block.args = append(block.args, d.PartialJSON...)
					if !emit(ctx, out, Event{Kind: ToolInputDelta, CallID: block.id, Input: json.RawMessage(d.PartialJSON)}) {
						return
					}
				}
			}
		case anthropic.ContentBlockStopEvent:
			block := blocks[e.Index]
			if block == nil {
				continue
			}
			switch block.kind {
			case TextStarted:
				if !emit(ctx, out, Event{Kind: TextEnded}) {
					return
				}
			case ReasoningStarted:
				if !emit(ctx, out, Event{Kind: ReasoningEnded}) {
					return
				}
			case ToolInputStarted:
				if !emit(ctx, out, Event{Kind: ToolInputEnded, CallID: block.id}) {
					return
				}
				if !json.Valid(block.args) {
					err := fmt.Errorf("anthropic tool call %q input: invalid JSON", block.id)
					emit(ctx, out, Event{Kind: StepFailed, Err: err, Text: fmt.Sprintf("%s (%s): %v", p.label, model, err)})
					return
				}
				if !emit(ctx, out, Event{Kind: ToolCall, CallID: block.id, ToolName: block.name, Input: json.RawMessage(block.args)}) {
					return
				}
			}
		case anthropic.MessageDeltaEvent:
			mergeAnthropicDeltaUsage(&usage, e.Usage)
		}
	}
	if err := stream.Err(); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		if isAnthropicContextOverflow(err) {
			err = &ContextOverflowError{Message: err.Error()}
		}
		emit(ctx, out, Event{Kind: StepFailed, Err: err, Text: fmt.Sprintf("%s (%s): %v", p.label, model, err)})
		return
	}
	emit(ctx, out, Event{Kind: StepEnded, Usage: usage})
}

type reportedUsageFields struct {
	input, output, reasoning, cacheRead, cacheWrite                          int
	inputPresent, outputPresent, reasoningPresent, readPresent, writePresent bool
}

func mergeAnthropicUsage(dst **Usage, src anthropic.Usage) {
	mergeReportedUsage(dst, reportedUsageFields{
		input: int(src.InputTokens), output: int(src.OutputTokens),
		reasoning: int(src.OutputTokensDetails.ThinkingTokens),
		cacheRead: int(src.CacheReadInputTokens), cacheWrite: int(src.CacheCreationInputTokens),
		inputPresent: src.JSON.InputTokens.Valid(), outputPresent: src.JSON.OutputTokens.Valid(),
		reasoningPresent: src.OutputTokensDetails.JSON.ThinkingTokens.Valid(),
		readPresent:      src.JSON.CacheReadInputTokens.Valid(), writePresent: src.JSON.CacheCreationInputTokens.Valid(),
	})
}

func mergeAnthropicDeltaUsage(dst **Usage, src anthropic.MessageDeltaUsage) {
	mergeReportedUsage(dst, reportedUsageFields{
		input: int(src.InputTokens), output: int(src.OutputTokens),
		reasoning: int(src.OutputTokensDetails.ThinkingTokens),
		cacheRead: int(src.CacheReadInputTokens), cacheWrite: int(src.CacheCreationInputTokens),
		inputPresent: src.JSON.InputTokens.Valid(), outputPresent: src.JSON.OutputTokens.Valid(),
		reasoningPresent: src.OutputTokensDetails.JSON.ThinkingTokens.Valid(),
		readPresent:      src.JSON.CacheReadInputTokens.Valid(), writePresent: src.JSON.CacheCreationInputTokens.Valid(),
	})
}

func mergeReportedUsage(dst **Usage, fields reportedUsageFields) {
	if !fields.inputPresent && !fields.outputPresent && !fields.reasoningPresent && !fields.readPresent && !fields.writePresent {
		return
	}
	if *dst == nil {
		*dst = &Usage{}
	}
	if fields.inputPresent {
		(*dst).InputTokens = fields.input
	}
	if fields.outputPresent {
		(*dst).OutputTokens = fields.output
	}
	if fields.reasoningPresent {
		(*dst).ReasoningTokens = fields.reasoning
	}
	if fields.readPresent {
		(*dst).CacheReadTokens = fields.cacheRead
	}
	if fields.writePresent {
		(*dst).CacheWriteTokens = fields.cacheWrite
	}
	(*dst).CacheableInputTokens = (*dst).InputTokens + (*dst).CacheReadTokens + (*dst).CacheWriteTokens
}

func isAnthropicContextOverflow(err error) bool {
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) || apiErr.Type() != anthropic.ErrorTypeInvalidRequestError {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "prompt is too long") || strings.Contains(message, "context window")
}

// toAnthropicMessages projects history into Anthropic content blocks. User
// text and image parts retain their order; images are encoded into native
// base64 sources. Images on every other role and unknown parts are refused
// before authorization or endpoint contact.
func toAnthropicMessages(messages []Message) ([]anthropic.MessageParam, error) {
	out := make([]anthropic.MessageParam, 0, len(messages))
	for _, message := range messages {
		if message.Role == "user" {
			blocks, err := toAnthropicUserBlocks(message.Parts)
			if err != nil {
				return nil, fmt.Errorf("anthropic: %w", err)
			}
			out = append(out, anthropic.NewUserMessage(blocks...))
			continue
		}

		text, err := message.TextOnly()
		if err != nil {
			return nil, fmt.Errorf("anthropic: %w", err)
		}
		blocks := make([]anthropic.ContentBlockParamUnion, 0, 1+len(message.ToolCalls))
		if text != "" {
			blocks = append(blocks, anthropic.NewTextBlock(text))
		}
		switch message.Role {
		case "assistant":
			for _, call := range message.ToolCalls {
				var input any
				if err := json.Unmarshal(call.Arguments, &input); err != nil {
					return nil, fmt.Errorf("anthropic tool call %q input: %w", call.ID, err)
				}
				blocks = append(blocks, anthropic.NewToolUseBlock(call.ID, input, call.Name))
			}
			out = append(out, anthropic.NewAssistantMessage(blocks...))
		case "tool":
			out = append(out, anthropic.NewUserMessage(anthropic.NewToolResultBlock(message.ToolCallID, text, message.IsError)))
		default:
			return nil, fmt.Errorf("anthropic: unsupported message role %q", message.Role)
		}
	}
	return out, nil
}

func toAnthropicUserBlocks(parts []Part) ([]anthropic.ContentBlockParamUnion, error) {
	blocks := make([]anthropic.ContentBlockParamUnion, 0, len(parts))
	var text strings.Builder
	flushText := func() {
		if text.Len() > 0 {
			blocks = append(blocks, anthropic.NewTextBlock(text.String()))
			text.Reset()
		}
	}
	for _, part := range parts {
		switch part.Kind {
		case TextPart:
			text.WriteString(part.Text)
		case ImagePart:
			flushText()
			blocks = append(blocks, anthropic.NewImageBlockBase64(part.MediaType, base64.StdEncoding.EncodeToString(part.Data)))
		default:
			return nil, &UnsupportedPartError{Kind: part.Kind}
		}
	}
	flushText()
	return blocks, nil
}

func toAnthropicTools(tools []ToolDef) ([]anthropic.ToolUnionParam, error) {
	out := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, tool := range tools {
		var schema map[string]any
		if err := json.Unmarshal(tool.Schema, &schema); err != nil {
			return nil, fmt.Errorf("anthropic tool %q schema: %w", tool.Name, err)
		}
		input := anthropic.ToolInputSchemaParam{ExtraFields: schema}
		delete(input.ExtraFields, "type")
		paramTool := anthropic.ToolParam{Name: tool.Name, InputSchema: input}
		if tool.Description != "" {
			paramTool.Description = param.NewOpt(tool.Description)
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: &paramTool})
	}
	return out, nil
}

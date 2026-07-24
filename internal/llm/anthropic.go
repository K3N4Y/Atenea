package llm

import (
	"context"
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

// AnthropicProvider adapts Anthropic's native Messages API to Provider.
type AnthropicProvider struct {
	client anthropic.Client
	model  string
}

var _ Provider = (*AnthropicProvider)(nil)

func NewAnthropicProvider(apiKey, baseURL, model string) *AnthropicProvider {
	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithRequestTimeout(defaultRequestTimeout),
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &AnthropicProvider{client: anthropic.NewClient(opts...), model: model}
}

func newAnthropicProviderWithTimeout(apiKey, baseURL, model string, timeout time.Duration) *AnthropicProvider {
	opts := []option.RequestOption{option.WithAPIKey(apiKey), option.WithRequestTimeout(timeout)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &AnthropicProvider{client: anthropic.NewClient(opts...), model: model}
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
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}

	out := make(chan Event)
	go p.runStream(ctx, out, params, model)
	return out, nil
}

type anthropicBlock struct {
	kind EventKind
	id   string
	name string
	args []byte
}

func (p *AnthropicProvider) runStream(ctx context.Context, out chan Event, params anthropic.MessageNewParams, model string) {
	defer close(out)
	if !emit(ctx, out, Event{Kind: StepStarted}) {
		return
	}

	stream := p.client.Messages.NewStreaming(ctx, params)
	defer stream.Close()
	blocks := make(map[int64]*anthropicBlock)
	var usage *Usage
	for stream.Next() {
		event := stream.Current()
		switch e := event.AsAny().(type) {
		case anthropic.MessageStartEvent:
			u := e.Message.Usage
			usage = &Usage{InputTokens: int(u.InputTokens), OutputTokens: int(u.OutputTokens), CacheReadTokens: int(u.CacheReadInputTokens), CacheWriteTokens: int(u.CacheCreationInputTokens)}
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
					emit(ctx, out, Event{Kind: StepFailed, Err: err, Text: fmt.Sprintf("Anthropic (%s): %v", model, err)})
					return
				}
				if !emit(ctx, out, Event{Kind: ToolCall, CallID: block.id, ToolName: block.name, Input: json.RawMessage(block.args)}) {
					return
				}
			}
		case anthropic.MessageDeltaEvent:
			if usage == nil {
				usage = &Usage{}
			}
			usage.InputTokens = int(e.Usage.InputTokens)
			usage.OutputTokens = int(e.Usage.OutputTokens)
			usage.ReasoningTokens = int(e.Usage.OutputTokensDetails.ThinkingTokens)
			usage.CacheReadTokens = int(e.Usage.CacheReadInputTokens)
			usage.CacheWriteTokens = int(e.Usage.CacheCreationInputTokens)
		}
	}
	if err := stream.Err(); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		if isAnthropicContextOverflow(err) {
			err = &ContextOverflowError{Message: err.Error()}
		}
		emit(ctx, out, Event{Kind: StepFailed, Err: err, Text: fmt.Sprintf("Anthropic (%s): %v", model, err)})
		return
	}
	emit(ctx, out, Event{Kind: StepEnded, Usage: usage})
}

func isAnthropicContextOverflow(err error) bool {
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) || apiErr.Type() != anthropic.ErrorTypeInvalidRequestError {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "prompt is too long") || strings.Contains(message, "context window")
}

func toAnthropicMessages(messages []Message) ([]anthropic.MessageParam, error) {
	out := make([]anthropic.MessageParam, 0, len(messages))
	for _, message := range messages {
		blocks := make([]anthropic.ContentBlockParamUnion, 0, 1+len(message.ToolCalls))
		if message.Text != "" {
			blocks = append(blocks, anthropic.NewTextBlock(message.Text))
		}
		switch message.Role {
		case "user":
			out = append(out, anthropic.NewUserMessage(blocks...))
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
			out = append(out, anthropic.NewUserMessage(anthropic.NewToolResultBlock(message.ToolCallID, message.Text, message.IsError)))
		default:
			return nil, fmt.Errorf("anthropic: unsupported message role %q", message.Role)
		}
	}
	return out, nil
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

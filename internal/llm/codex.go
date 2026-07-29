package llm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/option"
	"github.com/openai/openai-go/v2/packages/param"
	"github.com/openai/openai-go/v2/responses"
	"github.com/openai/openai-go/v2/shared"
)

// The dialect a ChatGPT subscription speaks.
//
// A subscription token is not an API key wearing a different hat: it is rejected
// by api.openai.com, it is rejected by Chat Completions, and the one endpoint that
// accepts it — the codex backend's Responses API — wants the system prompt in a
// field of its own, refuses to be told how many tokens to produce, and identifies
// the caller with a header rather than only a bearer. That is four differences in
// the shape of a request, which is what a wire format IS in this codebase, so this
// is its own adapter rather than an option on the OpenAI one.
const (
	// codexOriginator is how the backend attributes the traffic. It is atenea's own
	// name on purpose: the client id we authenticate with is Codex's because OpenAI
	// registers no other, and sending their originator on top of that would claim to
	// be a program we are not.
	codexOriginator = "atenea"
	// codexUserAgent is a real user agent, because the backend rejects requests
	// without one.
	codexUserAgent = "atenea/1.0 (+https://github.com/K3N4Y/atenea)"
	// codexBetaHeader opts into the Responses API the codex backend serves.
	codexBetaHeader = "responses=experimental"
	// codexLabel is what this endpoint is called in the sentences a user reads. It
	// is the subscription's name and not the provider id, because "openai-codex"
	// means nothing to someone who signed in with ChatGPT.
	codexLabel = "ChatGPT"
)

// CodexProvider adapts the codex backend's Responses API to Provider.
//
// It translates the turn the same way the other adapters do — StepStarted, then
// reasoning and text bracketed by their own started/ended pairs, then one
// ToolInputEnded plus ToolCall per function call, then StepEnded carrying the
// usage — over a different event vocabulary: the Responses stream names items
// (`response.output_item.added`) where chat completions named choices and deltas.
//
// It holds no credential. Every request asks tokens for one, which is what lets a
// turn outlive the hour an access token lives; see [OAuthTokenSource].
type CodexProvider struct {
	client openai.Client
	tokens OAuthTokenSource
	model  string
	// effort, summary and verbosity are what this adapter asks the model for. They
	// are per-instance because they are the knobs the codex backend exposes that a
	// caller might reasonably want to move, and the zero value asks for none of
	// them, which is what keeps an unset option out of the request body.
	effort    shared.ReasoningEffort
	summary   shared.ReasoningSummary
	verbosity responses.ResponseTextConfigVerbosity
}

var _ Provider = (*CodexProvider)(nil)

// CodexOption adjusts a CodexProvider at construction.
type CodexOption func(*CodexProvider)

// The reasoning and verbosity values this dialect accepts, as plain strings so a
// caller does not have to depend on the vendor SDK to name one.
const (
	CodexEffortMinimal = "minimal"
	CodexEffortLow     = "low"
	CodexEffortMedium  = "medium"
	CodexEffortHigh    = "high"

	CodexSummaryAuto     = "auto"
	CodexSummaryConcise  = "concise"
	CodexSummaryDetailed = "detailed"

	CodexVerbosityLow    = "low"
	CodexVerbosityMedium = "medium"
	CodexVerbosityHigh   = "high"
)

// WithCodexReasoning asks the model for a given reasoning effort and a summary of
// it. The summary is what becomes Reasoning* events: the raw chain of thought is
// never sent in the clear, so a turn with no summary shows no thinking at all.
func WithCodexReasoning(effort, summary string) CodexOption {
	return func(p *CodexProvider) {
		p.effort = shared.ReasoningEffort(effort)
		p.summary = shared.ReasoningSummary(summary)
	}
}

// WithCodexVerbosity constrains how much prose the model writes around its work.
func WithCodexVerbosity(verbosity string) CodexOption {
	return func(p *CodexProvider) {
		p.verbosity = responses.ResponseTextConfigVerbosity(verbosity)
	}
}

// NewCodexProvider builds the adapter against baseURL, which must be the codex
// backend's root (the SDK appends `responses` to it). tokens is required: an
// adapter with no way to resolve a credential could only ever produce 401s, and
// failing every turn with a legible error beats failing every turn with the
// endpoint's.
func NewCodexProvider(tokens OAuthTokenSource, baseURL, model string, opts ...CodexOption) *CodexProvider {
	return newCodexProviderWithTimeout(tokens, baseURL, model, defaultRequestTimeout, opts...)
}

// newCodexProviderWithTimeout is the real constructor, with the per-request
// timeout injectable so a test can prove a hung stream is cut without waiting out
// the long default.
func newCodexProviderWithTimeout(tokens OAuthTokenSource, baseURL, model string, timeout time.Duration, opts ...CodexOption) *CodexProvider {
	p := &CodexProvider{tokens: tokens, model: model}
	for _, opt := range opts {
		opt(p)
	}
	p.client = openai.NewClient(
		// The bearer is blanked here and set per request by authorize, so a stray
		// OPENAI_API_KEY in the environment cannot end up authenticating a
		// subscription request.
		option.WithAPIKey(""),
		option.WithBaseURL(strings.TrimRight(baseURL, "/")+"/"),
		option.WithRequestTimeout(timeout),
		option.WithHeader("originator", codexOriginator),
		option.WithHeader("User-Agent", codexUserAgent),
		option.WithHeader("OpenAI-Beta", codexBetaHeader),
		option.WithHeader("accept", "text/event-stream"),
	)
	return p
}

// authorize resolves the credential this request will carry, as request options
// rather than as a middleware.
//
// A middleware would run again on every SDK retry, which sounds better and is
// worse: a refresh that just failed fails again the same way, so the only thing
// three attempts buy is seven seconds between the user and the sentence telling
// them to log in. One resolution per request is what the seam promises, and the
// SDK's whole retry window is a few seconds inside the freshness margin the
// source already keeps.
func (p *CodexProvider) authorize(ctx context.Context) ([]option.RequestOption, error) {
	if p.tokens == nil {
		return nil, errors.New("no ChatGPT credential source is wired for this provider")
	}
	token, err := p.tokens.OAuthToken(ctx)
	if err != nil {
		return nil, err
	}
	return []option.RequestOption{
		option.WithHeader("Authorization", "Bearer "+token.AccessToken),
		option.WithHeader("chatgpt-account-id", token.AccountID),
	}, nil
}

func (p *CodexProvider) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}
	input, err := toCodexInput(req.Messages)
	if err != nil {
		return nil, err
	}
	credential, err := p.authorize(ctx)
	if err != nil {
		return nil, err
	}

	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(model),
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: input},
		// Nothing is stored server-side: a coding agent's transcript is the user's
		// own history, and this endpoint is stateless anyway — every turn replays
		// what it needs.
		Store: openai.Bool(false),
		// The encrypted reasoning comes back so a stateless multi-turn conversation
		// can carry it; see the note on what this adapter does not close.
		Include:           []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent},
		ToolChoice:        responses.ResponseNewParamsToolChoiceUnion{OfToolChoiceMode: param.NewOpt(responses.ToolChoiceOptionsAuto)},
		ParallelToolCalls: openai.Bool(true),
	}
	// The system prompt is a field, not a message. Sending it as a message works
	// against Chat Completions and is silently deprioritized here, which shows up
	// as a model that ignores its instructions rather than as an error.
	if req.System != "" {
		params.Instructions = openai.String(req.System)
	}
	if tools := toCodexTools(req.Tools); tools != nil {
		params.Tools = tools
	}
	if req.SessionKey != "" {
		params.PromptCacheKey = openai.String(req.SessionKey)
	}
	if p.effort != "" || p.summary != "" {
		params.Reasoning = shared.ReasoningParam{Effort: p.effort, Summary: p.summary}
	}
	if p.verbosity != "" {
		params.Text = responses.ResponseTextConfigParam{Verbosity: p.verbosity}
	}
	// req.MaxOutputTokens is DROPPED, deliberately and only here. The codex backend
	// rejects a subscription request that names a ceiling; the Codex CLI never
	// sends one and neither does any other client that works against it. A host
	// that reserves the ceiling in its context estimate is unaffected — it reserves
	// what it would have asked for, which is more conservative than what happens.

	out := make(chan Event)
	go p.runStream(ctx, out, params, model, req.SessionKey, credential)
	return out, nil
}

// codexCall is one function call being streamed. The Responses stream keys its
// argument deltas by ITEM id and the tool result pairs by CALL id, so both have to
// be remembered together.
type codexCall struct {
	callID string
	name   string
	args   strings.Builder
}

func (p *CodexProvider) runStream(ctx context.Context, out chan Event, params responses.ResponseNewParams, model, sessionKey string, credential []option.RequestOption) {
	defer close(out)
	if !emit(ctx, out, Event{Kind: StepStarted}) {
		return
	}

	opts := append(credential,
		option.WithHeader("session-id", codexSessionID(sessionKey)),
		option.WithHeader("x-client-request-id", randomUUID()),
		retryTelemetry(ctx, out, codexLabel, model),
	)
	stream := p.client.Responses.NewStreaming(ctx, params, opts...)
	defer stream.Close()

	var usage *Usage
	textOpen, reasoningOpen := false, false
	calls := map[string]*codexCall{}

	// close the open block before another kind of content starts. Reasoning always
	// closes before text opens, the way every other adapter here brackets it.
	closeBlocks := func() bool {
		if reasoningOpen {
			if !emit(ctx, out, Event{Kind: ReasoningEnded}) {
				return false
			}
			reasoningOpen = false
		}
		if textOpen {
			if !emit(ctx, out, Event{Kind: TextEnded}) {
				return false
			}
			textOpen = false
		}
		return true
	}

	for stream.Next() {
		event := stream.Current()
		switch event.Type {
		case "response.output_item.added":
			if event.Item.Type != "function_call" {
				continue
			}
			if !closeBlocks() {
				return
			}
			call := &codexCall{callID: event.Item.CallID, name: event.Item.Name}
			calls[event.Item.ID] = call
			if !emit(ctx, out, Event{Kind: ToolInputStarted, CallID: call.callID}) {
				return
			}
		case "response.function_call_arguments.delta":
			call := calls[event.ItemID]
			if call == nil || event.Delta == "" {
				continue
			}
			call.args.WriteString(event.Delta)
			if !emit(ctx, out, Event{Kind: ToolInputDelta, CallID: call.callID, Input: json.RawMessage(event.Delta)}) {
				return
			}
		case "response.output_item.done":
			call := calls[event.Item.ID]
			if call == nil {
				continue
			}
			delete(calls, event.Item.ID)
			if !emit(ctx, out, Event{Kind: ToolInputEnded, CallID: call.callID}) {
				return
			}
			// The done item carries the whole argument string, which is what a host
			// hands the tool; the accumulated deltas are the fallback for a backend
			// that streams them and does not repeat them.
			arguments := event.Item.Arguments
			if arguments == "" {
				arguments = call.args.String()
			}
			if !json.Valid([]byte(arguments)) {
				err := fmt.Errorf("codex tool call %q input: invalid JSON", call.callID)
				emit(ctx, out, Event{Kind: StepFailed, Err: err, Text: fmt.Sprintf("ChatGPT (%s): %v", model, err)})
				return
			}
			if !emit(ctx, out, Event{Kind: ToolCall, CallID: call.callID, ToolName: call.name, Input: json.RawMessage(arguments)}) {
				return
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			if event.Delta == "" {
				continue
			}
			if textOpen {
				if !emit(ctx, out, Event{Kind: TextEnded}) {
					return
				}
				textOpen = false
			}
			if !reasoningOpen {
				if !emit(ctx, out, Event{Kind: ReasoningStarted}) {
					return
				}
				reasoningOpen = true
			}
			if !emit(ctx, out, Event{Kind: ReasoningDelta, Text: event.Delta}) {
				return
			}
		case "response.output_text.delta":
			if event.Delta == "" {
				continue
			}
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
			if !emit(ctx, out, Event{Kind: TextDelta, Text: event.Delta}) {
				return
			}
		case "response.completed":
			usage = codexUsage(event.Response.Usage)
		case "response.failed", "response.incomplete":
			if !closeBlocks() {
				return
			}
			// A turn that stops early reports why under incomplete_details rather
			// than as an error, and a turn that failed reports it as one. Reading
			// both here is what keeps "the model was cut off" from surfacing as a
			// turn with no reason at all.
			message := event.Response.Error.Message
			if message == "" {
				message = event.Response.IncompleteDetails.Reason
			}
			err := codexResponseError(message, string(event.Response.Error.Code))
			emit(ctx, out, Event{Kind: StepFailed, Err: err, Text: fmt.Sprintf("ChatGPT (%s): %v", model, err)})
			return
		case "error":
			if !closeBlocks() {
				return
			}
			err := codexResponseError(event.Message, event.Code)
			emit(ctx, out, Event{Kind: StepFailed, Err: err, Text: fmt.Sprintf("ChatGPT (%s): %v", model, err)})
			return
		}
	}

	if !closeBlocks() {
		return
	}
	if err := stream.Err(); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		err = codexError(err)
		emit(ctx, out, Event{Kind: StepFailed, Err: err, Text: fmt.Sprintf("ChatGPT (%s): %v", model, err)})
		return
	}
	// A call the backend opened and never closed leaves a host waiting for input
	// that will not arrive, so the turn fails rather than ending as if it had run.
	for _, call := range calls {
		if !emit(ctx, out, Event{Kind: ToolInputEnded, CallID: call.callID}) {
			return
		}
		err := fmt.Errorf("codex tool call %q was never completed", call.callID)
		emit(ctx, out, Event{Kind: StepFailed, Err: err, Text: fmt.Sprintf("ChatGPT (%s): %v", model, err)})
		return
	}
	emit(ctx, out, Event{Kind: StepEnded, Usage: usage})
}

func codexUsage(usage responses.ResponseUsage) *Usage {
	if usage.TotalTokens == 0 && usage.InputTokens == 0 && usage.OutputTokens == 0 {
		return nil
	}
	return &Usage{
		InputTokens:          int(usage.InputTokens),
		OutputTokens:         int(usage.OutputTokens),
		ReasoningTokens:      int(usage.OutputTokensDetails.ReasoningTokens),
		CacheReadTokens:      int(usage.InputTokensDetails.CachedTokens),
		CacheableInputTokens: int(usage.InputTokens),
	}
}

// codexError turns an SDK transport failure into something a user can act on.
//
// The SDK's own error stringifies as the whole raw response body, which for the
// failure that actually happens here — a subscription that has run out of its
// window — is a wall of JSON that never says the words "usage limit". A context
// overflow is classified too, because a host acts on that one by compacting and
// retrying rather than by showing it.
func codexError(err error) error {
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		return err
	}
	message := apiErr.Message
	if message == "" {
		message = apiErr.Code
	}
	switch {
	case apiErr.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("ChatGPT subscription limit reached: %s", codexReason(message, "the plan's usage window is exhausted; it resets on its own schedule"))
	case apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden:
		return fmt.Errorf("ChatGPT subscription rejected the request: %s", codexReason(message, "the credential is no longer accepted; log in again"))
	case isCodexContextOverflow(apiErr.StatusCode, message):
		return &ContextOverflowError{Message: message}
	case message != "":
		return errors.New(message)
	default:
		return err
	}
}

// codexResponseError is codexError for a failure the backend reported INSIDE the
// stream, where there is no status code to read — only a code and a message.
func codexResponseError(message string, code string) error {
	if message == "" {
		message = code
	}
	switch {
	case code == "rate_limit_exceeded":
		return fmt.Errorf("ChatGPT subscription limit reached: %s", codexReason(message, "the plan's usage window is exhausted"))
	case isCodexContextOverflow(0, message):
		return &ContextOverflowError{Message: message}
	case message != "":
		return errors.New(message)
	default:
		return errors.New("the ChatGPT backend ended the turn without a reason")
	}
}

func codexReason(message, fallback string) string {
	if message = strings.TrimSpace(message); message != "" {
		return message
	}
	return fallback
}

func isCodexContextOverflow(status int, message string) bool {
	if status != 0 && status != http.StatusBadRequest {
		return false
	}
	lower := strings.ToLower(message)
	return strings.Contains(lower, "context_length_exceeded") ||
		strings.Contains(lower, "context length") ||
		strings.Contains(lower, "context window") ||
		strings.Contains(lower, "too long")
}

// toCodexInput projects the history into Responses items. The vocabulary is
// different from chat completions in a way that matters: a tool call is an item of
// its own (`function_call`) beside the assistant's message rather than a field
// inside it, and its result is another item (`function_call_output`) rather than a
// message with a role.
//
// This dialect carries text only, so a message with a part of any other kind fails
// the whole request rather than losing the part: an unspoken drop puts a
// conversation with a hole in it in front of the model.
func toCodexInput(messages []Message) (responses.ResponseInputParam, error) {
	items := make(responses.ResponseInputParam, 0, len(messages))
	for _, message := range messages {
		text, err := message.TextOnly()
		if err != nil {
			return nil, fmt.Errorf("codex: %w", err)
		}
		switch message.Role {
		case "assistant":
			if text != "" {
				items = append(items, codexMessage(text, responses.EasyInputMessageRoleAssistant))
			}
			for _, call := range message.ToolCalls {
				arguments := string(call.Arguments)
				// A tool with no arguments comes back as an empty input, and the
				// endpoint wants JSON in this field whatever the tool takes.
				if arguments == "" {
					arguments = "{}"
				}
				items = append(items, responses.ResponseInputItemParamOfFunctionCall(arguments, call.ID, call.Name))
			}
		case "tool":
			items = append(items, responses.ResponseInputItemParamOfFunctionCallOutput(message.ToolCallID, text))
		case "system":
			items = append(items, codexMessage(text, responses.EasyInputMessageRoleSystem))
		default:
			items = append(items, codexMessage(text, responses.EasyInputMessageRoleUser))
		}
	}
	return items, nil
}

// codexMessage is one history message as an input item. The type is stated rather
// than left to be inferred: every client that works against this backend states
// it, and an item whose kind a stricter parser has to guess is not worth the two
// bytes saved.
func codexMessage(text string, role responses.EasyInputMessageRole) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemUnionParam{OfMessage: &responses.EasyInputMessageParam{
		Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(text)},
		Role:    role,
		Type:    responses.EasyInputMessageTypeMessage,
	}}
}

// toCodexTools materializes each ToolDef as a function tool. Strict mode is off:
// it is what the Codex CLI sends, and a strict schema turns a tool whose
// parameters the model got slightly wrong into a refused turn instead of a
// repairable call.
func toCodexTools(tools []ToolDef) []responses.ToolUnionParam {
	if len(tools) == 0 {
		return nil
	}
	out := make([]responses.ToolUnionParam, 0, len(tools))
	for _, tool := range tools {
		parameters := map[string]any{"type": "object", "properties": map[string]any{}}
		if len(tool.Schema) > 0 {
			var decoded map[string]any
			if err := json.Unmarshal(tool.Schema, &decoded); err == nil {
				parameters = decoded
			}
		}
		definition := responses.ToolParamOfFunction(tool.Name, parameters, false)
		if tool.Description != "" {
			definition.OfFunction.Description = openai.String(tool.Description)
		}
		out = append(out, definition)
	}
	return out
}

// codexSessionID is the `session-id` header, which the backend wants as a UUID.
// It is derived from the host's own conversation key so every turn of one
// conversation carries the same one, and a host that has no key gets a fresh
// value rather than a shared constant.
func codexSessionID(sessionKey string) string {
	if sessionKey == "" {
		return randomUUID()
	}
	sum := sha256.Sum256([]byte(sessionKey))
	return formatUUID(sum[:16])
}

func randomUUID() string {
	var raw [16]byte
	// crypto/rand.Read does not fail: an unusable entropy source panics inside the
	// runtime rather than returning an error here.
	_, _ = rand.Read(raw[:])
	return formatUUID(raw[:])
}

// formatUUID stamps the version 4 and variant bits onto 16 bytes and renders
// them, so a derived or random value is still a well-formed UUID.
func formatUUID(raw []byte) string {
	buf := make([]byte, 16)
	copy(buf, raw)
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	hexed := hex.EncodeToString(buf)
	return hexed[0:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:32]
}

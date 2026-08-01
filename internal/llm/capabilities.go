package llm

import contract "github.com/K3N4Y/atenea/agentcore/llm"
import "strings"

// What each adapter shipped here declares about itself, and the context windows
// of the models it serves.
//
// The windows are declared per wire format rather than in one global table
// because a model id only means something inside the dialect that names it:
// `claude-opus-4-8` is Anthropic's native id and `anthropic/claude-opus-4.8` is
// OpenRouter's id for the same family. A single map keyed by id had to hold both
// and could not say which adapter either belonged to — so it answered for models
// the caller was not talking to, and had nothing to say about an adapter added
// from outside this package.

// anthropicWindows are Anthropic's native ids, which carry no provider prefix.
// The generally available 200K window is what goes here: extended-context betas
// must not inflate a host's preventive-compaction budget.
var anthropicWindows = map[string]int{
	"claude-opus-4-8":  200_000,
	"claude-fable-5":   200_000,
	"claude-sonnet-5":  200_000,
	"claude-haiku-4-5": 200_000,
}

// openAIWindows are OpenAI's own ids, which carry no vendor prefix (gpt-4o, not
// openai/gpt-4o — that one is OpenRouter's spelling of it).
var openAIWindows = map[string]int{
	"gpt-5.6":       1_050_000,
	"gpt-5.6-terra": 1_050_000,
	"gpt-5.6-luna":  1_050_000,
	"gpt-5.4-mini":  400_000,
	"gpt-5.4-nano":  400_000,
	"gpt-5":         400_000,
	"gpt-5-mini":    400_000,
	"gpt-4.1":       1_047_576,
	"gpt-4.1-mini":  1_047_576,
	"gpt-4.1-nano":  1_047_576,
	"gpt-4o":        128_000,
	"gpt-4o-mini":   128_000,
}

// codexWindows are the ids the codex backend serves a ChatGPT subscription. They
// are their own table rather than an entry in openAIWindows for the reason stated
// above: the same vendor reached through another dialect is another set of ids,
// and this backend serves a codex-tuned list the public API does not.
//
// The windows are curated data, like the model list in the catalog they mirror:
// the endpoint publishes no /models and no window, so the honest thing is one
// number per id in one place that is cheap to correct. The gpt-5.4 family's
// published window is what they all get, which is the conservative reading —
// under-declaring costs an early compaction, over-declaring costs a failed turn.
var codexWindows = map[string]int{
	"gpt-5.5":             400_000,
	"gpt-5.4":             400_000,
	"gpt-5.4-mini":        400_000,
	"gpt-5.3-codex-spark": 400_000,
}

// openRouterWindows are OpenRouter's ids, which prefix the model with its vendor.
// The curated free models are here too: they are OpenRouter's catalog, and the
// TUI used to carry them in a table of its own.
var openRouterWindows = map[string]int{
	"anthropic/claude-opus-4.8":   200_000,
	"anthropic/claude-sonnet-4.5": 200_000,
	"anthropic/claude-3.5-sonnet": 200_000,
	"openai/gpt-4o":               128_000,
	"google/gemini-2.5-pro":       1_048_576,
	"tencent/hy3:free":            262_144,
	"poolside/laguna-xs-2.1:free": 262_144,
	"cohere/north-mini-code:free": 256_000,
}

// posthogWindows are the ids PostHog's LLM gateway serves off its
// anthropic-messages surface. Their own table, per the note above: the same
// vendor's models reached through the gateway are the gateway's catalog, and it
// advertises 1M windows for the opus/sonnet family where Anthropic's public API
// declares 200K.
//
// The numbers mirror what the gateway's /v1/models declares. They are trusted
// as given — under-declaring costs an early compaction, over-declaring costs a
// failed turn — and this table is the one place to correct if real requests
// disagree.
var posthogWindows = map[string]int{
	"claude-opus-5":     1_000_000,
	"claude-opus-4-8":   1_000_000,
	"claude-opus-4-7":   1_000_000,
	"claude-sonnet-5":   1_000_000,
	"claude-sonnet-4-6": 1_000_000,
	"claude-haiku-4-5":  200_000,
	"gpt-5.6-sol":       1_050_000,
	"gpt-5.6-terra":     1_050_000,
	"gpt-5.6-luna":      1_050_000,
	"gpt-5.5":           1_050_000,
	"gpt-5.4":           1_050_000,
	"gpt-5.3-codex":     272_000,
	"gpt-5-mini":        272_000,
}

var anthropicCapabilities = Capabilities{
	Streaming: true,
	Tools:     true,
	// The adapter never sends Anthropic's `thinking` parameter, so it does not
	// ask for reasoning; it forwards a ThinkingBlock if one arrives anyway.
	Reasoning: false,
	// CacheControl goes on every request, with no key and no opt-out, so
	// Request.SessionKey buys nothing here.
	PromptCaching: ImplicitPromptCaching,
	// The SDK retries transient failures; this adapter does not report them, so a
	// host sees a pause and cannot tell it apart from a slow model.
	RetryTelemetry:         false,
	DefaultMaxOutputTokens: defaultAnthropicMaxOutputTokens,
	ContextWindows:         anthropicWindows,
}

// posthogCapabilities is the gateway's catalog-wide behavior. Reasoning is
// intentionally represented per model: Claude uses the Anthropic adapter and
// GPT uses Responses with a requested summary.
var posthogCapabilities = Capabilities{
	Streaming:              true,
	Tools:                  true,
	PromptCaching:          ImplicitPromptCaching,
	RetryTelemetry:         false,
	DefaultMaxOutputTokens: defaultAnthropicMaxOutputTokens,
	ContextWindows:         posthogWindows,
}

func posthogCapabilitiesFor(models ...string) Capabilities {
	caps := posthogCapabilities
	if len(models) == 0 {
		return caps
	}
	caps.ReasoningModels = make(map[string]bool, len(models))
	for _, model := range models {
		if strings.HasPrefix(model, "gpt-") {
			caps.ReasoningModels[model] = true
		} else if strings.HasPrefix(model, "claude-") {
			caps.ReasoningModels[model] = false
		}
	}
	return caps
}

// Capabilities is what this adapter does with the anthropic-messages wire
// format, against whichever catalog the instance was built for.
func (p *AnthropicProvider) Capabilities() Capabilities { return p.capabilities }

// capabilities is what the dialect this profile selects can do. It is the profile
// made observable: what used to be decided at construction and then forgotten is
// now something the built provider can be asked about.
func (c compatibilityProfile) capabilities() Capabilities {
	caps := Capabilities{
		Streaming:      true,
		Tools:          true,
		RetryTelemetry: true, // retryTiming reports every attempt as StepRetrying
	}
	switch c {
	case compatibilityOpenAI:
		caps.PromptCaching = KeyedPromptCaching // prompt_cache_key
		caps.ContextWindows = openAIWindows
	case compatibilityOpenRouter:
		caps.PromptCaching = KeyedPromptCaching // session_id
		caps.ContextWindows = openRouterWindows
	}
	// compatibilityNeutral is whatever the endpoint happens to be — a local
	// runtime, an unrecognized gateway — so there is no catalog to declare and no
	// vendor cache field to send.
	return caps
}

// Capabilities is what this adapter does with the dialect it was built for, plus
// the one thing that is per-instance: whether it asks for reasoning.
func (p *OpenAIProvider) Capabilities() Capabilities {
	caps := p.profile.capabilities()
	caps.Reasoning = p.reasoning
	return caps
}

// Capabilities is what this adapter does with the codex backend's Responses API.
//
// DefaultMaxOutputTokens is zero and cannot be anything else: this dialect never
// sends a ceiling, so there is none to declare. Reasoning is per-instance, like
// the OpenAI adapter's, because asking for a reasoning summary is the option that
// decides whether Reasoning* events can ever arrive.
func (p *responsesProvider) Capabilities() Capabilities {
	return Capabilities{
		Streaming: true,
		Tools:     true,
		Reasoning: p.summary != "",
		// prompt_cache_key carries Request.SessionKey.
		PromptCaching:  KeyedPromptCaching,
		RetryTelemetry: true,
		ContextWindows: p.profile.contextWindows,
	}
}

// DescribeAnthropic is what NewAnthropicProvider builds, without building one.
func DescribeAnthropic() Capabilities { return anthropicCapabilities }

// DescribePosthog is what NewAnthropicOAuthProvider builds, without building
// one — the same wire format serving the gateway's catalog.
func DescribePosthog(models ...string) Capabilities { return posthogCapabilitiesFor(models...) }

// DescribeCodex is what a codex-dialect adapter built with opts would declare,
// without building one — which the catalog needs, since it has to describe every
// configured provider and builds only the selected one. It resolves the options
// through the same code the constructor runs, so the description cannot drift
// away from the provider.
func DescribeCodex(opts ...CodexOption) Capabilities {
	p := &responsesProvider{profile: codexResponsesProfile}
	for _, opt := range opts {
		opt(p)
	}
	return p.Capabilities()
}

// DescribeOpenAI is what an OpenAI-dialect adapter built with opts would declare,
// without building one. A registry needs this: it has to describe every provider
// the user configured, and all but the selected one are never constructed.
//
// It resolves the same options through the same code the constructor runs, so a
// format's description cannot drift from the provider that format builds — which
// is the only reason it is safe to answer for something that does not exist yet.
func DescribeOpenAI(opts ...Option) Capabilities {
	p := &OpenAIProvider{}
	for _, opt := range opts {
		opt(p)
	}
	return p.Capabilities()
}

// ActiveCapabilities resolves the capabilities of the adapter that would actually
// serve a turn, and whether it declared any.
//
// It exists because SwitchableProvider is a handle, not an adapter, and
// deliberately does not implement Describing: answering on behalf of a delegate
// that declared nothing would turn "said nothing" into "declared the zero value",
// which is the flattening the contract warns about. Acquire is the unwrap seam
// that was already there, and it answers for a plain provider too.
func ActiveCapabilities(p Provider) (Capabilities, bool) {
	return contract.CapabilitiesOf(Acquire(p).Provider)
}

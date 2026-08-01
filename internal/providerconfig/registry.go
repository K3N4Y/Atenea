package providerconfig

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/K3N4Y/atenea/internal/llm"
)

// The wire formats built into atenea. A provider's Type names one of them and
// the registry resolves it to the adapter that speaks it, so the type is the
// only thing that decides how a request is shaped — never the provider's id.
const (
	Anthropic  = "anthropic"
	OpenAI     = "openai"
	OpenRouter = "openrouter"
	// OpenAICompatible is the neutral dialect: chat completions with no vendor
	// extension. It is what a local endpoint (LM Studio, Ollama, vLLM) and any
	// unrecognized gateway speak.
	OpenAICompatible = "openai-compatible"
	// OpenAICodex is the dialect a ChatGPT subscription speaks: the codex backend's
	// Responses API, authenticated by an OAuth login rather than by a key. It is a
	// separate format and not an option on OpenAI because the request shape differs
	// — the system prompt is a field, no output ceiling may be sent, and the account
	// travels as a header.
	OpenAICodex = "openai-codex"
	// Posthog is PostHog's LLM gateway: the anthropic wire format, authenticated
	// by an OAuth login (browser redirect, not device code) whose bearer the
	// gateway routes on. It is a separate format and not an option on Anthropic
	// because the credential is a login and the model catalog is the gateway's
	// own, plan-gated per account.
	Posthog = "posthog"
)

// BuildParams is everything a factory needs to construct one live provider.
//
// It is a struct rather than an argument list because the credential stopped being
// one thing. An api_key resolves to a string that can be handed over once; an
// OAuth login resolves to a bearer plus an account id that both expire within the
// hour, so what a factory needs is not the credential but the way to ask for one.
// A struct lets a format take whichever of the two it authenticates with, and lets
// the next kind arrive as a field instead of as a signature change in every
// factory that does not care about it.
type BuildParams struct {
	// Provider is the declared endpoint: its base URL, its dialect options, its
	// issuer.
	Provider Provider
	// Model is the model the provider is being built on.
	Model string
	// APIKey is the resolved static credential, or the keyless placeholder when the
	// endpoint needs none. It is empty for a format that authenticates some other
	// way.
	APIKey string
	// Tokens resolves an OAuth credential per request, for the formats whose auth
	// is a login. nil for every other format — and for a format that needs it, nil
	// is a wiring error the factory must refuse rather than paper over.
	Tokens llm.OAuthTokenSource
}

// Factory builds the live provider for one wire format.
type Factory func(BuildParams) (llm.Provider, error)

// Describer answers what a provider built from def would declare about itself,
// without building it. Description is a separate function from construction
// because the catalog has to answer for every provider the user configured, and
// all but the selected one are never built — an answer that costs an SDK client,
// a credential lookup or a socket is not one the model picker can ask for.
type Describer func(def Provider) llm.Capabilities

// OAuthFlow is how a format whose credential is a login rather than a pasted
// string obtains one and keeps it working. A format without a flow authenticates
// with [BuildParams.APIKey]; a format with one gets [BuildParams.Tokens] instead,
// and its providers are connected by logging in rather than by typing a key.
//
// It lives on the format because that is where a wire format's facts belong. The
// alternative — the credential wiring switching on type names — is the coupling
// the registry exists to remove, and it would silently hand a third party's OAuth
// format OpenAI's refresh protocol.
type OAuthFlow struct {
	// Refresh renews the stored credential of one provider of this format.
	Refresh func(def Provider) OAuthRefresher
	// Login starts a device-code login for one provider of this format.
	Login DeviceLoginFunc
}

// Format is what this build knows about one wire format.
type Format struct {
	// Build constructs the live provider. A format with no Build cannot be
	// selected: Build reports it as an unsupported type.
	Build Factory
	// Describe is what that provider would declare. nil means the format declares
	// nothing — which is what a third-party factory registered without one says,
	// and a host reads it as silence rather than as a "no".
	Describe Describer
	// OAuth is set when this format's credential is a login. nil is the common
	// case: an API key, an exec command, or no credential at all.
	OAuth *OAuthFlow
	// Discover lists what one provider of this format serves, when the generic
	// OpenAI-compatible lister cannot: a different path, a different response
	// shape, results that need filtering. bearer is whatever credential the
	// catalog resolved — an OAuth access token for a login format, an API key
	// otherwise. nil means the format has nothing special to say and discovery
	// follows the provider's disable_model_discovery flag down the generic path.
	Discover func(ctx context.Context, def Provider, bearer string) ([]string, error)
}

// Registry maps a declared wire format to what this build knows about it. It is
// a plain map so extending it is an assignment rather than a code change here:
//
//	registry := providerconfig.DefaultRegistry()
//	registry["bedrock"] = providerconfig.Format{Build: newBedrockProvider}
//	providerconfig.Open(..., registry, ...)
type Registry map[string]Format

// DefaultRegistry returns the wire formats this build speaks. Each call returns
// a fresh map, so extending one copy never reaches another.
func DefaultRegistry() Registry {
	return Registry{
		Anthropic: Format{
			Build: func(params BuildParams) (llm.Provider, error) {
				return llm.NewAnthropicProvider(params.APIKey, params.Provider.BaseURL, params.Model), nil
			},
			Describe: func(Provider) llm.Capabilities { return llm.DescribeAnthropic() },
		},
		OpenAI:           openAIDialect(llm.WithOpenAICompatibility()),
		OpenAICompatible: openAIDialect(llm.WithoutOpenRouterReasoning()),
		OpenRouter: Format{
			Build: func(params BuildParams) (llm.Provider, error) {
				return llm.NewOpenAIProvider(params.APIKey, params.Provider.BaseURL, params.Model, openRouterOptions(params.Provider)...), nil
			},
			Describe: func(def Provider) llm.Capabilities {
				return llm.DescribeOpenAI(openRouterOptions(def)...)
			},
		},
		OpenAICodex: Format{
			Build: func(params BuildParams) (llm.Provider, error) {
				// A codex adapter with no token source could only ever produce 401s.
				// Refusing here names the wiring; letting it build names the endpoint.
				if params.Tokens == nil {
					return nil, fmt.Errorf("provider %q authenticates with a ChatGPT login and no credential source is wired for it", params.Provider.ID)
				}
				return llm.NewCodexProvider(params.Tokens, params.Provider.BaseURL, params.Model, codexOptions()...), nil
			},
			Describe: func(Provider) llm.Capabilities { return llm.DescribeCodex(codexOptions()...) },
			OAuth:    openAIOAuthFlow(),
		},
		Posthog: Format{
			Build: func(params BuildParams) (llm.Provider, error) {
				// Same reasoning as codex: an adapter with no token source could only
				// ever produce 401s, and refusing here names the wiring.
				if params.Tokens == nil {
					return nil, fmt.Errorf("provider %q authenticates with a PostHog login and no credential source is wired for it", params.Provider.ID)
				}
				return llm.NewAnthropicOAuthProvider(params.Tokens, params.Provider.BaseURL, params.Model), nil
			},
			Describe: func(Provider) llm.Capabilities { return llm.DescribePosthog() },
			OAuth:    posthogOAuthFlow(),
			Discover: posthogDiscover,
		},
	}
}

// openAIDialect is a format whose shape does not depend on the provider def.
// Build and Describe close over the same options, which is what keeps a format's
// description from drifting away from the provider that format builds.
func openAIDialect(opts ...llm.Option) Format {
	return Format{
		Build: func(params BuildParams) (llm.Provider, error) {
			return llm.NewOpenAIProvider(params.APIKey, params.Provider.BaseURL, params.Model, opts...), nil
		},
		Describe: func(Provider) llm.Capabilities { return llm.DescribeOpenAI(opts...) },
	}
}

// codexOptions are what the codex dialect asks its models for. A reasoning
// summary is requested because these are reasoning models and a turn without one
// shows the user a silent minute; the raw chain of thought is never available in
// the clear, so the summary is all there is to show.
func codexOptions() []llm.CodexOption {
	return []llm.CodexOption{llm.WithCodexReasoning(llm.CodexEffortMedium, llm.CodexSummaryAuto)}
}

// openRouterOptions are the options for one OpenRouter provider: its routing
// dialect always, and its reasoning extension unless the def opts out. It is the
// one format whose capabilities depend on the def, so both halves read it here.
func openRouterOptions(def Provider) []llm.Option {
	opts := []llm.Option{llm.WithOpenRouterCompatibility()}
	if !def.OpenRouterReasoning {
		opts = append(opts, llm.WithoutOpenRouterReasoning())
	}
	return opts
}

// Build resolves def.Type and constructs the provider. An unknown type names
// what is registered: the point of a registry is that the answer is data, so
// the error has to say which data this build was given.
func (r Registry) Build(params BuildParams) (llm.Provider, error) {
	format, ok := r[params.Provider.Type]
	if !ok || format.Build == nil {
		return nil, fmt.Errorf("provider %q has unsupported type %q (known types: %s)", params.Provider.ID, params.Provider.Type, strings.Join(r.Types(), ", "))
	}
	return format.Build(params)
}

// Describe is what a provider of def's format declares about itself, and whether
// this build can say. A type it does not know is not an error here — the catalog
// still lists the provider, it just has nothing to add about its models.
func (r Registry) Describe(def Provider) (llm.Capabilities, bool) {
	format, ok := r[def.Type]
	if !ok || format.Describe == nil {
		return llm.Capabilities{}, false
	}
	return format.Describe(def), true
}

// OAuth is the login flow of a wire format, and whether it has one. Everything
// that has to tell a login-authenticated provider from a key-authenticated one —
// which credential seam to wire, which affordance a UI shows, which /connect
// branch to take — asks here rather than comparing type names.
func (r Registry) OAuth(providerType string) (*OAuthFlow, bool) {
	format, ok := r[providerType]
	if !ok || format.OAuth == nil {
		return nil, false
	}
	return format.OAuth, true
}

// Speaks reports whether this build can construct a provider of this wire
// format. Loading a config does not ask — an entry this build cannot speak stays
// in the file for the build that can — but accepting a new one from a user does,
// because an entry nobody can select is not worth writing.
func (r Registry) Speaks(providerType string) bool {
	format, ok := r[providerType]
	return ok && format.Build != nil
}

// Types lists the wire formats this registry can build, sorted so the error
// above is stable. A format registered with a description and no factory is left
// out on purpose: it would otherwise be named as a known type by the very error
// that says it cannot be built.
func (r Registry) Types() []string {
	types := make([]string, 0, len(r))
	for name, format := range r {
		if format.Build == nil {
			continue
		}
		types = append(types, name)
	}
	sort.Strings(types)
	return types
}

package providerconfig

import (
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
)

// Factory builds the live provider for one wire format.
type Factory func(def Provider, model, apiKey string) (llm.Provider, error)

// Describer answers what a provider built from def would declare about itself,
// without building it. Description is a separate function from construction
// because the catalog has to answer for every provider the user configured, and
// all but the selected one are never built — an answer that costs an SDK client,
// a credential lookup or a socket is not one the model picker can ask for.
type Describer func(def Provider) llm.Capabilities

// Format is what this build knows about one wire format.
type Format struct {
	// Build constructs the live provider. A format with no Build cannot be
	// selected: Build reports it as an unsupported type.
	Build Factory
	// Describe is what that provider would declare. nil means the format declares
	// nothing — which is what a third-party factory registered without one says,
	// and a host reads it as silence rather than as a "no".
	Describe Describer
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
			Build: func(def Provider, model, apiKey string) (llm.Provider, error) {
				return llm.NewAnthropicProvider(apiKey, def.BaseURL, model), nil
			},
			Describe: func(Provider) llm.Capabilities { return llm.DescribeAnthropic() },
		},
		OpenAI:           openAIDialect(llm.WithOpenAICompatibility()),
		OpenAICompatible: openAIDialect(llm.WithoutOpenRouterReasoning()),
		OpenRouter: Format{
			Build: func(def Provider, model, apiKey string) (llm.Provider, error) {
				return llm.NewOpenAIProvider(apiKey, def.BaseURL, model, openRouterOptions(def)...), nil
			},
			Describe: func(def Provider) llm.Capabilities {
				return llm.DescribeOpenAI(openRouterOptions(def)...)
			},
		},
	}
}

// openAIDialect is a format whose shape does not depend on the provider def.
// Build and Describe close over the same options, which is what keeps a format's
// description from drifting away from the provider that format builds.
func openAIDialect(opts ...llm.Option) Format {
	return Format{
		Build: func(def Provider, model, apiKey string) (llm.Provider, error) {
			return llm.NewOpenAIProvider(apiKey, def.BaseURL, model, opts...), nil
		},
		Describe: func(Provider) llm.Capabilities { return llm.DescribeOpenAI(opts...) },
	}
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
func (r Registry) Build(def Provider, model, apiKey string) (llm.Provider, error) {
	format, ok := r[def.Type]
	if !ok || format.Build == nil {
		return nil, fmt.Errorf("provider %q has unsupported type %q (known types: %s)", def.ID, def.Type, strings.Join(r.Types(), ", "))
	}
	return format.Build(def, model, apiKey)
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

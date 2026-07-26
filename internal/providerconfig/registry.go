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

// Registry maps a declared wire format to the factory that speaks it. It is a
// plain map so extending it is an assignment rather than a code change here:
//
//	registry := providerconfig.DefaultRegistry()
//	registry["bedrock"] = newBedrockProvider
//	providerconfig.Open(..., registry.Build, ...)
type Registry map[string]Factory

// DefaultRegistry returns the wire formats this build speaks. Each call returns
// a fresh map, so extending one copy never reaches another.
func DefaultRegistry() Registry {
	return Registry{
		Anthropic: func(def Provider, model, apiKey string) (llm.Provider, error) {
			return llm.NewAnthropicProvider(apiKey, def.BaseURL, model), nil
		},
		OpenAI: func(def Provider, model, apiKey string) (llm.Provider, error) {
			return llm.NewOpenAIProvider(apiKey, def.BaseURL, model, llm.WithOpenAICompatibility()), nil
		},
		OpenRouter: func(def Provider, model, apiKey string) (llm.Provider, error) {
			opts := []llm.Option{llm.WithOpenRouterCompatibility()}
			if !def.OpenRouterReasoning {
				opts = append(opts, llm.WithoutOpenRouterReasoning())
			}
			return llm.NewOpenAIProvider(apiKey, def.BaseURL, model, opts...), nil
		},
		OpenAICompatible: func(def Provider, model, apiKey string) (llm.Provider, error) {
			return llm.NewOpenAIProvider(apiKey, def.BaseURL, model, llm.WithoutOpenRouterReasoning()), nil
		},
	}
}

// Build resolves def.Type and constructs the provider. An unknown type names
// what is registered: the point of a registry is that the answer is data, so
// the error has to say which data this build was given.
func (r Registry) Build(def Provider, model, apiKey string) (llm.Provider, error) {
	factory, ok := r[def.Type]
	if !ok {
		return nil, fmt.Errorf("provider %q has unsupported type %q (known types: %s)", def.ID, def.Type, strings.Join(r.Types(), ", "))
	}
	return factory(def, model, apiKey)
}

// Types lists the registered wire formats, sorted so the error above is stable.
func (r Registry) Types() []string {
	types := make([]string, 0, len(r))
	for name := range r {
		types = append(types, name)
	}
	sort.Strings(types)
	return types
}

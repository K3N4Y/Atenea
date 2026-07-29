package providerconfig

import (
	"os"
	"strings"

	"github.com/K3N4Y/atenea/internal/llm"
)

// EnvironmentFallback is the provider a bare environment can speak: the first
// provider in cfg whose API-key variable is set, on the model its ModelEnv names
// or its first curated model. It is what the host chats with before any /model or
// /connect selection exists, which makes the catalog's order its precedence
// order — the provider listed first in the picker is the one an unconfigured
// environment lands on.
//
// Only the environment is read, never a stored credential: a stored credential
// arrives with the providers.json that /connect wrote, and that file carries its
// own selection. This is the answer for when there is no file at all.
//
// A provider whose declared type this build cannot construct is passed over
// rather than reported. The key is still a fact, it just names a wire format this
// binary does not speak, and the next key is a better answer than a dead
// provider. false means no usable key anywhere, and the host supplies its own
// offline provider.
func EnvironmentFallback(cfg Config, getenv func(string) string, registry Registry) (llm.ProviderSnapshot, bool) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if registry == nil {
		registry = DefaultRegistry()
	}
	for _, provider := range cfg.Providers {
		if provider.APIKeyEnv == "" {
			continue
		}
		apiKey := strings.TrimSpace(getenv(provider.APIKeyEnv))
		if apiKey == "" {
			continue
		}
		model := environmentModel(provider, getenv)
		if model == "" {
			continue
		}
		// Only the environment is read here, so a provider whose credential is a
		// login has no key to be selected by and never reaches this.
		delegate, err := registry.Build(BuildParams{Provider: provider, Model: model, APIKey: apiKey})
		if err != nil {
			continue
		}
		return snapshot(provider, model, delegate), true
	}
	return llm.ProviderSnapshot{}, false
}

// environmentModel is the model an environment-selected provider starts on: the
// override its ModelEnv names, or its first curated model.
func environmentModel(provider Provider, getenv func(string) string) string {
	if provider.ModelEnv != "" {
		if model := strings.TrimSpace(getenv(provider.ModelEnv)); model != "" {
			return model
		}
	}
	if len(provider.Models) > 0 {
		return provider.Models[0]
	}
	return ""
}

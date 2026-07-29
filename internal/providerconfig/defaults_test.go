package providerconfig

import (
	"strings"
	"testing"
)

// The embedded catalog is decoded through the same door as a user's file, so a
// stray field or a missing id fails here instead of panicking on first launch.
func TestDefaultCatalog_DeclaresTheCuratedProviders(t *testing.T) {
	cfg := DefaultCatalog()

	wantIDs := []string{"anthropic", "openrouter", "openai", "openai-codex", "opencode", "opencode-go"}
	gotIDs := make([]string, 0, len(cfg.Providers))
	for _, provider := range cfg.Providers {
		gotIDs = append(gotIDs, provider.ID)
	}
	if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("catalog providers = %v, want %v in that order (order is precedence)", gotIDs, wantIDs)
	}
	if cfg.Selected.Provider != "" || cfg.Selected.Model != "" {
		t.Fatalf("catalog selection = %+v, want none: the default catalog offers providers, it does not pick one", cfg.Selected)
	}

	wantModels := map[string][]string{
		"anthropic":    {"claude-opus-4-8", "claude-fable-5", "claude-sonnet-5", "claude-haiku-4-5"},
		"openai":       {"gpt-5.6", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.4-mini", "gpt-5.4-nano", "gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano", "gpt-4o", "gpt-4o-mini"},
		"openai-codex": {"gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex-spark"},
	}
	wantBaseURLs := map[string]string{
		"anthropic":    "https://api.anthropic.com",
		"openrouter":   "https://openrouter.ai/api/v1",
		"openai":       "https://api.openai.com/v1",
		"openai-codex": "https://chatgpt.com/backend-api/codex",
		"opencode":     "https://opencode.ai/zen/v1",
		"opencode-go":  "https://opencode.ai/zen/go/v1",
	}
	for _, provider := range cfg.Providers {
		if got := provider.BaseURL; got != wantBaseURLs[provider.ID] {
			t.Errorf("%s base_url = %q, want %q", provider.ID, got, wantBaseURLs[provider.ID])
		}
		if provider.Name == "" {
			t.Errorf("%s = %#v, want a display name", provider.ID, provider)
		}
		// Every provider names the variable that overrides its key, except the one
		// whose credential is a login: there is no variable that could hold one, and
		// declaring one would make an unconnected subscription look configured.
		if _, login := DefaultRegistry().OAuth(provider.Type); login == (provider.APIKeyEnv != "") {
			t.Errorf("%s = %#v, want an api_key_env exactly when its credential is not a login", provider.ID, provider)
		}
		// The first model is the default /connect activates when nothing is
		// selected yet: a provider without one connects to nothing.
		if len(provider.Models) == 0 {
			t.Errorf("%s declares no models, so /connect would have no default to activate", provider.ID)
		}
		if want, ok := wantModels[provider.ID]; ok && strings.Join(provider.Models, ",") != strings.Join(want, ",") {
			t.Errorf("%s models = %#v, want %#v", provider.ID, provider.Models, want)
		}
		// OpenRouter is the only endpoint whose GET /models the picker can trust:
		// the others publish models the agent loop cannot drive.
		if wantDiscovery := provider.ID == "openrouter"; provider.DisableModelDiscovery == wantDiscovery {
			t.Errorf("%s disable_model_discovery = %v; only OpenRouter should discover models remotely", provider.ID, provider.DisableModelDiscovery)
		}
	}
}

// TestDefaultCatalog_ReturnsIndependentCopies: callers normalize and merge into
// what they are handed, so a shared backing array would let one host's config
// rewrite another's — the failure mode DefaultRegistry avoids the same way.
func TestDefaultCatalog_ReturnsIndependentCopies(t *testing.T) {
	first := DefaultCatalog()
	first.Providers[0].BaseURL = "http://mutated"
	first.Providers[0].Models[0] = "mutated"

	second := DefaultCatalog()
	if second.Providers[0].BaseURL == "http://mutated" || second.Providers[0].Models[0] == "mutated" {
		t.Fatalf("a second DefaultCatalog() saw the first one's mutations: %#v", second.Providers[0])
	}
}

// buildParamsFor is what a host hands the registry for one provider of the shipped
// catalog: a resolved key for a format that authenticates with one, and a
// per-request credential source for a format whose credential is a login.
func buildParamsFor(provider Provider, model string) BuildParams {
	params := BuildParams{Provider: provider, Model: model, APIKey: "test-key"}
	if _, ok := DefaultRegistry().OAuth(provider.Type); ok {
		params.Tokens = NewCredentialResolver(nil).OAuthTokenSource(provider.ID, nil)
	}
	return params
}

// TestDefaultCatalog_DeclaresBuildableWireFormats: an unregistered type is no
// longer a config error, so a typo here would ship silently and only surface when
// a user selects that provider.
func TestDefaultCatalog_DeclaresBuildableWireFormats(t *testing.T) {
	registry := DefaultRegistry()
	want := map[string]string{
		"anthropic":    Anthropic,
		"openrouter":   OpenRouter,
		"openai":       OpenAI,
		"openai-codex": OpenAICodex,
		"opencode":     OpenAICompatible,
		"opencode-go":  OpenAICompatible,
	}
	for _, provider := range DefaultCatalog().Providers {
		if got := provider.Type; got != want[provider.ID] {
			t.Errorf("provider %q type = %q, want %q", provider.ID, got, want[provider.ID])
		}
		if _, err := registry.Build(buildParamsFor(provider, provider.Models[0])); err != nil {
			t.Errorf("provider %q declares type %q, which the default registry cannot build: %v", provider.ID, provider.Type, err)
		}
		if _, ok := registry.Describe(provider); !ok {
			t.Errorf("provider %q declares type %q, which the default registry cannot describe: every context label it shows would be an em dash", provider.ID, provider.Type)
		}
	}
}

// TestDefaultCatalog_CuratedModelsKeepTheirContextWindows: the models the picker
// offers out of the box are the ones a user meets first, and a window that
// silently stops being declared costs both the label and preventive compaction.
func TestDefaultCatalog_CuratedModelsKeepTheirContextWindows(t *testing.T) {
	registry := DefaultRegistry()
	want := map[string]map[string]int{
		"anthropic": {"claude-opus-4-8": 200_000, "claude-fable-5": 200_000, "claude-sonnet-5": 200_000, "claude-haiku-4-5": 200_000},
		"openai":    {"gpt-5.6-terra": 1_050_000, "gpt-4o": 128_000},
		"openrouter": {
			"tencent/hy3:free":            262_144,
			"poolside/laguna-xs-2.1:free": 262_144,
			"cohere/north-mini-code:free": 256_000,
		},
	}
	for _, provider := range DefaultCatalog().Providers {
		capabilities, _ := registry.Describe(provider)
		for model, wantWindow := range want[provider.ID] {
			if got, ok := capabilities.ContextWindow(model); !ok || got != wantWindow {
				t.Errorf("%s/%s window = (%d, %v), want (%d, true)", provider.ID, model, got, ok, wantWindow)
			}
		}
	}
}

// TestDefaultCatalog_CoversEveryConnectableProvider: Connectable() filters the
// configured providers through the allowlist, so an allowlisted id the catalog
// does not ship is not an error — it is a row missing from the /connect picker.
func TestDefaultCatalog_CoversEveryConnectableProvider(t *testing.T) {
	declared := map[string]struct{}{}
	for _, provider := range DefaultCatalog().Providers {
		declared[provider.ID] = struct{}{}
	}
	for id := range connectableProviderIDs {
		if _, ok := declared[id]; !ok {
			t.Errorf("/connect offers %q but the default catalog does not declare it: its row would silently be absent", id)
		}
	}
}

// TestDefaultFallback_PrefersTheEnvironmentAndFallsBackToOffline: both hosts
// resolve their startup provider through here, so the two answers that matter are
// "the environment named one" and "it did not, here is yours back".
func TestDefaultFallback_PrefersTheEnvironmentAndFallsBackToOffline(t *testing.T) {
	// The catalog's own variables, cleared from the developer's shell so the result
	// is a property of this test rather than of the machine running it. Derived from
	// the catalog, which covers the next provider added to it too.
	for _, provider := range DefaultCatalog().Providers {
		if provider.APIKeyEnv != "" {
			t.Setenv(provider.APIKeyEnv, "")
		}
		if provider.ModelEnv != "" {
			t.Setenv(provider.ModelEnv, "")
		}
	}
	offline := fallbackSnapshot()

	got, fromEnvironment := DefaultFallback(offline)
	if fromEnvironment || got.ProviderID != offline.ProviderID {
		t.Fatalf("fallback = %#v (fromEnvironment=%v), want the offline provider unchanged", got, fromEnvironment)
	}

	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")
	got, fromEnvironment = DefaultFallback(offline)
	if !fromEnvironment || got.ProviderID != "anthropic" {
		t.Fatalf("fallback = %#v (fromEnvironment=%v), want the catalog's first keyed provider", got, fromEnvironment)
	}
}

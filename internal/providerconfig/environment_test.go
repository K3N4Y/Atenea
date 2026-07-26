package providerconfig

import "testing"

func envFrom(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

// Catalog order is precedence order: with two keys present the provider the
// picker lists first is the one an unconfigured environment lands on.
func TestEnvironmentFallback_TakesTheFirstProviderWithAKey(t *testing.T) {
	catalog := DefaultCatalog()

	snapshot, ok := EnvironmentFallback(catalog, envFrom(map[string]string{
		"OPENROUTER_API_KEY": "or-key",
		"ANTHROPIC_API_KEY":  "anthropic-key",
	}), nil)
	if !ok || snapshot.ProviderID != "anthropic" {
		t.Fatalf("fallback = %#v (ok=%v), want the catalog's first keyed provider (anthropic)", snapshot, ok)
	}
	if snapshot.Model != catalog.Providers[0].Models[0] || snapshot.BaseURL != catalog.Providers[0].BaseURL {
		t.Fatalf("fallback = %#v, want %s on its first curated model", snapshot, catalog.Providers[0].BaseURL)
	}
	if snapshot.Provider == nil {
		t.Fatal("fallback carries no live provider: the switcher has nothing to chat with")
	}

	snapshot, ok = EnvironmentFallback(catalog, envFrom(map[string]string{"OPENROUTER_API_KEY": "or-key"}), nil)
	if !ok || snapshot.ProviderID != "openrouter" {
		t.Fatalf("fallback = %#v (ok=%v), want openrouter when it holds the only key", snapshot, ok)
	}
}

func TestEnvironmentFallback_ModelEnvOverridesTheCuratedDefault(t *testing.T) {
	snapshot, ok := EnvironmentFallback(DefaultCatalog(), envFrom(map[string]string{
		"ANTHROPIC_API_KEY": "anthropic-key",
		"ANTHROPIC_MODEL":   "  claude-pinned-snapshot  ",
	}), nil)
	if !ok || snapshot.Model != "claude-pinned-snapshot" {
		t.Fatalf("fallback = %#v (ok=%v), want the ANTHROPIC_MODEL override, trimmed", snapshot, ok)
	}
}

func TestEnvironmentFallback_ReportsNothingWithoutAKey(t *testing.T) {
	if snapshot, ok := EnvironmentFallback(DefaultCatalog(), envFrom(nil), nil); ok {
		t.Fatalf("fallback = %#v, want none: an empty environment has no provider to offer", snapshot)
	}
}

// A key naming a wire format this build does not register is passed over, not
// reported: the next key is a better answer than a provider that cannot be built.
func TestEnvironmentFallback_SkipsUnbuildableTypes(t *testing.T) {
	catalog := Config{Providers: []Provider{
		{ID: "gateway", Name: "Gateway", Type: "bedrock", BaseURL: "https://gateway.test", APIKeyEnv: "GATEWAY_KEY", Models: []string{"m"}},
		{ID: "local", Name: "Local", Type: OpenAICompatible, BaseURL: "http://127.0.0.1:1234/v1", APIKeyEnv: "LOCAL_KEY", Models: []string{"llama"}},
	}}
	env := envFrom(map[string]string{"GATEWAY_KEY": "gw-key", "LOCAL_KEY": "local-key"})

	snapshot, ok := EnvironmentFallback(catalog, env, nil)
	if !ok || snapshot.ProviderID != "local" {
		t.Fatalf("fallback = %#v (ok=%v), want the next buildable provider (local)", snapshot, ok)
	}

	// Registering the format makes the first provider the answer, without the
	// catalog changing at all.
	registry := DefaultRegistry()
	registry["bedrock"] = registry[OpenAICompatible]
	if snapshot, ok := EnvironmentFallback(catalog, env, registry); !ok || snapshot.ProviderID != "gateway" {
		t.Fatalf("fallback = %#v (ok=%v), want gateway once its type is registered", snapshot, ok)
	}
}

// A provider with no models and no override has no model to start on, so it is
// skipped rather than reported as active on the empty string.
func TestEnvironmentFallback_SkipsProvidersWithNoModel(t *testing.T) {
	catalog := Config{Providers: []Provider{
		{ID: "empty", Name: "Empty", Type: OpenAICompatible, BaseURL: "https://empty.test", APIKeyEnv: "EMPTY_KEY"},
		{ID: "local", Name: "Local", Type: OpenAICompatible, BaseURL: "http://127.0.0.1:1234/v1", APIKeyEnv: "LOCAL_KEY", Models: []string{"llama"}},
	}}
	snapshot, ok := EnvironmentFallback(catalog, envFrom(map[string]string{"EMPTY_KEY": "k", "LOCAL_KEY": "k"}), nil)
	if !ok || snapshot.ProviderID != "local" {
		t.Fatalf("fallback = %#v (ok=%v), want local: a provider with no model cannot be started", snapshot, ok)
	}
}

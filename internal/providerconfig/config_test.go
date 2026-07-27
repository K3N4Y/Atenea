package providerconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultPath_UsesUserConfigDir(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skipf("XDG_CONFIG_HOME is not the UserConfigDir override on %s", runtime.GOOS)
	}
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	want := filepath.Join(root, "atenea", "providers.json")
	if got := DefaultPath(); got != want {
		t.Fatalf("DefaultPath() = %q, want %q", got, want)
	}
}

func TestDefaultCachePath_UsesXDGCacheHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", root)

	want := filepath.Join(root, "atenea", "models-cache.json")
	if got := DefaultCachePath(); got != want {
		t.Fatalf("DefaultCachePath() = %q, want %q", got, want)
	}
}

func TestLoad_ParsesValidatedProviderSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	err := os.WriteFile(path, []byte(`{
      "providers":[{
        "id":"openrouter","name":"OpenRouter",
        "type":"openai-compatible",
        "base_url":"https://openrouter.ai/api/v1/",
        "api_key_env":"OPENROUTER_API_KEY",
        "openrouter_reasoning":true,
        "models":["openai/gpt-5","openai/gpt-5"]
      }],
      "selected":{"provider":"openrouter","model":"openai/gpt-5"}
    }`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Providers[0].BaseURL; got != "https://openrouter.ai/api/v1" {
		t.Fatalf("BaseURL = %q", got)
	}
	if got := len(cfg.Providers[0].Models); got != 1 {
		t.Fatalf("models = %d", got)
	}
}

func TestLoad_AcceptsNativeAnthropicProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	if err := os.WriteFile(path, []byte(`{"providers":[{"id":"anthropic","name":"Anthropic","type":"anthropic","base_url":"https://api.anthropic.com","api_key_env":"ANTHROPIC_API_KEY","disable_model_discovery":true,"models":["claude-sonnet-4-5-20250929"]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Providers[0].Type; got != Anthropic {
		t.Fatalf("Type = %q, want %q", got, Anthropic)
	}
}

// TestLoad_MigratesLegacyOpenAICompatibleDialects: a config written before the
// dialect became the type says "openai-compatible" for OpenAI and OpenRouter.
// Reading it literally would silently drop prompt_cache_key and the OpenRouter
// routing fields, so the id resolves the dialect once, here.
func TestLoad_MigratesLegacyOpenAICompatibleDialects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	legacy := `{"providers":[
	  {"id":"openai","name":"OpenAI","type":"openai-compatible","base_url":"https://api.openai.com/v1"},
	  {"id":"openrouter","name":"OpenRouter","type":"openai-compatible","base_url":"https://openrouter.ai/api/v1"},
	  {"id":"opencode","name":"OpenCode Zen","type":"openai-compatible","base_url":"https://opencode.ai/zen/v1"}
	]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"openai": OpenAI, "openrouter": OpenRouter, "opencode": OpenAICompatible}
	for _, provider := range cfg.Providers {
		if got := provider.Type; got != want[provider.ID] {
			t.Fatalf("provider %q type = %q, want %q", provider.ID, got, want[provider.ID])
		}
	}
}

// TestLoad_KeepsProviderWhoseTypeThisBuildCannotSpeak: the file is shared with
// builds that register other factories. Rejecting an unknown type here would
// discard every other provider in the file too.
func TestLoad_KeepsProviderWhoseTypeThisBuildCannotSpeak(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	body := `{"providers":[{"id":"b","name":"Bedrock","type":"bedrock","base_url":"http://b"},{"id":"local","name":"Local","type":"openai-compatible","base_url":"http://local"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers) != 2 || cfg.Providers[0].Type != "bedrock" {
		t.Fatalf("providers = %#v", cfg.Providers)
	}
}

func TestLoad_RejectsInvalidConfigurations(t *testing.T) {
	tests := map[string]string{
		"duplicate provider": `{"providers":[{"id":"x","name":"X","type":"openai-compatible","base_url":"http://one"},{"id":"x","name":"Y","type":"openai-compatible","base_url":"http://two"}]}`,
		"unknown selection":  `{"providers":[{"id":"x","name":"X","type":"openai-compatible","base_url":"http://one"}],"selected":{"provider":"missing","model":"m"}}`,
		"missing type":       `{"providers":[{"id":"x","name":"X","base_url":"http://one"}]}`,
		"secret value":       `{"providers":[{"id":"x","name":"X","type":"openai-compatible","base_url":"http://one","api_key":"secret"}]}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "providers.json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("Load succeeded")
			}
		})
	}
}

func TestSave_WritesAtomicallyWithoutSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "providers.json")
	cfg := Config{Providers: []Provider{{ID: "local", Name: "Local", Type: OpenAICompatible, BaseURL: "http://localhost:11434/v1", APIKeyEnv: "LOCAL_KEY", Models: []string{"qwen"}}}, Selected: Selection{Provider: "local", Model: "qwen"}}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secret") {
		t.Fatalf("secret persisted: %s", b)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v err=%v", info.Mode().Perm(), err)
	}
}

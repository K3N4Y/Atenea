package providerconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/K3N4Y/atenea/internal/paths"
)

type Provider struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	BaseURL   string `json:"base_url"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
	// ModelEnv names the variable that overrides which model this provider starts
	// on when it is picked from the environment (see EnvironmentFallback). It is
	// declared rather than derived from APIKeyEnv so a provider named by any
	// convention can offer the override.
	ModelEnv            string `json:"model_env,omitempty"`
	OpenRouterReasoning bool   `json:"openrouter_reasoning,omitempty"`
	// OAuthIssuer is the authorization server that issues this provider's
	// credential, for the wire formats whose auth is a login rather than a key. It
	// is declared beside BaseURL and for the same reason: which host a credential
	// comes from is a fact about the endpoint, and one compiled into an adapter
	// cannot be pointed anywhere else — not at a stub, not at an enterprise tenant.
	// Empty means the format's own default.
	OAuthIssuer string `json:"oauth_issuer,omitempty"`
	// LocalModels declares an endpoint whose models run on the user's own machine
	// (LM Studio, Ollama, vLLM). It is a fact about the endpoint, not a request:
	// a host that shapes anything around the difference — atenea picks the system
	// prompt that spells out the tool-calling protocol — reads it here instead of
	// guessing from a base URL or a model id. Local ids are arbitrary, so the
	// family routing that works for a vendor catalog has nothing to route on.
	LocalModels           bool     `json:"local_models,omitempty"`
	DisableModelDiscovery bool     `json:"disable_model_discovery,omitempty"`
	Models                []string `json:"models,omitempty"`
}

type Selection struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type Config struct {
	Providers []Provider `json:"providers"`
	Selected  Selection  `json:"selected,omitempty"`
}

func DefaultPath() string {
	path, err := paths.Providers()
	if err != nil {
		return filepath.Join(".", "atenea", "providers.json")
	}
	return path
}

func DefaultCachePath() string {
	path, err := paths.ModelsCache()
	if err != nil {
		return filepath.Join(".", "atenea", "models-cache.json")
	}
	return path
}

func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	return decodeConfig(f)
}

// decodeConfig reads one provider config whatever its source: the user's file and
// the embedded default catalog go through the same door, so a stray field or a
// missing id is caught by the same rules in both.
func decodeConfig(r io.Reader) (Config, error) {
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode provider config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return Config{}, errors.New("decode provider config: multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("decode provider config: %w", err)
	}
	if err := normalizeAndValidate(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	if err := normalizeAndValidate(&cfg); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode provider config: %w", err)
	}
	b = append(b, '\n')
	if err := writeFileAtomic(path, b); err != nil {
		return fmt.Errorf("save provider config: %w", err)
	}
	return nil
}

func normalizeAndValidate(cfg *Config) error {
	seen := make(map[string]struct{}, len(cfg.Providers))
	for i := range cfg.Providers {
		provider := &cfg.Providers[i]
		provider.ID = strings.TrimSpace(provider.ID)
		provider.Name = strings.TrimSpace(provider.Name)
		provider.Type = strings.TrimSpace(provider.Type)
		provider.BaseURL = strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
		provider.APIKeyEnv = strings.TrimSpace(provider.APIKeyEnv)
		provider.ModelEnv = strings.TrimSpace(provider.ModelEnv)
		provider.OAuthIssuer = strings.TrimRight(strings.TrimSpace(provider.OAuthIssuer), "/")
		if provider.ID == "" || provider.Name == "" || provider.Type == "" || provider.BaseURL == "" {
			return fmt.Errorf("provider %d requires id, name, type, and base_url", i)
		}
		// A type this build cannot speak is not a config error: the file is
		// shared with builds that register other factories, and rejecting it
		// here would drop every other provider in it too. The registry answers
		// when the provider is actually built.
		provider.Type = migrateLegacyDialect(provider.ID, provider.Type)
		if _, ok := seen[provider.ID]; ok {
			return fmt.Errorf("duplicate provider id %q", provider.ID)
		}
		seen[provider.ID] = struct{}{}
		models := make([]string, 0, len(provider.Models))
		modelSeen := map[string]struct{}{}
		for _, model := range provider.Models {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			if _, ok := modelSeen[model]; ok {
				continue
			}
			modelSeen[model] = struct{}{}
			models = append(models, model)
		}
		provider.Models = models
	}
	cfg.Selected.Provider = strings.TrimSpace(cfg.Selected.Provider)
	cfg.Selected.Model = strings.TrimSpace(cfg.Selected.Model)
	if (cfg.Selected.Provider == "") != (cfg.Selected.Model == "") {
		return errors.New("selected provider and model must both be set")
	}
	if cfg.Selected.Provider != "" {
		if _, ok := seen[cfg.Selected.Provider]; !ok {
			return fmt.Errorf("selected provider %q is not configured", cfg.Selected.Provider)
		}
	}
	return nil
}

// validateBaseURL rejects a base URL no adapter could reach. It runs when a user
// declares a provider (see Service.Declare), not while loading the config: a
// file is shared between hosts and one bad entry must not take the other
// providers down with it, but someone typing an endpoint right now can be told.
func validateBaseURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("base URL is required (e.g. http://localhost:1234/v1)")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("invalid base URL %q (expected http(s)://host:port/path)", raw)
	}
	return nil
}

// legacyDialects are the provider ids whose OpenAI dialect used to be inferred
// from the id inside the provider factory, back when every OpenAI-ish endpoint
// declared the same type. A config written by such a build says
// "openai-compatible" for both, and reading it literally would silently drop
// OpenAI's prompt_cache_key and OpenRouter's routing and reasoning fields.
var legacyDialects = map[string]string{
	"openai":     OpenAI,
	"openrouter": OpenRouter,
}

// migrateLegacyDialect reproduces that id switch exactly once, at the config
// boundary, so nothing downstream ever looks at an id again. It is bounded and
// dated: the map only knows the two ids the default catalog ever shipped, and
// the first model selection rewrites the file with the resolved type.
func migrateLegacyDialect(id, providerType string) string {
	if providerType != OpenAICompatible {
		return providerType
	}
	if dialect, ok := legacyDialects[id]; ok {
		return dialect
	}
	return providerType
}

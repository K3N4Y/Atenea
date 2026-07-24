// Package wailsprovider owns the legacy Wails provider selector lifecycle.
//
// The desktop frontend still selects between OpenRouter and a keyless local
// OpenAI-compatible endpoint. Manager keeps that selection and its provider in
// one atomic snapshot so agent wiring never observes a provider/config mix.
package wailsprovider

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"

	"atenea/internal/llm"
	"atenea/internal/providerconfig"
)

const (
	OpenRouterBaseURL = "https://openrouter.ai/api/v1"
	DefaultModel      = "openrouter/free"

	KindOpenRouter = "openrouter"
	KindLocal      = "local"
	KindDemo       = "demo"

	localPlaceholderKey = "local"
)

// Config is deliberately secret-free because the frontend persists it.
type Config struct {
	Kind    string
	BaseURL string
	Model   string
}

// Snapshot is the complete provider state consumed by agent wiring.
type Snapshot struct {
	Provider llm.Provider
	Config   Config
	Local    bool
}

type Factory func(Config) llm.Provider
type ModelLister func(context.Context, string, string) ([]string, error)

// Manager atomically owns the active provider and its public configuration.
type Manager struct {
	mu          sync.RWMutex
	snapshot    Snapshot
	factory     Factory
	getenv      func(string) string
	credentials providerconfig.CredentialStore
	listModels  ModelLister
}

func New(provider llm.Provider, cfg Config, factory Factory, getenv func(string) string, credentials providerconfig.CredentialStore, listModels ModelLister) *Manager {
	if getenv == nil {
		getenv = os.Getenv
	}
	if listModels == nil {
		listModels = llm.ListModels
	}
	return &Manager{
		snapshot:    Snapshot{Provider: provider, Config: cfg, Local: cfg.Kind == KindLocal},
		factory:     factory,
		getenv:      getenv,
		credentials: credentials,
		listModels:  listModels,
	}
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.snapshot
}

// SetFactory replaces provider construction for subsequent selections. It is
// primarily an internal test seam; replacing it does not alter the active
// snapshot.
func (m *Manager) SetFactory(factory Factory) {
	m.mu.Lock()
	m.factory = factory
	m.mu.Unlock()
}

// Set validates and constructs before publishing, so errors leave the active
// snapshot untouched and readers never observe a partial transition.
func (m *Manager) Set(kind, baseURL, model string) (Snapshot, error) {
	cfg, err := Normalize(kind, baseURL, model)
	if err != nil {
		return m.Snapshot(), err
	}
	m.mu.RLock()
	factory := m.factory
	m.mu.RUnlock()
	provider := factory(cfg)
	next := Snapshot{Provider: provider, Config: cfg, Local: cfg.Kind == KindLocal}
	m.mu.Lock()
	m.snapshot = next
	m.mu.Unlock()
	return next, nil
}

func (m *Manager) ListModels(ctx context.Context, baseURL string) ([]string, error) {
	apiKey := ""
	if strings.TrimRight(baseURL, "/") == OpenRouterBaseURL {
		apiKey = OpenRouterAPIKey(m.getenv, m.credentials)
	}
	return m.listModels(ctx, baseURL, apiKey)
}

func ResolveModel(getenv func(string) string) string {
	if model := getenv("OPENROUTER_MODEL"); model != "" {
		return model
	}
	return DefaultModel
}

func InitialConfig(getenv func(string) string, credentials providerconfig.CredentialStore) Config {
	if OpenRouterAPIKey(getenv, credentials) == "" {
		return Config{Kind: KindDemo, Model: DefaultModel}
	}
	return Config{Kind: KindOpenRouter, BaseURL: OpenRouterBaseURL, Model: ResolveModel(getenv)}
}

func OpenRouterAPIKey(getenv func(string) string, credentials providerconfig.CredentialStore) string {
	if key := getenv("OPENROUTER_API_KEY"); key != "" {
		return key
	}
	if credentials != nil {
		if credential, ok := credentials.Get(KindOpenRouter); ok && credential.Type == providerconfig.CredentialTypeAPIKey {
			return credential.APIKey
		}
	}
	return ""
}

func Build(cfg Config, getenv func(string) string, credentials providerconfig.CredentialStore, demo llm.Provider) llm.Provider {
	switch cfg.Kind {
	case KindLocal:
		return llm.NewOpenAIProvider(localPlaceholderKey, cfg.BaseURL, cfg.Model, llm.WithoutOpenRouterReasoning())
	case KindOpenRouter:
		return llm.NewOpenAIProvider(OpenRouterAPIKey(getenv, credentials), cfg.BaseURL, cfg.Model, llm.WithOpenRouterCompatibility())
	default:
		return demo
	}
}

func Normalize(kind, baseURL, model string) (Config, error) {
	switch kind {
	case KindOpenRouter:
		if model == "" {
			model = DefaultModel
		}
		return Config{Kind: kind, BaseURL: OpenRouterBaseURL, Model: model}, nil
	case KindLocal:
		if err := validateBaseURL(baseURL); err != nil {
			return Config{}, err
		}
		if strings.TrimSpace(model) == "" {
			return Config{}, fmt.Errorf("provider local: falta el modelo")
		}
		return Config{Kind: kind, BaseURL: baseURL, Model: model}, nil
	default:
		return Config{}, fmt.Errorf("provider desconocido: %q (usa %q o %q)", kind, KindOpenRouter, KindLocal)
	}
}

func validateBaseURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("provider local: falta el baseURL (p.ej. http://localhost:1234/v1)")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("provider local: baseURL invalido %q (espera http(s)://host:puerto/v1)", raw)
	}
	return nil
}

package providerconfig

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"atenea/internal/llm"
)

type Active struct {
	ProviderID   string
	ProviderName string
	BaseURL      string
	Model        string
}

type ProviderFactory func(def Provider, model, apiKey string) (llm.Provider, error)
type SaveConfig func(path string, cfg Config) error

type Service struct {
	mu       sync.RWMutex
	path     string
	config   Config
	catalog  *Catalog
	switcher *llm.SwitchableProvider
	getenv   func(string) string
	factory  ProviderFactory
	save     SaveConfig
}

func Open(path, cachePath string, fallback llm.ProviderSnapshot, getenv func(string) string, factory ProviderFactory, save SaveConfig, list ModelLister, defaults ...Config) (*Service, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if factory == nil {
		factory = defaultProviderFactory
	}
	if save == nil {
		save = Save
	}
	switcher, err := llm.NewSwitchableProvider(fallback)
	if err != nil {
		return nil, err
	}
	s := &Service{path: path, switcher: switcher, getenv: getenv, factory: factory, save: save}
	cfg, loadErr := Load(path)
	if loadErr != nil {
		if errors.Is(loadErr, os.ErrNotExist) {
			if len(defaults) > 0 {
				cfg = defaults[0]
				if err := normalizeAndValidate(&cfg); err != nil {
					return s, fmt.Errorf("validate default provider config: %w", err)
				}
				s.config = cfg
				s.catalog = NewCatalog(cfg, cachePath, getenv, list)
			}
			return s, nil
		}
		return s, fmt.Errorf("load provider config: %w", loadErr)
	}
	if len(defaults) > 0 {
		defaultConfig := defaults[0]
		if err := normalizeAndValidate(&defaultConfig); err != nil {
			return s, fmt.Errorf("validate default provider config: %w", err)
		}
		cfg = mergeMissingProviders(cfg, defaultConfig)
	}
	s.config = cfg
	s.catalog = NewCatalog(cfg, cachePath, getenv, list)
	provider, ok := findProvider(cfg, cfg.Selected.Provider)
	if !ok || cfg.Selected.Model == "" {
		return s, errors.New("provider config has no active selection")
	}
	apiKey, err := requiredAPIKey(provider, getenv)
	if err != nil {
		return s, err
	}
	delegate, err := factory(provider, cfg.Selected.Model, apiKey)
	if err != nil {
		return s, err
	}
	s.switcher.Swap(snapshot(provider, cfg.Selected.Model, delegate))
	return s, nil
}

func mergeMissingProviders(cfg, defaults Config) Config {
	seen := make(map[string]struct{}, len(cfg.Providers))
	for _, provider := range cfg.Providers {
		seen[provider.ID] = struct{}{}
	}
	for _, provider := range defaults.Providers {
		if _, ok := seen[provider.ID]; ok {
			continue
		}
		cfg.Providers = append(cfg.Providers, provider)
	}
	return cfg
}

func (s *Service) Provider() *llm.SwitchableProvider { return s.switcher }
func (s *Service) Active() Active {
	snapshot := s.switcher.Acquire()
	return Active{ProviderID: snapshot.ProviderID, ProviderName: snapshot.ProviderName, BaseURL: snapshot.BaseURL, Model: snapshot.Model}
}
func (s *Service) Catalog() []ProviderModels {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.catalog == nil {
		return nil
	}
	return s.catalog.Snapshot()
}
func (s *Service) Refresh(ctx context.Context) ([]ProviderModels, error) {
	s.mu.RLock()
	catalog := s.catalog
	s.mu.RUnlock()
	if catalog == nil {
		return nil, nil
	}
	return catalog.Refresh(ctx)
}

func (s *Service) Select(_ context.Context, providerID, model string) (Active, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if model == "" {
		return s.Active(), errors.New("model is required")
	}
	provider, ok := findProvider(s.config, providerID)
	if !ok {
		return s.Active(), fmt.Errorf("provider %q is not configured", providerID)
	}
	apiKey, err := requiredAPIKey(provider, s.getenv)
	if err != nil {
		return s.Active(), err
	}
	delegate, err := s.factory(provider, model, apiKey)
	if err != nil {
		return s.Active(), err
	}
	next := s.config
	next.Providers = append([]Provider(nil), s.config.Providers...)
	next.Selected = Selection{Provider: providerID, Model: model}
	if err := s.save(s.path, next); err != nil {
		return s.Active(), err
	}
	s.switcher.Swap(snapshot(provider, model, delegate))
	s.config = next
	s.catalog = NewCatalog(next, s.catalogPath(), s.getenv, s.catalogLister())
	return s.Active(), nil
}
func (s *Service) catalogLister() ModelLister {
	if s.catalog == nil {
		return nil
	}
	return s.catalog.modelLister()
}

func (s *Service) catalogPath() string {
	if s.catalog == nil {
		return ""
	}
	return s.catalog.cachePath
}
func findProvider(cfg Config, id string) (Provider, bool) {
	for _, provider := range cfg.Providers {
		if provider.ID == id {
			return provider, true
		}
	}
	return Provider{}, false
}
func snapshot(provider Provider, model string, delegate llm.Provider) llm.ProviderSnapshot {
	return llm.ProviderSnapshot{ProviderID: provider.ID, ProviderName: provider.Name, BaseURL: provider.BaseURL, Model: model, Provider: delegate}
}
func requiredAPIKey(provider Provider, getenv func(string) string) (string, error) {
	if provider.APIKeyEnv == "" {
		return "atenea-keyless-provider", nil
	}
	value := getenv(provider.APIKeyEnv)
	if value == "" {
		return "", fmt.Errorf("required API key environment variable %s is not set", provider.APIKeyEnv)
	}
	return value, nil
}
func defaultProviderFactory(def Provider, model, apiKey string) (llm.Provider, error) {
	opts := []llm.Option{llm.WithoutOpenRouterReasoning()}
	if def.OpenRouterReasoning {
		opts = nil
	}
	return llm.NewOpenAIProvider(apiKey, def.BaseURL, model, opts...), nil
}

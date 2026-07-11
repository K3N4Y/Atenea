package providerconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"atenea/internal/llm"
)

type ProviderModels struct {
	ID     string
	Name   string
	Models []string
}

type CachedProvider struct {
	ID        string    `json:"id"`
	BaseURL   string    `json:"base_url"`
	Models    []string  `json:"models"`
	FetchedAt time.Time `json:"fetched_at"`
}

type Cache struct {
	Providers []CachedProvider `json:"providers"`
}
type ModelLister func(context.Context, string, string) ([]string, error)

type Catalog struct {
	mu        sync.RWMutex
	config    Config
	cachePath string
	cache     Cache
	cached    map[string][]string
	remote    map[string][]string
	getenv    func(string) string
	list      ModelLister
	refreshMu sync.Mutex
	inflight  *catalogRefresh
}

type catalogRefresh struct {
	done      chan struct{}
	providers []ProviderModels
	err       error
}

func NewCatalog(cfg Config, cachePath string, getenv func(string) string, list ModelLister) *Catalog {
	if getenv == nil {
		getenv = os.Getenv
	}
	if list == nil {
		list = llm.ListModels
	}
	c := &Catalog{config: cfg, cachePath: cachePath, cached: map[string][]string{}, remote: map[string][]string{}, getenv: getenv, list: list}
	if cachePath != "" {
		if data, err := os.ReadFile(cachePath); err == nil && json.Unmarshal(data, &c.cache) == nil {
			for _, entry := range c.cache.Providers {
				for _, provider := range cfg.Providers {
					if entry.ID == provider.ID && entry.BaseURL == provider.BaseURL {
						c.cached[entry.ID] = append([]string(nil), entry.Models...)
					}
				}
			}
		}
	}
	return c
}

func (c *Catalog) Snapshot() []ProviderModels {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]ProviderModels, 0, len(c.config.Providers))
	for _, provider := range c.config.Providers {
		seen := map[string]struct{}{}
		models := make([]string, 0)
		add := func(values ...string) {
			for _, model := range values {
				if model == "" {
					continue
				}
				if _, ok := seen[model]; ok {
					continue
				}
				seen[model] = struct{}{}
				models = append(models, model)
			}
		}
		if c.config.Selected.Provider == provider.ID {
			add(c.config.Selected.Model)
		}
		add(provider.Models...)
		remote := append([]string(nil), c.remote[provider.ID]...)
		sort.Strings(remote)
		add(remote...)
		add(c.cached[provider.ID]...)
		result = append(result, ProviderModels{ID: provider.ID, Name: provider.Name, Models: models})
	}
	return result
}

func (c *Catalog) Refresh(ctx context.Context) ([]ProviderModels, error) {
	c.refreshMu.Lock()
	if c.inflight != nil {
		refresh := c.inflight
		c.refreshMu.Unlock()
		select {
		case <-ctx.Done():
			return c.Snapshot(), ctx.Err()
		case <-refresh.done:
			return cloneCatalogProviders(refresh.providers), refresh.err
		}
	}
	refresh := &catalogRefresh{done: make(chan struct{})}
	c.inflight = refresh
	c.refreshMu.Unlock()

	providers, err := c.refresh(ctx)
	refresh.providers = cloneCatalogProviders(providers)
	refresh.err = err
	close(refresh.done)
	c.refreshMu.Lock()
	c.inflight = nil
	c.refreshMu.Unlock()
	return providers, err
}

func (c *Catalog) refresh(ctx context.Context) ([]ProviderModels, error) {
	var warnings []error
	now := time.Now()
	cache := Cache{}
	for _, provider := range c.config.Providers {
		models, err := c.list(ctx, provider.BaseURL, c.getenv(provider.APIKeyEnv))
		if err != nil {
			warnings = append(warnings, fmt.Errorf("refresh %s: %w", provider.ID, err))
			c.mu.RLock()
			cached := append([]string(nil), c.cached[provider.ID]...)
			c.mu.RUnlock()
			if len(cached) > 0 {
				cache.Providers = append(cache.Providers, CachedProvider{ID: provider.ID, BaseURL: provider.BaseURL, Models: cached, FetchedAt: cachedFetchedAt(c.cache, provider.ID, provider.BaseURL)})
			}
			continue
		}
		c.mu.Lock()
		c.remote[provider.ID] = append([]string(nil), models...)
		c.cached[provider.ID] = append([]string(nil), models...)
		c.mu.Unlock()
		cache.Providers = append(cache.Providers, CachedProvider{ID: provider.ID, BaseURL: provider.BaseURL, Models: models, FetchedAt: now})
	}
	if c.cachePath != "" && len(cache.Providers) > 0 {
		if err := saveCache(c.cachePath, cache); err != nil {
			warnings = append(warnings, err)
		}
	}
	return c.Snapshot(), errors.Join(warnings...)
}

func cachedFetchedAt(cache Cache, providerID, baseURL string) time.Time {
	for _, provider := range cache.Providers {
		if provider.ID == providerID && provider.BaseURL == baseURL {
			return provider.FetchedAt
		}
	}
	return time.Time{}
}

func cloneCatalogProviders(in []ProviderModels) []ProviderModels {
	out := make([]ProviderModels, len(in))
	for i, provider := range in {
		out[i] = provider
		out[i].Models = append([]string(nil), provider.Models...)
	}
	return out
}

func (c *Catalog) modelLister() ModelLister { return c.list }

func saveCache(path string, cache Cache) error {
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".models-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

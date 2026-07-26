package providerconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/K3N4Y/atenea/internal/llm"
)

type ProviderModels struct {
	ID     string
	Name   string
	Models []string
	// Capabilities is what the adapter for this provider's wire format declares,
	// resolved without building it — the model picker has to label every model it
	// offers, not only the one currently selected. Zero when this build cannot
	// speak the type, which reads as "nothing known", never as "nothing there".
	Capabilities llm.Capabilities
	// BuiltIn marks a provider that ships in atenea's catalog, as opposed to one
	// the user declared. Only a declared provider can be forgotten — removing a
	// built-in one would see it merged back at the next launch — so a host that
	// offers removal reads this rather than keeping its own list of names.
	BuiltIn bool
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
	mu          sync.RWMutex
	config      Config
	cachePath   string
	cache       Cache
	cached      map[string][]string
	remote      map[string][]string
	getenv      func(string) string
	credentials CredentialStore
	list        ModelLister
	registry    Registry
	refreshMu   sync.Mutex
	inflight    *catalogRefresh
}

type catalogRefresh struct {
	done      chan struct{}
	providers []ProviderModels
	err       error
}

func NewCatalog(cfg Config, cachePath string, getenv func(string) string, list ModelLister, credentials CredentialStore, registry Registry) *Catalog {
	if getenv == nil {
		getenv = os.Getenv
	}
	if list == nil {
		list = llm.ListModels
	}
	if registry == nil {
		registry = DefaultRegistry()
	}
	c := &Catalog{config: cfg, cachePath: cachePath, cached: map[string][]string{}, remote: map[string][]string{}, getenv: getenv, credentials: credentials, list: list, registry: registry}
	if cachePath != "" {
		if data, err := os.ReadFile(cachePath); err == nil && json.Unmarshal(data, &c.cache) == nil {
			for _, entry := range c.cache.Providers {
				for _, provider := range cfg.Providers {
					if entry.ID == provider.ID && entry.BaseURL == provider.BaseURL && !provider.DisableModelDiscovery {
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
		capabilities, _ := c.registry.Describe(provider)
		result = append(result, ProviderModels{ID: provider.ID, Name: provider.Name, Models: models, Capabilities: capabilities})
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
			return CloneProviderModels(refresh.providers), refresh.err
		}
	}
	refresh := &catalogRefresh{done: make(chan struct{})}
	c.inflight = refresh
	c.refreshMu.Unlock()

	providers, err := c.refresh(ctx)
	refresh.providers = CloneProviderModels(providers)
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
		if provider.DisableModelDiscovery {
			continue
		}
		models, err := c.list(ctx, provider.BaseURL, apiKeyFor(provider, c.getenv, c.credentials))
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

// CloneProviderModels deep-copies a catalog snapshot (each entry's Models slice
// and declared context windows too), so callers that keep or mutate the result
// never touch what another owner still holds.
func CloneProviderModels(in []ProviderModels) []ProviderModels {
	out := make([]ProviderModels, len(in))
	for i, provider := range in {
		out[i] = provider
		out[i].Models = append([]string(nil), provider.Models...)
		if provider.Capabilities.ContextWindows != nil {
			windows := make(map[string]int, len(provider.Capabilities.ContextWindows))
			for model, window := range provider.Capabilities.ContextWindows {
				windows[model] = window
			}
			out[i].Capabilities.ContextWindows = windows
		}
	}
	return out
}

func saveCache(path string, cache Cache) error {
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(path, data)
}

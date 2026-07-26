package providerconfig

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/K3N4Y/atenea/internal/llm"
)

type Active struct {
	ProviderID   string
	ProviderName string
	BaseURL      string
	Model        string
	// LocalModels is what the active provider declared about its models (see
	// Provider.LocalModels). A host that shapes a turn around the difference reads
	// it from the selection rather than keeping a second copy of it.
	LocalModels bool
}

type SaveConfig func(path string, cfg Config) error

type Service struct {
	mu       sync.RWMutex
	path     string
	config   Config
	catalog  *Catalog
	switcher *llm.SwitchableProvider
	getenv   func(string) string
	registry Registry
	save     SaveConfig
	// cachePath and list are what every catalog this service publishes is built
	// with. They are held here rather than read back off the current catalog
	// because there may not be one yet: a host that passed no defaults and has no
	// config file gets its first catalog from a Declare, and fishing the lister out
	// of a nil catalog would silently substitute the real network one.
	cachePath string
	list      ModelLister
	// credentials persists secrets and tokens resolves them. They are two fields
	// because they are two jobs: Connect and Connectable read and write the store,
	// while everything that needs an actual bearer string goes through the
	// resolver, which is the only thing allowed to run a command for one.
	credentials CredentialStore
	tokens      *CredentialResolver
	// builtIn are the ids of the catalog this build ships, so Forget can tell a
	// provider the user declared from one that would be merged back at the next
	// launch. Empty when the host passed no defaults: then nothing is built in.
	builtIn map[string]struct{}
	// validateKey guards Connect: nil means defaultKeyValidator (real network
	// check); tests inject their own.
	validateKey KeyValidator
}

// Open loads the provider configuration and activates the persisted selection.
//
// ctx bounds the credential resolution that activation needs: a selected
// provider may authenticate with an exec credential, and running its command is
// the one part of opening this service that can block on something other than
// the local disk.
func Open(ctx context.Context, path, cachePath string, fallback llm.ProviderSnapshot, getenv func(string) string, registry Registry, save SaveConfig, list ModelLister, credentials CredentialStore, defaults ...Config) (*Service, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if registry == nil {
		registry = DefaultRegistry()
	}
	if save == nil {
		save = Save
	}
	switcher, err := llm.NewSwitchableProvider(fallback)
	if err != nil {
		return nil, err
	}
	s := &Service{path: path, switcher: switcher, getenv: getenv, registry: registry, save: save, cachePath: cachePath, list: list, credentials: credentials, tokens: NewCredentialResolver(credentials)}
	var shipped Config
	if len(defaults) > 0 {
		shipped = defaults[0]
		if err := normalizeAndValidate(&shipped); err != nil {
			return s, fmt.Errorf("validate default provider config: %w", err)
		}
		s.builtIn = providerIDs(shipped.Providers)
	}
	cfg, loadErr := Load(path)
	if loadErr != nil {
		if errors.Is(loadErr, os.ErrNotExist) {
			if len(defaults) > 0 {
				s.config = shipped
				s.catalog = s.newCatalog(shipped)
			}
			return s, nil
		}
		return s, fmt.Errorf("load provider config: %w", loadErr)
	}
	if len(defaults) > 0 {
		cfg = mergeMissingProviders(cfg, shipped)
	}
	s.config = cfg
	s.catalog = s.newCatalog(cfg)
	provider, ok := findProvider(cfg, cfg.Selected.Provider)
	if !ok || cfg.Selected.Model == "" {
		return s, errors.New("provider config has no active selection")
	}
	apiKey, err := resolveAPIKey(ctx, provider, getenv, s.tokens)
	if err != nil {
		return s, err
	}
	delegate, err := registry.Build(provider, cfg.Selected.Model, apiKey)
	if err != nil {
		return s, err
	}
	s.switcher.Swap(snapshot(provider, cfg.Selected.Model, delegate))
	return s, nil
}

func mergeMissingProviders(cfg, defaults Config) Config {
	seen := providerIDs(cfg.Providers)
	for _, provider := range defaults.Providers {
		if _, ok := seen[provider.ID]; ok {
			continue
		}
		cfg.Providers = append(cfg.Providers, provider)
	}
	return cfg
}

func providerIDs(providers []Provider) map[string]struct{} {
	ids := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		ids[provider.ID] = struct{}{}
	}
	return ids
}

func (s *Service) Provider() *llm.SwitchableProvider { return s.switcher }
func (s *Service) Active() Active {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeLocked()
}

// activeLocked is Active for callers that already hold s.mu — every mutation
// reports the resulting selection, and s.mu is not reentrant.
func (s *Service) activeLocked() Active {
	snapshot := s.switcher.Acquire()
	// The selection can name a provider the config does not have: the
	// environment fallback and the offline provider are both built without one.
	// Then there is nothing declared, which is the zero value.
	provider, _ := findProvider(s.config, snapshot.ProviderID)
	return Active{
		ProviderID:   snapshot.ProviderID,
		ProviderName: snapshot.ProviderName,
		BaseURL:      snapshot.BaseURL,
		Model:        snapshot.Model,
		LocalModels:  provider.LocalModels,
	}
}
func (s *Service) Catalog() []ProviderModels {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.catalog == nil {
		return nil
	}
	return s.markBuiltIn(s.catalog.Snapshot())
}
func (s *Service) Refresh(ctx context.Context) ([]ProviderModels, error) {
	s.mu.RLock()
	catalog := s.catalog
	s.mu.RUnlock()
	if catalog == nil {
		return nil, nil
	}
	providers, err := catalog.Refresh(ctx)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.markBuiltIn(providers), err
}

// markBuiltIn stamps which entries of a catalog snapshot ship with atenea. The
// catalog answers about models and knows nothing about where a provider came
// from; the service does, because it is the one that merged them.
func (s *Service) markBuiltIn(providers []ProviderModels) []ProviderModels {
	for i := range providers {
		_, providers[i].BuiltIn = s.builtIn[providers[i].ID]
	}
	return providers
}

func (s *Service) Select(ctx context.Context, providerID, model string) (Active, error) {
	if model == "" {
		return s.Active(), errors.New("model is required")
	}
	return s.applySelection(ctx, providerID, model)
}

// Declare adds or replaces a provider the user declared themselves — a local
// endpoint (LM Studio, Ollama) or a gateway this build does not ship — and
// persists it in the same providers.json every host reads, so declaring one in
// the desktop app makes it selectable in the terminal too.
//
// It does not select it: declaring an endpoint and chatting with it are two
// decisions, and the second one is Select.
func (s *Service) Declare(def Provider) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.builtIn[def.ID]; ok {
		return fmt.Errorf("provider %q ships with atenea; declare yours under another id", def.ID)
	}
	if !s.registry.Speaks(def.Type) {
		return fmt.Errorf("wire format %q is not one this build speaks (known types: %s)", def.Type, strings.Join(s.registry.Types(), ", "))
	}
	if err := validateBaseURL(def.BaseURL); err != nil {
		return fmt.Errorf("provider %q: %w", def.ID, err)
	}
	next := s.config
	next.Providers = append([]Provider(nil), s.config.Providers...)
	replaced := false
	for i := range next.Providers {
		if next.Providers[i].ID == def.ID {
			next.Providers[i] = def
			replaced = true
			break
		}
	}
	if !replaced {
		next.Providers = append(next.Providers, def)
	}
	if err := normalizeAndValidate(&next); err != nil {
		return err
	}
	return s.publishLocked(next)
}

// Forget removes a provider the user declared. A provider that ships with atenea
// cannot be forgotten — the built-in catalog is merged in at every launch, so it
// would silently come back — and neither can the active one: dropping the
// selection out from under a live conversation is worse than asking for another
// one first.
func (s *Service) Forget(providerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.builtIn[providerID]; ok {
		return fmt.Errorf("provider %q ships with atenea and cannot be removed", providerID)
	}
	if _, ok := findProvider(s.config, providerID); !ok {
		return fmt.Errorf("provider %q is not configured", providerID)
	}
	if s.config.Selected.Provider == providerID {
		return fmt.Errorf("provider %q is the active one; select another provider first", providerID)
	}
	next := s.config
	next.Providers = make([]Provider, 0, len(s.config.Providers))
	for _, provider := range s.config.Providers {
		if provider.ID == providerID {
			continue
		}
		next.Providers = append(next.Providers, provider)
	}
	return s.publishLocked(next)
}

// publishLocked persists a changed configuration and republishes the catalog
// over it. It writes first: a config that could not be saved must not become the
// one this process answers from, or the two disagree until the next launch.
func (s *Service) publishLocked(next Config) error {
	if err := s.save(s.path, next); err != nil {
		return err
	}
	s.config = next
	s.catalog = s.newCatalog(next)
	return nil
}

// newCatalog builds a catalog over cfg with this service's own dependencies, so
// every catalog it ever publishes discovers models the same way.
func (s *Service) newCatalog(cfg Config) *Catalog {
	return NewCatalog(cfg, s.cachePath, s.getenv, s.list, s.tokens, s.registry)
}

// applySelection resolves the provider's credential and then activates the
// selection. The resolution runs outside s.mu on purpose: an exec credential
// runs a command, and holding the write lock for its timeout would freeze every
// reader — the model picker, the composer footer, the running turn's view of
// what it is talking to. Connect validates outside the lock for the same reason.
func (s *Service) applySelection(ctx context.Context, providerID, model string) (Active, error) {
	s.mu.RLock()
	provider, ok := findProvider(s.config, providerID)
	s.mu.RUnlock()
	if !ok {
		return s.Active(), fmt.Errorf("provider %q is not configured", providerID)
	}
	apiKey, err := resolveAPIKey(ctx, provider, s.getenv, s.tokens)
	if err != nil {
		return s.Active(), err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectLocked(providerID, model, apiKey)
}

// selectLocked applies a provider/model selection with an already-resolved key;
// the caller holds s.mu. It re-reads the provider under the lock because the
// configuration may have changed while the credential was resolving.
func (s *Service) selectLocked(providerID, model, apiKey string) (Active, error) {
	provider, ok := findProvider(s.config, providerID)
	if !ok {
		return s.activeLocked(), fmt.Errorf("provider %q is not configured", providerID)
	}
	delegate, err := s.registry.Build(provider, model, apiKey)
	if err != nil {
		return s.activeLocked(), err
	}
	next := s.config
	next.Providers = append([]Provider(nil), s.config.Providers...)
	next.Selected = Selection{Provider: providerID, Model: model}
	if err := s.publishLocked(next); err != nil {
		return s.activeLocked(), err
	}
	s.switcher.Swap(snapshot(provider, model, delegate))
	return s.activeLocked(), nil
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

// keylessAPIKey is what a provider that declares no API-key variable and stores
// no credential is built with. Local endpoints ignore the header entirely.
const keylessAPIKey = "atenea-keyless-provider"

// apiKeyFor resolves a provider's key for *listing* models, without judging
// absence: the environment override wins, then the stored credential. Empty
// means "no key", which is fine for a keyless local endpoint.
//
// It goes through the token cache. Listing walks every configured provider, so
// resolving fresh here would run one command per exec-credentialed provider on
// every refresh. A credential that cannot be honored is still an error — a
// broken token command deserves a warning in the picker, not a silent 401.
func apiKeyFor(ctx context.Context, provider Provider, getenv func(string) string, credentials *CredentialResolver) (string, error) {
	if value := environmentKey(provider, getenv); value != "" {
		return value, nil
	}
	return credentials.CachedToken(ctx, provider.ID)
}

// resolveAPIKey is apiKeyFor for a *selection* rather than a listing: the string
// it returns is baked into the adapter that carries the conversation, so an exec
// credential runs now instead of being served from the cache. A provider that
// needs no key at all gets a placeholder, and a missing key is an error that
// names both ways to supply one.
//
// The stored credential is consulted whether or not the provider declares an
// API-key variable: a provider authenticated by a command has no variable to
// name, and handing it the keyless placeholder while a credential sits in the
// file would be a silently wrong answer.
func resolveAPIKey(ctx context.Context, provider Provider, getenv func(string) string, credentials *CredentialResolver) (string, error) {
	if value := environmentKey(provider, getenv); value != "" {
		return value, nil
	}
	value, err := credentials.Token(ctx, provider.ID)
	if err != nil {
		return "", err
	}
	if value != "" {
		return value, nil
	}
	if provider.APIKeyEnv == "" {
		return keylessAPIKey, nil
	}
	return "", fmt.Errorf("no API key for provider %q: set %s or run /connect", provider.ID, provider.APIKeyEnv)
}

func environmentKey(provider Provider, getenv func(string) string) string {
	if provider.APIKeyEnv == "" {
		return ""
	}
	return getenv(provider.APIKeyEnv)
}

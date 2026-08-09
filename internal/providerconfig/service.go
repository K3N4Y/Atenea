package providerconfig

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/tool/editmode"
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
	mu              sync.RWMutex
	path            string
	agentModelsPath string
	agentModelsErr  error
	config          Config
	agentModels     map[string]AgentModelSelection
	catalog         *Catalog
	switcher        *llm.SwitchableProvider
	getenv          func(string) string
	registry        Registry
	save            SaveConfig
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
	// logins are the device-code logins in flight, one per provider at most, under
	// a lock of their own. Waiting for a human must not hold the lock the model
	// picker and the composer footer read the configuration through.
	//
	// loginSeq numbers the attempts in the order they STARTED, which is not the
	// order they finish minting: the code arrives after a network round trip, so two
	// attempts can land inverted. The number is what tells the newer of the two from
	// the older once both are back.
	loginMu  sync.Mutex
	loginSeq uint64
	logins   map[string]*pendingLogin
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
	agentModelsPath := filepath.Join(filepath.Dir(path), "agent-models.json")
	s := &Service{path: path, agentModelsPath: agentModelsPath, agentModels: map[string]AgentModelSelection{}, switcher: switcher, getenv: getenv, registry: registry, save: save, cachePath: cachePath, list: list, credentials: credentials, tokens: NewCredentialResolver(credentials)}
	agentModels, agentModelsErr := loadAgentModels(agentModelsPath)
	if agentModelsErr == nil {
		s.agentModels = agentModels
	} else {
		s.agentModelsErr = agentModelsErr
	}
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
			if agentModelsErr != nil {
				return s, fmt.Errorf("load agent model config: %w", agentModelsErr)
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
	if cfg.Selected.Provider == "" && cfg.Selected.Model == "" {
		if agentModelsErr != nil {
			return s, fmt.Errorf("load agent model config: %w", agentModelsErr)
		}
		return s, nil
	}
	provider, ok := findProvider(cfg, cfg.Selected.Provider)
	if !ok || cfg.Selected.Model == "" {
		return s, errors.New("provider config has no active selection")
	}
	apiKey, err := resolveAPIKey(ctx, provider, getenv, s.tokens)
	if err != nil {
		return s, err
	}
	delegate, err := registry.Build(s.buildParams(provider, cfg.Selected.Model, apiKey))
	if err != nil {
		return s, err
	}
	s.switcher.Swap(snapshot(provider, cfg.Selected.Model, delegate))
	if agentModelsErr != nil {
		return s, fmt.Errorf("load agent model config: %w", agentModelsErr)
	}
	return s, nil
}
func (s *Service) ReasoningEffort() llm.ReasoningEffort {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.config.Selected.Provider == "" || s.config.Selected.Model == "" {
		return ""
	}
	return s.config.Selected.ReasoningEffort
}

// EditSettings returns a detached turn configuration from the same persisted
// local file as provider selection. Environment overrides are resolved later by
// editmode when the turn materializes.
func (s *Service) EditSettings(model, _ string) (editmode.Config, error) {
	s.mu.RLock()
	edit := s.config.Edit
	s.mu.RUnlock()
	variants := make(map[string]editmode.Mode, len(edit.ModelVariants))
	for name, mode := range edit.ModelVariants {
		variants[name] = mode
	}
	return editmode.Config{
		Model: model, ModelVariants: variants, Setting: string(edit.Mode),
		Fuzzy: edit.Fuzzy, Threshold: edit.FuzzyThreshold,
		EnforceSeenLines: edit.EnforceSeenLines,
	}, nil
}

func (s *Service) SetReasoningEffort(effort llm.ReasoningEffort) error {
	if err := validateReasoningEffort(effort); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.config.Selected.Provider == "" || s.config.Selected.Model == "" {
		return nil
	}
	if s.config.Selected.ReasoningEffort == effort {
		return nil
	}
	next := s.config
	next.Providers = append([]Provider(nil), s.config.Providers...)
	next.Selected.ReasoningEffort = effort
	return s.publishLocked(next)
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

// AgentModels returns a detached copy of all configured overrides.
func (s *Service) AgentModels() map[string]AgentModelSelection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneAgentModels(s.agentModels)
}

// AgentModel returns the configured override, without applying manifest or
// global-provider defaults.
func (s *Service) AgentModel(agentName string) (AgentModelSelection, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	selection, ok := s.agentModels[agentName]
	return selection, ok
}

// EffectiveAgentModel returns the configured override, or the manifest model
// on the active provider. false means that the subagent inherits its parent.
func (s *Service) EffectiveAgentModel(agentName, manifestModel string) (AgentModelSelection, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	activeProvider := s.switcher.Acquire().ProviderID
	if selection, ok := s.agentModels[agentName]; ok {
		if selection.Provider == "" {
			selection.Provider = activeProvider
		}
		return selection, true
	}
	if strings.TrimSpace(manifestModel) == "" {
		return AgentModelSelection{}, false
	}
	return AgentModelSelection{Provider: activeProvider, Model: manifestModel}, true
}

// SetAgentModel strongly validates the provider, offered model, and credential
// before atomically persisting an override. Credential commands run without the
// service lock.
func (s *Service) SetAgentModel(ctx context.Context, agentName string, selection AgentModelSelection) error {
	if err := validateAgentSelection(agentName, selection); err != nil {
		return err
	}
	s.mu.RLock()
	if s.agentModelsErr != nil {
		err := s.agentModelsErr
		s.mu.RUnlock()
		return fmt.Errorf("agent model config is invalid: %w", err)
	}
	providerID := selection.Provider
	if providerID == "" {
		providerID = s.switcher.Acquire().ProviderID
	}
	provider, ok := findProvider(s.config, providerID)
	modelKnown := s.modelKnownLocked(providerID, selection.Model)
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("provider %q is not configured", providerID)
	}
	if !modelKnown {
		return fmt.Errorf("model %q is not offered by provider %q", selection.Model, providerID)
	}
	if _, err := resolveAPIKey(ctx, provider, s.getenv, s.tokens); err != nil {
		return fmt.Errorf("resolve credential for provider %q: %w", providerID, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.agentModelsErr != nil {
		return fmt.Errorf("agent model config is invalid: %w", s.agentModelsErr)
	}
	currentProviderID := selection.Provider
	if currentProviderID == "" {
		currentProviderID = s.switcher.Acquire().ProviderID
	}
	if currentProviderID != providerID {
		return errors.New("active provider changed while validating agent model")
	}
	if _, ok := findProvider(s.config, providerID); !ok || !s.modelKnownLocked(providerID, selection.Model) {
		return fmt.Errorf("provider %q or model %q changed while validating agent model", providerID, selection.Model)
	}
	next := cloneAgentModels(s.agentModels)
	next[agentName] = selection
	if err := saveAgentModels(s.agentModelsPath, next); err != nil {
		return err
	}
	s.agentModels = next
	return nil
}

func (s *Service) ClearAgentModel(agentName string) error {
	if strings.TrimSpace(agentName) == "" {
		return errors.New("agent name is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.agentModelsErr != nil {
		return fmt.Errorf("agent model config is invalid: %w", s.agentModelsErr)
	}
	if _, ok := s.agentModels[agentName]; !ok {
		return nil
	}
	next := cloneAgentModels(s.agentModels)
	delete(next, agentName)
	if err := saveAgentModels(s.agentModelsPath, next); err != nil {
		return err
	}
	s.agentModels = next
	return nil
}

func (s *Service) modelKnownLocked(providerID, model string) bool {
	if s.catalog == nil {
		return false
	}
	for _, provider := range s.catalog.Snapshot() {
		if provider.ID != providerID {
			continue
		}
		for _, offered := range provider.Models {
			if offered == model {
				return true
			}
		}
		return false
	}
	return false
}

// ResolveAgentModel applies override > manifest on active provider > parent
// inheritance. Explicit provider failures never fall back to the active one.
func (s *Service) ResolveAgentModel(ctx context.Context, agentName, manifestModel string) (llm.Provider, error) {
	s.mu.RLock()
	selection, overridden := s.agentModels[agentName]
	if !overridden {
		if strings.TrimSpace(manifestModel) == "" {
			s.mu.RUnlock()
			return nil, nil
		}
		selection = AgentModelSelection{Model: manifestModel}
	}
	providerID := selection.Provider
	if providerID == "" {
		providerID = s.switcher.Acquire().ProviderID
	}
	provider, ok := findProvider(s.config, providerID)
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("provider %q is not configured", providerID)
	}
	apiKey, err := resolveAPIKey(ctx, provider, s.getenv, s.tokens)
	if err != nil {
		return nil, err
	}
	delegate, err := s.registry.Build(s.buildParams(provider, selection.Model, apiKey))
	if err != nil {
		return nil, err
	}
	return &fixedProvider{snapshot: snapshot(provider, selection.Model, delegate), reasoningEffort: selection.ReasoningEffort}, nil
}

// ResolveModel retains the role-model API by resolving a manifest model with no
// named override.
func (s *Service) ResolveModel(ctx context.Context, model string) (llm.Provider, error) {
	if strings.TrimSpace(model) == "" {
		return nil, errors.New("role model is empty")
	}
	return s.ResolveAgentModel(ctx, "", model)
}

type fixedProvider struct {
	snapshot        llm.ProviderSnapshot
	reasoningEffort llm.ReasoningEffort
}

func (p *fixedProvider) Acquire() llm.ProviderSnapshot { return p.snapshot }

func (p *fixedProvider) Stream(ctx context.Context, request llm.Request) (<-chan llm.Event, error) {
	request.Model = p.snapshot.Model
	// A configured effort is an explicit subagent policy and therefore wins over
	// a caller preference. Empty preserves the request and provider default.
	if p.reasoningEffort != "" {
		request.Reasoning = &llm.ReasoningPreference{Effort: p.reasoningEffort}
	}
	return p.snapshot.Provider.Stream(ctx, request)
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
	if next.Selected.Provider == "" && next.Selected.Model == "" {
		next.Selected.ReasoningEffort = ""
	}
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

func (s *Service) selectLocked(providerID, model, apiKey string) (Active, error) {
	provider, ok := findProvider(s.config, providerID)
	if !ok {
		return s.activeLocked(), fmt.Errorf("provider %q is not configured", providerID)
	}
	delegate, err := s.registry.Build(s.buildParams(provider, model, apiKey))
	if err != nil {
		return s.activeLocked(), err
	}
	sameSelection := s.config.Selected.Provider == providerID && s.config.Selected.Model == model
	if !sameSelection {
		next := s.config
		next.Providers = append([]Provider(nil), s.config.Providers...)
		next.Selected = Selection{Provider: providerID, Model: model}
		if err := s.publishLocked(next); err != nil {
			return s.activeLocked(), err
		}
	}
	s.switcher.Swap(snapshot(provider, model, delegate))
	return s.activeLocked(), nil
}

// buildParams is what the registry needs to construct one live provider: the
// declared endpoint, the model, the resolved static credential, and — for a format
// whose credential is a login — the seam that resolves one per request.
func (s *Service) buildParams(provider Provider, model, apiKey string) BuildParams {
	return BuildParams{Provider: provider, Model: model, APIKey: apiKey, Tokens: s.tokenSource(provider)}
}

// tokenSource is the per-request credential seam for a provider whose format
// authenticates with a login, and nil for every other one. Which of the two a
// provider is comes from the registry rather than from its declared type read here:
// the registry is where a wire format's facts live.
func (s *Service) tokenSource(provider Provider) llm.OAuthTokenSource {
	flow, ok := s.registry.OAuth(provider.Type)
	if !ok || flow.Refresh == nil {
		return nil
	}
	return s.tokens.OAuthTokenSource(provider.ID, flow.Refresh(provider))
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

package providerconfig

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/K3N4Y/atenea/internal/llm"
)

// connectableProviderIDs is the set of providers /connect supports. Growing it
// means adding the entry here plus a validation strategy in defaultKeyValidator;
// the storage, resolution, and UI flow are already generic.
var connectableProviderIDs = map[string]struct{}{
	"anthropic":    {},
	"openai":       {},
	"openai-codex": {},
	"openrouter":   {},
	"opencode":     {},
	"opencode-go":  {},
}

// The ways a provider is connected. They are two different conversations with the
// user — paste a secret, or approve a code somewhere else — so a UI has to know
// which one it is about to have before it draws anything.
const (
	ConnectAPIKey     = "api_key"
	ConnectDeviceCode = "device_code"
)

// ConnectableProvider is one row of the /connect picker: a provider the user can
// connect, how it is connected, and whether a credential is already stored for it.
type ConnectableProvider struct {
	ID   string
	Name string
	// Kind is [ConnectAPIKey] or [ConnectDeviceCode]. It is reported rather than
	// inferred by the host: a UI that guessed from the id would be one release
	// behind the catalog, and the wrong guess is a masked input where a code should
	// be.
	Kind      string
	Connected bool
}

// KeyValidator checks an API key against the provider before it is stored.
// Injectable so tests (and future providers) replace the network call.
type KeyValidator func(ctx context.Context, provider Provider, apiKey string) error

// defaultKeyValidator picks the validation strategy per provider. Only
// providers in connectableProviderIDs ever reach it.
func defaultKeyValidator(ctx context.Context, provider Provider, apiKey string) error {
	switch provider.ID {
	case "anthropic":
		return llm.ValidateAnthropicKey(ctx, provider.BaseURL, apiKey)
	case "openai":
		return llm.ValidateOpenAIKey(ctx, provider.BaseURL, apiKey)
	case "openrouter":
		return llm.ValidateOpenRouterKey(ctx, provider.BaseURL, apiKey)
	case "opencode", "opencode-go":
		// OpenCode exposes /models publicly and has no documented non-billable
		// credential check. Connect already rejects empty keys; the first model
		// request reports invalid credentials or missing Zen/Go entitlement.
		return nil
	default:
		return fmt.Errorf("provider %q does not support key validation", provider.ID)
	}
}

// Connectable lists the providers /connect can manage, with their stored
// credential state. The environment override is deliberately not reflected
// here: /connect manages stored credentials, and showing an env-derived
// "connected" would suggest there is something to rotate when there is not.
func (s *Service) Connectable() []ConnectableProvider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ConnectableProvider, 0, len(connectableProviderIDs))
	for _, provider := range s.config.Providers {
		if _, ok := connectableProviderIDs[provider.ID]; !ok {
			continue
		}
		connected := false
		if s.credentials != nil {
			_, connected = s.credentials.Get(provider.ID)
		}
		out = append(out, ConnectableProvider{ID: provider.ID, Name: provider.Name, Kind: s.connectKindLocked(provider), Connected: connected})
	}
	return out
}

// connectKindLocked is how a provider is connected, answered by the registry: the
// format that decides how a request is shaped is the same one that decides how it
// is authenticated. The caller holds s.mu.
func (s *Service) connectKindLocked(provider Provider) string {
	if _, ok := s.registry.OAuth(provider.Type); ok {
		return ConnectDeviceCode
	}
	return ConnectAPIKey
}

// Connect validates an API key against the provider, persists it, and makes
// the connection usable right away: with no active selection it activates the
// provider on its default model (first curated model), and when the provider
// is already the active one it rebuilds the live delegate so a rotated key
// takes effect without a restart. A selection on another provider is left
// alone — the credential just waits for the next /model switch.
func (s *Service) Connect(ctx context.Context, providerID, apiKey string) (Active, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return s.Active(), errors.New("API key is required")
	}
	if s.credentials == nil {
		return s.Active(), errors.New("credential storage is unavailable")
	}
	if _, ok := connectableProviderIDs[providerID]; !ok {
		return s.Active(), fmt.Errorf("provider %q does not support /connect yet", providerID)
	}

	// The network validation runs outside the lock: a slow endpoint must not
	// freeze concurrent Catalog/Select calls for up to the validator timeout.
	s.mu.RLock()
	provider, ok := findProvider(s.config, providerID)
	validate := s.validateKey
	_, isLogin := s.registry.OAuth(provider.Type)
	s.mu.RUnlock()
	if !ok {
		return s.Active(), fmt.Errorf("provider %q is not configured", providerID)
	}
	// Storing a pasted string for a provider whose credential is a login would
	// produce a credential nothing can refresh and no request can route.
	if isLogin {
		return s.Active(), fmt.Errorf("provider %q is connected by logging in, not with an API key", providerID)
	}
	if validate == nil {
		validate = defaultKeyValidator
	}
	if err := validate(ctx, provider, apiKey); err != nil {
		return s.Active(), err
	}

	s.mu.Lock()
	err := s.credentials.Put(providerID, Credential{Type: CredentialTypeAPIKey, APIKey: apiKey})
	selected := s.config.Selected
	provider, _ = findProvider(s.config, providerID)
	s.mu.Unlock()
	if err != nil {
		return s.Active(), err
	}

	// The selection goes through applySelection rather than the locked path: it
	// resolves the credential, and resolution is what must not happen under s.mu.
	switch {
	case selected.Provider == providerID:
		return s.applySelection(ctx, providerID, selected.Model)
	case selected.Provider == "":
		if len(provider.Models) > 0 {
			return s.applySelection(ctx, providerID, provider.Models[0])
		}
	}
	return s.Active(), nil
}

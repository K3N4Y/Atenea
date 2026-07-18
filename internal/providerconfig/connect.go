package providerconfig

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"atenea/internal/llm"
)

// connectableProviderIDs is the set of providers /connect supports. Growing it
// means adding the entry here plus a validation strategy in defaultKeyValidator;
// the storage, resolution, and UI flow are already generic.
var connectableProviderIDs = map[string]struct{}{
	"openrouter": {},
}

// ConnectableProvider is one row of the /connect picker: a provider the user
// can connect plus whether a credential is already stored for it.
type ConnectableProvider struct {
	ID        string
	Name      string
	Connected bool
}

// KeyValidator checks an API key against the provider before it is stored.
// Injectable so tests (and future providers) replace the network call.
type KeyValidator func(ctx context.Context, provider Provider, apiKey string) error

// defaultKeyValidator picks the validation strategy per provider. Only
// providers in connectableProviderIDs ever reach it.
func defaultKeyValidator(ctx context.Context, provider Provider, apiKey string) error {
	switch provider.ID {
	case "openrouter":
		return llm.ValidateOpenRouterKey(ctx, provider.BaseURL, apiKey)
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
		out = append(out, ConnectableProvider{ID: provider.ID, Name: provider.Name, Connected: connected})
	}
	return out
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
	s.mu.RUnlock()
	if !ok {
		return s.Active(), fmt.Errorf("provider %q is not configured", providerID)
	}
	if validate == nil {
		validate = defaultKeyValidator
	}
	if err := validate(ctx, provider, apiKey); err != nil {
		return s.Active(), err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.credentials.Put(providerID, Credential{Type: CredentialTypeAPIKey, APIKey: apiKey}); err != nil {
		return s.Active(), err
	}
	switch {
	case s.config.Selected.Provider == providerID:
		return s.selectLocked(providerID, s.config.Selected.Model)
	case s.config.Selected.Provider == "":
		provider, ok := findProvider(s.config, providerID)
		if ok && len(provider.Models) > 0 {
			return s.selectLocked(providerID, provider.Models[0])
		}
	}
	return s.Active(), nil
}

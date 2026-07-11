package llm

import (
	"context"
	"errors"
	"sync/atomic"
)

type ProviderSnapshot struct {
	ProviderID   string
	ProviderName string
	BaseURL      string
	Model        string
	Provider     Provider
}

type SwitchableProvider struct {
	active atomic.Pointer[ProviderSnapshot]
}

func NewSwitchableProvider(initial ProviderSnapshot) (*SwitchableProvider, error) {
	if err := validateProviderSnapshot(initial); err != nil {
		return nil, err
	}
	provider := &SwitchableProvider{}
	provider.Swap(initial)
	return provider, nil
}

func (p *SwitchableProvider) Acquire() ProviderSnapshot {
	if p == nil {
		return ProviderSnapshot{}
	}
	snapshot := p.active.Load()
	if snapshot == nil {
		return ProviderSnapshot{}
	}
	return *snapshot
}

func (p *SwitchableProvider) Swap(next ProviderSnapshot) {
	copy := next
	p.active.Store(&copy)
}

func (p *SwitchableProvider) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	snapshot := p.Acquire()
	if snapshot.Provider == nil {
		return nil, errors.New("switchable provider has no active delegate")
	}
	req.Model = snapshot.Model
	return snapshot.Provider.Stream(ctx, req)
}

func Acquire(provider Provider) ProviderSnapshot {
	if switcher, ok := provider.(interface{ Acquire() ProviderSnapshot }); ok {
		return switcher.Acquire()
	}
	return ProviderSnapshot{Provider: provider}
}

func validateProviderSnapshot(snapshot ProviderSnapshot) error {
	if snapshot.ProviderID == "" || snapshot.ProviderName == "" || snapshot.BaseURL == "" || snapshot.Model == "" || snapshot.Provider == nil {
		return errors.New("provider snapshot requires id, name, base URL, model, and provider")
	}
	return nil
}

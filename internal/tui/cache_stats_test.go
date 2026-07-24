//go:build !production

package tui

import (
	"strings"
	"testing"

	"atenea/internal/session"
)

func TestModel_CacheStatsCommandTogglesLocallyWithoutSendingPrompt(t *testing.T) {
	agent := &fakeAgent{}
	m := NewModel(agent, "s1", nil)
	m.input.SetValue("/cache-stats")

	m, cmd := m.submitPrompt()
	if cmd != nil || !m.cacheStatsEnabled || len(agent.sent) != 0 || m.input.Value() != "" {
		t.Fatalf("first toggle = enabled:%v cmd:%v sent:%v input:%q", m.cacheStatsEnabled, cmd, agent.sent, m.input.Value())
	}
	m.input.SetValue("/cache-stats")
	m, cmd = m.submitPrompt()
	if cmd != nil || m.cacheStatsEnabled || len(agent.sent) != 0 {
		t.Fatalf("second toggle = enabled:%v cmd:%v sent:%v", m.cacheStatsEnabled, cmd, agent.sent)
	}
}

func TestModel_CacheStatsRenderUsesNormalizedProviderDenominator(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m.usage = &session.Usage{
		InputTokens:          600,
		OutputTokens:         20,
		CacheReadTokens:      300,
		CacheWriteTokens:     100,
		CacheableInputTokens: 1000,
	}

	if got := m.tokenUsageLabel(); strings.Contains(got, "cache") {
		t.Fatalf("disabled label = %q, cache stats must be hidden", got)
	}
	m.cacheStatsEnabled = true
	if got := m.tokenUsageLabel(); !strings.Contains(got, "cache read 300") || !strings.Contains(got, "write 100") || !strings.Contains(got, "hit 30%") {
		t.Fatalf("enabled label = %q, want normalized cache read/write/hit", got)
	}
	m.usage.CacheableInputTokens = 0
	if got := m.tokenUsageLabel(); !strings.Contains(got, "hit 0%") {
		t.Fatalf("zero-denominator label = %q, want a safe zero hit rate", got)
	}
}

func TestModel_CacheStatsAppearsInDevelopmentAutocomplete(t *testing.T) {
	m := NewModel(nil, "s1", nil)
	m.input.SetValue("/cache")
	m.input.SetCursor(len([]rune("/cache")))
	m, _ = m.refreshMenu()
	for _, item := range m.menuItems {
		if item.label == "/cache-stats" && item.builtin {
			return
		}
	}
	t.Fatalf("development menu items = %#v, want /cache-stats builtin", m.menuItems)
}

//go:build production

package tui

import "testing"

func TestModel_CacheStatsIsAbsentInProduction(t *testing.T) {
	agent := &fakeAgent{}
	m := NewModel(agent, "s1", nil)
	m.composer.input.SetValue("/cache")
	m.composer.input.SetCursor(len([]rune("/cache")))
	m, _ = m.refreshMenu()
	for _, item := range m.composer.menuItems {
		if item.label == "/cache-stats" {
			t.Fatalf("production menu unexpectedly contains /cache-stats: %#v", m.composer.menuItems)
		}
	}
	for _, input := range []string{"/cache-stats", " /cache-stats "} {
		m.composer.input.SetValue(input)
		m, _ = m.submitPrompt()
		if got := agent.sent[len(agent.sent)-1].text; got != input {
			t.Fatalf("production command = %q, want literal prompt %q", got, input)
		}
	}
}

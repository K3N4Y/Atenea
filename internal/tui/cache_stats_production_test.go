//go:build production

package tui

import "testing"

func TestModel_CacheStatsIsAbsentInProduction(t *testing.T) {
	agent := &fakeAgent{}
	m := NewModel(agent, "s1", nil)
	m.input.SetValue("/cache")
	m.input.SetCursor(len([]rune("/cache")))
	m, _ = m.refreshMenu()
	for _, item := range m.menuItems {
		if item.label == "/cache-stats" {
			t.Fatalf("production menu unexpectedly contains /cache-stats: %#v", m.menuItems)
		}
	}
	for _, input := range []string{"/cache-stats", " /cache-stats "} {
		m.input.SetValue(input)
		m, _ = m.submitPrompt()
		if got := agent.sent[len(agent.sent)-1].text; got != input {
			t.Fatalf("production command = %q, want literal prompt %q", got, input)
		}
	}
}

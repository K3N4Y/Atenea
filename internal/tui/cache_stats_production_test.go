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
	m.input.SetValue("/cache-stats")
	m, _ = m.submitPrompt()
	if len(agent.sent) != 1 || agent.sent[0].text != "/cache-stats" {
		t.Fatalf("production command was intercepted: sent=%#v", agent.sent)
	}
}

//go:build !production

package tui

import (
	"fmt"
	"strings"
)

type cacheStatsState struct {
	cacheStatsEnabled bool
}

func (m Model) handleCacheStatsCommand(input string) (Model, bool) {
	if input != "/cache-stats" {
		return m, false
	}
	m.cacheStatsEnabled = !m.cacheStatsEnabled
	m.input.SetValue("")
	m.menuItems = nil
	return m.resizeViewport(), true
}

func (m Model) cacheStatsUsageLabel() string {
	if !m.cacheStatsEnabled || m.usage == nil {
		return ""
	}
	hitRate := 0
	if m.usage.CacheableInputTokens > 0 {
		hitRate = 100 * m.usage.CacheReadTokens / m.usage.CacheableInputTokens
	}
	return fmt.Sprintf(" cache read %s write %s hit %d%%", formatTokenCount(m.usage.CacheReadTokens), formatTokenCount(m.usage.CacheWriteTokens), hitRate)
}

func developmentBuiltinCommands(query string) []menuItem {
	if strings.HasPrefix("cache-stats", query) {
		return []menuItem{{label: "/cache-stats", description: "Toggle prompt cache statistics", builtin: true}}
	}
	return nil
}

func isDevelopmentBuiltinSelection(label string) bool { return label == "/cache-stats" }

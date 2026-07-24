//go:build production

package tui

type cacheStatsState struct{}

func (m Model) handleCacheStatsCommand(_ string) (Model, bool) { return m, false }
func (m Model) cacheStatsUsageLabel() string                   { return "" }
func developmentBuiltinCommands(_ string) []menuItem           { return nil }
func isDevelopmentBuiltinSelection(_ string) bool              { return false }

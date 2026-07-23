package tui

import (
	"regexp"
	"strings"
)

var providerSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(api[_-]?key["' :=?&]+)[^&\s,"'}]+`),
	regexp.MustCompile(`(?i)(authorization["' :=]+(?:bearer\s+)?)[^\s,"'}]+`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}\b`),
}

// friendlyProviderError keeps provider internals available under Details while
// making the transcript useful at a glance.
func friendlyProviderError(raw string) string {
	lower := strings.ToLower(raw)
	context := providerErrorContext(raw)
	switch {
	case strings.Contains(lower, "429") || strings.Contains(lower, "rate limit") || strings.Contains(lower, "rate-limit"):
		return context + "Rate limit reached. Please try again in a few seconds."
	case strings.Contains(lower, "401") || strings.Contains(lower, "403") || strings.Contains(lower, "unauthorized") || strings.Contains(lower, "invalid api key"):
		return context + "Authentication failed. Check your provider credentials."
	case strings.Contains(lower, "404") || strings.Contains(lower, "model not found"):
		return context + "The selected model is unavailable."
	case strings.Contains(lower, "502") || strings.Contains(lower, "503") || strings.Contains(lower, "504") || strings.Contains(lower, "connection refused"):
		return context + "The provider is temporarily unavailable."
	default:
		return context + "The provider request failed."
	}
}

func providerErrorContext(raw string) string {
	const prefix = "provider stream failed: "
	start := strings.Index(raw, prefix)
	if start < 0 {
		return ""
	}
	rest := raw[start+len(prefix):]
	if end := strings.Index(rest, "): "); end >= 0 {
		return rest[:end+1] + ": "
	}
	return ""
}

func isProviderError(raw string) bool {
	lower := strings.ToLower(raw)
	return strings.Contains(lower, "provider stream failed") ||
		strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "rate-limited")
}

func sanitizeProviderDetails(raw string) string {
	for _, pattern := range providerSecretPatterns {
		raw = pattern.ReplaceAllString(raw, `${1}[redacted]`)
	}
	return sanitizeTerminalText(raw)
}

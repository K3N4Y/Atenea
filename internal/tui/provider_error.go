package tui

import (
	"regexp"
	"strings"
)

// What must never reach the transcript, even under Details. A provider's error can
// quote the request that caused it, and this is the last stop before a secret is
// drawn on a screen that gets scrolled back, screenshotted and pasted into issues.
var providerSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(api[_-]?key["' :=?&]+)[^&\s,"'}]+`),
	// Both halves of a device-code grant. They go first because the generic
	// authorization pattern below stops at the word boundary and would never see
	// `authorization_code`, and because a leaked pair is a login an attacker can
	// complete.
	regexp.MustCompile(`(?i)((?:authorization[_-]?code|code[_-]?verifier)["' :=?&]+)[^&\s,"'}]+`),
	regexp.MustCompile(`(?i)(authorization["' :=]+(?:bearer\s+)?)[^\s,"'}]+`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}\b`),
	// The OAuth arm's own field names, however they are spelled in a body.
	regexp.MustCompile(`(?i)((?:access|refresh|id)[_-]?token["' :=?&]+)[^&\s,"'}]+`),
	// A JWT, wherever it appears. Base64url-encoded JSON always starts `eyJ`, which
	// is what makes a bare token recognizable with no field name around it.
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]*`),
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

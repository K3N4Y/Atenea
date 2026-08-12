package llm

import "testing"

// TestUsage_TotalInputTokensCountsCachedInputExactlyOnce pins the shape each
// adapter reports, because the two families disagree about what InputTokens
// means and a consumer must not have to know which one served the turn.
func TestUsage_TotalInputTokensCountsCachedInputExactlyOnce(t *testing.T) {
	tests := []struct {
		name  string
		usage Usage
		want  int
	}{{
		// Anthropic bills InputTokens as the suffix after the last cache
		// breakpoint. Reading it as the prompt size is the bug this accessor exists
		// to prevent: 2 tokens instead of 55k.
		name:  "anthropic cache hit",
		usage: Usage{InputTokens: 2, CacheReadTokens: 54_004, CacheWriteTokens: 1_800, CacheableInputTokens: 55_806},
		want:  55_806,
	}, {
		name:  "anthropic cache write",
		usage: Usage{InputTokens: 4, CacheWriteTokens: 54_000, CacheableInputTokens: 54_004},
		want:  54_004,
	}, {
		// The OpenAI-compatible families bill prompt_tokens as the whole input and
		// report cached_tokens as a subset of it, so the total must not add the
		// cached share back on top.
		name:  "openai cached subset is not added twice",
		usage: Usage{InputTokens: 10_000, CacheReadTokens: 9_000, CacheableInputTokens: 10_000},
		want:  10_000,
	}, {
		name:  "no cache accounting falls back to billed input",
		usage: Usage{InputTokens: 12_000},
		want:  12_000,
	}, {
		name:  "empty usage",
		usage: Usage{},
		want:  0,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.usage.TotalInputTokens(); got != tt.want {
				t.Errorf("TotalInputTokens() = %d, want %d", got, tt.want)
			}
		})
	}
}

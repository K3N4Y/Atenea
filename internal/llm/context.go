package llm

import (
	"encoding/json"
	"fmt"
)

const preventiveCompactionPercent = 80

var contextWindows = map[string]int{
	"anthropic/claude-opus-4.8":   200_000,
	"anthropic/claude-sonnet-4.5": 200_000,
	"anthropic/claude-3.5-sonnet": 200_000,
	"openai/gpt-4o":               128_000,
	"google/gemini-2.5-pro":       1_048_576,
}

type ContextOverflowError struct {
	Message string
}

func (e *ContextOverflowError) Error() string {
	if e.Message == "" {
		return "provider context window exceeded"
	}
	return e.Message
}

func ContextWindow(model string) (int, bool) {
	window, ok := contextWindows[model]
	return window, ok
}

func NeedsPreventiveCompaction(estimatedTokens, contextWindow int) bool {
	if estimatedTokens <= 0 || contextWindow <= 0 {
		return false
	}
	threshold := contextWindow / 100 * preventiveCompactionPercent
	if contextWindow%100 != 0 {
		threshold += (contextWindow%100*preventiveCompactionPercent + 99) / 100
	}
	return estimatedTokens >= threshold
}

func EstimateRequestTokens(req Request) int {
	bytes := len(req.System)
	for _, message := range req.Messages {
		bytes += len(message.Role) + len(message.Text) + len(message.ToolCallID) + 12
		for _, call := range message.ToolCalls {
			bytes += len(call.ID) + len(call.Name) + len(call.Arguments) + 12
		}
	}
	for _, tool := range req.Tools {
		bytes += len(tool.Name) + len(tool.Description) + len(tool.Schema) + 16
	}
	outputReserve := req.MaxOutputTokens
	if outputReserve < 0 {
		outputReserve = 0
	}
	return (bytes+2)/3 + outputReserve
}

func EstimateJSONTokens(value any) (int, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	return (len(encoded) + 2) / 3, nil
}

func FormatContextUsage(estimated, window int) string {
	return fmt.Sprintf("%d/%d estimated tokens", estimated, window)
}

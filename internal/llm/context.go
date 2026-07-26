package llm

import (
	"encoding/json"
	"fmt"
)

const preventiveCompactionPercent = 80

type ContextOverflowError struct {
	Message string
}

func (e *ContextOverflowError) Error() string {
	if e.Message == "" {
		return "provider context window exceeded"
	}
	return e.Message
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
		bytes += len(message.Role) + len(message.ToolCallID) + 12
		// A message weighs what its parts weigh. A part kind that carries bytes
		// anywhere other than Text has to be sized here as well: what the estimate
		// omits it under-counts, and an under-count is preventive compaction not
		// firing on the request that then overflows.
		for _, part := range message.Parts {
			bytes += len(part.Text)
		}
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

package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
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

// IsContextOverflow reports whether err is a provider rejection for exceeding
// the model context window — the one stream failure a host can act on, by
// compacting durable history and retrying the same turn. It unwraps, so a
// host classifies a wrapped provider failure without reaching for the cause.
func IsContextOverflow(err error) bool {
	var overflow *ContextOverflowError
	return errors.As(err, &overflow)
}

// contextOverflowMarkers name a context-window rejection as the endpoints print
// it. There is no status code that means "too long" on its own — a 400 covers
// every malformed request — so the message is the only classifier available,
// and OpenAI-compatible endpoints each word it slightly differently.
var contextOverflowMarkers = []string{
	"context_length_exceeded", // OpenAI's error code
	"context length",          // "maximum context length is 8192 tokens"
	"context window",          // Anthropic and several local runtimes
	"too long",                // "prompt is too long"
	"reduce the length",       // some OpenAI-compatible proxies
}

// classifyContextOverflow returns a typed ContextOverflowError when status and
// message describe a context-window rejection, and nil otherwise. A status of 0
// means the caller has no HTTP status to offer and only the message is judged.
//
// Every adapter has to route its failures through this: an overflow the adapter
// leaves untyped is a turn the runner cannot compact and retry, which is the
// whole point of classifying it.
func classifyContextOverflow(status int, message string) *ContextOverflowError {
	if status != 0 && status != http.StatusBadRequest {
		return nil
	}
	lower := strings.ToLower(message)
	for _, marker := range contextOverflowMarkers {
		if strings.Contains(lower, marker) {
			return &ContextOverflowError{Message: message}
		}
	}
	return nil
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

// Image token costs are bounded, not proportional to the payload: every provider
// prices an image by the tiles it covers and caps it (Anthropic tops out near
// 1600 tokens). Charging the base64 length instead would bill a 1 MiB screenshot
// as ~460k tokens and compact a session that was never close to its window, so
// the estimate approximates tiles from the encoded size and caps the result.
const (
	imageTokensPerByte = 750  // bytes of compressed image per token, empirically
	maxImageTokens     = 1600 // per-image ceiling, the largest any adapter charges
	minImageTokens     = 85   // a thumbnail still costs its base tile
)

func estimateImageTokens(part Part) int {
	tokens := len(part.Data) / imageTokensPerByte
	if tokens > maxImageTokens {
		return maxImageTokens
	}
	if tokens < minImageTokens {
		return minImageTokens
	}
	return tokens
}

func EstimateRequestTokens(req Request) int {
	bytes := len(req.System)
	imageTokens := 0
	for _, message := range req.Messages {
		bytes += len(message.Role) + len(message.ToolCallID) + 12
		// A message weighs what its parts weigh. A part kind that carries bytes
		// anywhere other than Text has to be sized here as well: what the estimate
		// omits it under-counts, and an under-count is preventive compaction not
		// firing on the request that then overflows.
		for _, part := range message.Parts {
			bytes += len(part.Text)
			if part.Kind == ImagePart {
				imageTokens += estimateImageTokens(part)
			}
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
	return (bytes+2)/3 + imageTokens + outputReserve
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

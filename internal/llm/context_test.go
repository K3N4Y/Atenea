package llm

import (
	"errors"
	"math"
	"testing"
)

func TestContextWindow_KnownAndUnknownModels(t *testing.T) {
	if got, ok := ContextWindow("anthropic/claude-opus-4.8"); !ok || got != 200_000 {
		t.Fatalf("ContextWindow known = (%d, %v), want (200000, true)", got, ok)
	}
	if got, ok := ContextWindow("totally/unknown"); ok || got != 0 {
		t.Fatalf("ContextWindow unknown = (%d, %v), want (0, false)", got, ok)
	}
}

func TestEstimateRequestTokens_IncludesSystemToolsMessagesAndOutputReserve(t *testing.T) {
	req := Request{
		Model:           "anthropic/claude-opus-4.8",
		System:          "system text",
		Messages:        []Message{{Role: "user", Text: "user text"}},
		Tools:           []ToolDef{{Name: "read", Description: "read a file", Schema: []byte(`{"type":"object"}`)}},
		MaxOutputTokens: 4_096,
	}
	withoutTools := req
	withoutTools.Tools = nil
	withoutSystem := req
	withoutSystem.System = ""
	withoutMessages := req
	withoutMessages.Messages = nil

	got := EstimateRequestTokens(req)
	if got <= EstimateRequestTokens(withoutTools) {
		t.Fatal("tool definitions must increase the estimate")
	}
	if got <= EstimateRequestTokens(withoutSystem) {
		t.Fatal("system prompt must increase the estimate")
	}
	if got <= EstimateRequestTokens(withoutMessages) {
		t.Fatal("messages must increase the estimate")
	}
	if got < req.MaxOutputTokens {
		t.Fatalf("estimate = %d, must include output reserve %d", got, req.MaxOutputTokens)
	}
}

func TestEstimateRequestTokens_IncludesAssistantToolCallsAndToolResultCallID(t *testing.T) {
	base := Request{Messages: []Message{{Role: "assistant"}, {Role: "tool", Text: "result"}}}
	withToolCall := base
	withToolCall.Messages = append([]Message(nil), base.Messages...)
	withToolCall.Messages[0].ToolCalls = []ToolCallPart{{ID: "call-1", Name: "read", Arguments: []byte(`{"path":"file.go"}`)}}
	withToolResultCallID := base
	withToolResultCallID.Messages = append([]Message(nil), base.Messages...)
	withToolResultCallID.Messages[1].ToolCallID = "call-1"

	baseEstimate := EstimateRequestTokens(base)
	if got := EstimateRequestTokens(withToolCall); got <= baseEstimate {
		t.Fatalf("assistant tool call estimate = %d, must exceed base %d", got, baseEstimate)
	}
	if got := EstimateRequestTokens(withToolResultCallID); got <= baseEstimate {
		t.Fatalf("tool result call ID estimate = %d, must exceed base %d", got, baseEstimate)
	}
}

func TestEstimateRequestTokens_NegativeOutputReserveIsClampedToZero(t *testing.T) {
	req := Request{System: "system", MaxOutputTokens: -10_000}
	withoutReserve := req
	withoutReserve.MaxOutputTokens = 0

	if got, want := EstimateRequestTokens(req), EstimateRequestTokens(withoutReserve); got != want {
		t.Fatalf("estimate with negative reserve = %d, want %d", got, want)
	}
}

func TestNeedsPreventiveCompaction_TriggersAtEightyPercent(t *testing.T) {
	window := 100
	if NeedsPreventiveCompaction(79, window) {
		t.Fatal("79% must not compact")
	}
	if !NeedsPreventiveCompaction(80, window) {
		t.Fatal("80% must compact")
	}
}

func TestNeedsPreventiveCompaction_UnknownWindowNeverTriggers(t *testing.T) {
	if NeedsPreventiveCompaction(1_000_000, 0) {
		t.Fatal("unknown window must rely on reactive overflow")
	}
}

func TestNeedsPreventiveCompaction_RejectsNonpositiveInputs(t *testing.T) {
	for _, test := range []struct {
		name      string
		estimated int
		window    int
	}{
		{name: "negative estimate", estimated: -1, window: 100},
		{name: "zero estimate", estimated: 0, window: 100},
		{name: "negative window", estimated: 80, window: -100},
		{name: "zero window", estimated: 80, window: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			if NeedsPreventiveCompaction(test.estimated, test.window) {
				t.Fatalf("NeedsPreventiveCompaction(%d, %d) = true, want false", test.estimated, test.window)
			}
		})
	}
}

func TestNeedsPreventiveCompaction_IsOverflowSafeForLargeInts(t *testing.T) {
	if !NeedsPreventiveCompaction(math.MaxInt, math.MaxInt) {
		t.Fatal("100% occupancy at MaxInt must compact")
	}
	if NeedsPreventiveCompaction(math.MaxInt/2, math.MaxInt) {
		t.Fatal("approximately 50% occupancy at MaxInt must not compact")
	}
}

func TestContextOverflowError_IsDiscoverableWithErrorsAs(t *testing.T) {
	wrapped := errors.Join(errors.New("provider failed"), &ContextOverflowError{Message: "maximum context length"})
	var overflow *ContextOverflowError
	if !errors.As(wrapped, &overflow) {
		t.Fatal("ContextOverflowError must be discoverable")
	}
}

func TestContextOverflowError_DefaultAndCustomText(t *testing.T) {
	if got := (&ContextOverflowError{}).Error(); got != "provider context window exceeded" {
		t.Fatalf("default error = %q, want %q", got, "provider context window exceeded")
	}
	if got := (&ContextOverflowError{Message: "maximum context length"}).Error(); got != "maximum context length" {
		t.Fatalf("custom error = %q, want %q", got, "maximum context length")
	}
}

func TestEstimateJSONTokens_UsesConservativeByteEstimate(t *testing.T) {
	got, err := EstimateJSONTokens(map[string]string{"key": "value"})
	if err != nil {
		t.Fatalf("EstimateJSONTokens returned error: %v", err)
	}
	if got != 5 {
		t.Fatalf("EstimateJSONTokens = %d, want 5", got)
	}
}

func TestEstimateJSONTokens_ReturnsMarshalError(t *testing.T) {
	if got, err := EstimateJSONTokens(func() {}); err == nil || got != 0 {
		t.Fatalf("EstimateJSONTokens unsupported = (%d, %v), want (0, error)", got, err)
	}
}

func TestFormatContextUsage_ReportsEstimateAndWindow(t *testing.T) {
	if got := FormatContextUsage(80, 100); got != "80/100 estimated tokens" {
		t.Fatalf("FormatContextUsage = %q, want %q", got, "80/100 estimated tokens")
	}
}

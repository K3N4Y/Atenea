package llm

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestEstimateRequestTokens_IncludesSystemToolsMessagesAndOutputReserve(t *testing.T) {
	req := Request{
		Model:           "anthropic/claude-opus-4.8",
		System:          "system text",
		Messages:        []Message{TextMessage("user", "user text")},
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
	base := Request{Messages: []Message{{Role: "assistant"}, TextMessage("tool", "result")}}
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

// A message weighs every one of its parts, not just the first: an estimate that
// stopped at one part would under-count a multi-part message and let the request
// that overflows sail past the preventive-compaction threshold.
func TestEstimateRequestTokens_WalksEveryContentPart(t *testing.T) {
	one := Request{Messages: []Message{TextMessage("user", "first")}}
	two := Request{Messages: []Message{{Role: "user", Parts: []Part{
		{Kind: TextPart, Text: "first"},
		{Kind: TextPart, Text: "second"},
	}}}}

	if EstimateRequestTokens(two) <= EstimateRequestTokens(one) {
		t.Fatalf("estimate of two parts = %d, must exceed one part = %d",
			EstimateRequestTokens(two), EstimateRequestTokens(one))
	}
	// Splitting the same text across parts must not change what it weighs: the
	// estimate is about content, not about how the host chose to slice it.
	whole := Request{Messages: []Message{TextMessage("user", "firstsecond")}}
	if got, want := EstimateRequestTokens(two), EstimateRequestTokens(whole); got != want {
		t.Errorf("estimate of split text = %d, want the same as the whole = %d", got, want)
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

// An image part carries its bytes in Data, not Text. An estimate that only
// walked Text counted a screenshot as nothing, so a session of pasted images
// never reached the preventive threshold and overflowed on the provider instead.
func TestEstimateRequestTokens_CountsImagePartsThatCarryNoText(t *testing.T) {
	empty := Request{Messages: []Message{{Role: "user", Parts: []Part{{Kind: TextPart, Text: "look"}}}}}
	withImage := Request{Messages: []Message{{Role: "user", Parts: []Part{
		{Kind: TextPart, Text: "look"},
		{Kind: ImagePart, MediaType: "image/png", Data: make([]byte, 256*1024)},
	}}}}

	if EstimateRequestTokens(withImage) <= EstimateRequestTokens(empty) {
		t.Fatalf("estimate with an image = %d, must exceed the same request without it = %d",
			EstimateRequestTokens(withImage), EstimateRequestTokens(empty))
	}
}

// What an image costs is bounded: providers price it by the tiles it covers and
// cap it. Charging its transfer size instead would bill a 4 MiB screenshot as
// hundreds of thousands of tokens and compact a session nowhere near its window.
func TestEstimateRequestTokens_ImageCostIsBounded(t *testing.T) {
	image := func(size int) Request {
		return Request{Messages: []Message{{Role: "user", Parts: []Part{
			{Kind: ImagePart, MediaType: "image/png", Data: make([]byte, size)},
		}}}}
	}

	if got := EstimateRequestTokens(image(4 * 1024 * 1024)); got > maxImageTokens+64 {
		t.Fatalf("estimate of a 4 MiB image = %d, want it capped near %d", got, maxImageTokens)
	}
	// A thumbnail still costs its base tile rather than rounding down to nothing.
	if got := EstimateRequestTokens(image(512)); got < minImageTokens {
		t.Fatalf("estimate of a small image = %d, want at least %d", got, minImageTokens)
	}
	// Bigger images never cost less than smaller ones.
	if EstimateRequestTokens(image(1024*1024)) < EstimateRequestTokens(image(64*1024)) {
		t.Fatal("a larger image must not estimate below a smaller one")
	}
}

// TestProjectRequestTokens_AnchorsOnTheReportedCountAndPricesOnlyTheDelta is the
// point of the projection: the estimator's bias applies to the whole prompt, so
// pricing an unchanged history with it is what produced spurious compactions.
// Anchoring on the reported count leaves only the newly added bytes estimated.
func TestProjectRequestTokens_AnchorsOnTheReportedCountAndPricesOnlyTheDelta(t *testing.T) {
	previous := Request{Messages: []Message{TextMessage("user", strings.Repeat("x", 30_000))}}
	// The provider priced that same prompt well below the estimate, as real
	// adapters do: the estimator divides bytes by 3 and runs ~24% high.
	observed := TokenObservation{EstimatedTokens: EstimateRequestTokens(previous), ReportedTokens: 8_000}

	grown := previous
	grown.Messages = append(append([]Message{}, previous.Messages...), TextMessage("user", strings.Repeat("y", 3_000)))
	delta := EstimateRequestTokens(grown) - observed.EstimatedTokens

	if got, want := ProjectRequestTokens(grown, observed), 8_000+delta; got != want {
		t.Errorf("ProjectRequestTokens = %d, want the reported anchor plus the estimated delta %d", got, want)
	}
	// The whole point: the projection must stay near the provider's scale rather
	// than the estimator's inflated one.
	if projected := ProjectRequestTokens(grown, observed); projected >= EstimateRequestTokens(grown) {
		t.Errorf("projection %d must sit below the raw estimate %d it corrects", projected, EstimateRequestTokens(grown))
	}
}

// TestProjectRequestTokens_ReservedOutputSurvivesTheAnchor: the observation is
// prompt-only, so a request that reserves output must carry that reserve into the
// projection. Cancelling it would under-count every turn by its own output.
func TestProjectRequestTokens_ReservedOutputSurvivesTheAnchor(t *testing.T) {
	req := Request{Messages: []Message{TextMessage("user", strings.Repeat("x", 9_000))}}
	observed := TokenObservation{EstimatedTokens: EstimateRequestTokens(req), ReportedTokens: 2_400}

	reserved := req
	reserved.MaxOutputTokens = 4_096
	got := ProjectRequestTokens(reserved, observed)
	if want := 2_400 + 4_096; got != want {
		t.Errorf("ProjectRequestTokens with a reserve = %d, want %d (anchor plus the full reserve)", got, want)
	}
}

// TestProjectRequestTokens_FallsBackToTheEstimateWithoutAUsableObservation: no
// completed turn, or a provider that reported nothing, must leave the previous
// behavior intact rather than projecting from a zero anchor — which would read as
// a nearly empty context and never compact.
func TestProjectRequestTokens_FallsBackToTheEstimateWithoutAUsableObservation(t *testing.T) {
	req := Request{Messages: []Message{TextMessage("user", strings.Repeat("x", 60_000))}}
	estimate := EstimateRequestTokens(req)

	for name, observed := range map[string]TokenObservation{
		"no turn completed yet":  {},
		"provider reported none": {EstimatedTokens: 12_000, ReportedTokens: 0},
		"estimate missing":       {EstimatedTokens: 0, ReportedTokens: 12_000},
		"negative counts":        {EstimatedTokens: -5, ReportedTokens: -5},
	} {
		if got := ProjectRequestTokens(req, observed); got != estimate {
			t.Errorf("%s: ProjectRequestTokens = %d, want the raw estimate %d", name, got, estimate)
		}
	}
}

// TestProjectRequestTokens_ShrunkContextFallsBackToTheEstimate: compaction and
// epoch rebuilds shrink the request below what the anchor priced, which drives
// the delta negative. The estimate is the only term that still describes the
// request being sent, so a nonsensical projection must never be returned.
func TestProjectRequestTokens_ShrunkContextFallsBackToTheEstimate(t *testing.T) {
	before := Request{Messages: []Message{TextMessage("user", strings.Repeat("x", 300_000))}}
	observed := TokenObservation{EstimatedTokens: EstimateRequestTokens(before), ReportedTokens: 80_000}

	compacted := Request{Messages: []Message{TextMessage("user", "a short summary")}}
	got := ProjectRequestTokens(compacted, observed)
	if want := EstimateRequestTokens(compacted); got != want {
		t.Errorf("ProjectRequestTokens after a shrink = %d, want the estimate %d", got, want)
	}
	if got <= 0 {
		t.Errorf("ProjectRequestTokens = %d, want a positive size for a real request", got)
	}
}

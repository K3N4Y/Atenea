package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func validSummary() StructuredSummary {
	return StructuredSummary{
		CurrentGoal: "compact the agent context",
		Constraints: []string{"keep the event log"},
		Decisions:   []string{"use durable checkpoints"},
		Completed:   []string{},
		Files:       []string{"internal/session/store.go"},
		ToolResults: []string{},
		Failures:    []string{},
		Pending:     []string{"implement stores"},
		Invariants:  []string{"tool calls stay paired"},
	}
}

func TestStructuredSummary_ValidateRequiresEveryJSONField(t *testing.T) {
	raw := []byte(`{"current_goal":"x"}`)
	var summary StructuredSummary
	err := DecodeStructuredSummary(raw, &summary)
	if err == nil {
		t.Fatal("partial summary must fail")
	}
	if !errors.Is(err, ErrInvalidSummary) {
		t.Fatalf("error = %v, want ErrInvalidSummary", err)
	}
}

func TestStructuredSummary_RejectsEmptyGoal(t *testing.T) {
	summary := validSummary()
	summary.CurrentGoal = ""
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}

	err = DecodeStructuredSummary(raw, &StructuredSummary{})
	if !errors.Is(err, ErrInvalidSummary) {
		t.Fatalf("error = %v, want ErrInvalidSummary", err)
	}
}

func TestStructuredSummary_RejectsWhitespaceOnlyGoal(t *testing.T) {
	summary := validSummary()
	summary.CurrentGoal = " \n\t "
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}

	err = DecodeStructuredSummary(raw, &StructuredSummary{})
	if !errors.Is(err, ErrInvalidSummary) {
		t.Fatalf("error = %v, want ErrInvalidSummary", err)
	}
}

func TestStructuredSummary_TrimsGoal(t *testing.T) {
	summary := validSummary()
	summary.CurrentGoal = "  compact the agent context \n"
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}

	var got StructuredSummary
	if err := DecodeStructuredSummary(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.CurrentGoal != "compact the agent context" {
		t.Fatalf("current goal = %q", got.CurrentGoal)
	}
}

func TestStructuredSummary_RejectsUnknownFields(t *testing.T) {
	raw := validSummaryJSON(t)
	raw = append(raw[:len(raw)-1], []byte(`,"unexpected":true}`)...)

	err := DecodeStructuredSummary(raw, &StructuredSummary{})
	if !errors.Is(err, ErrInvalidSummary) {
		t.Fatalf("error = %v, want ErrInvalidSummary", err)
	}
}

func TestStructuredSummary_RejectsDuplicateFields(t *testing.T) {
	raw := validSummaryJSON(t)
	raw = append(raw[:len(raw)-1], []byte(`,"current_goal":"replacement"}`)...)

	err := DecodeStructuredSummary(raw, &StructuredSummary{})
	if !errors.Is(err, ErrInvalidSummary) {
		t.Fatalf("error = %v, want ErrInvalidSummary", err)
	}
}

func TestStructuredSummary_RequiresArraysForListFields(t *testing.T) {
	listFields := []string{
		"constraints_and_instructions",
		"decisions",
		"completed_work",
		"files_and_changes",
		"relevant_tool_results",
		"failures_and_attempts",
		"pending_and_next_step",
		"facts_not_to_reinterpret",
	}
	for _, field := range listFields {
		t.Run(field, func(t *testing.T) {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(validSummaryJSON(t), &fields); err != nil {
				t.Fatal(err)
			}
			fields[field] = json.RawMessage("null")
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}

			err = DecodeStructuredSummary(raw, &StructuredSummary{})
			if !errors.Is(err, ErrInvalidSummary) {
				t.Fatalf("error = %v, want ErrInvalidSummary", err)
			}
		})
	}
}

func TestStructuredSummary_DoesNotMutateOutputOnError(t *testing.T) {
	want := validSummary()
	got := want
	invalid := validSummary()
	invalid.CurrentGoal = "   "
	raw, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}

	if err := DecodeStructuredSummary(raw, &got); err == nil {
		t.Fatal("invalid summary must fail")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("output changed to %+v, want %+v", got, want)
	}
}

func TestStructuredSummary_RejectsNilOutput(t *testing.T) {
	raw, err := json.Marshal(validSummary())
	if err != nil {
		t.Fatal(err)
	}

	err = DecodeStructuredSummary(raw, nil)
	if !errors.Is(err, ErrInvalidSummary) {
		t.Fatalf("error = %v, want ErrInvalidSummary", err)
	}
}

func TestStructuredSummary_RoundTripsAllFields(t *testing.T) {
	want := validSummary()
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}

	var got StructuredSummary
	if err := DecodeStructuredSummary(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestSessionEvent_CompactionPayloadRoundTripsJSON(t *testing.T) {
	want := SessionEvent{
		Kind: KindContextCompacted,
		Compaction: &CompactionCheckpoint{
			Summary:              validSummary(),
			ExpectedEpoch:        ContextEpoch{BaselineSeq: 4, Revision: 2},
			CoveredThroughSeq:    10,
			AnchorUserSeq:        11,
			PreservedFromSeq:     11,
			Model:                "claude-sonnet-4-5",
			Reason:               CompactionPreventive,
			InputTokensBefore:    160000,
			EstimatedTokensAfter: 32000,
		},
	}

	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"Compaction":`) {
		t.Fatalf("serialized event lacks Compaction key: %s", raw)
	}
	if strings.Contains(string(raw), `"compaction":`) {
		t.Fatalf("serialized event contains lowercase compaction key: %s", raw)
	}
	var got SessionEvent
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Kind != want.Kind {
		t.Fatalf("kind = %q, want %q", got.Kind, want.Kind)
	}
	if !reflect.DeepEqual(got.Compaction, want.Compaction) {
		t.Fatalf("compaction = %+v, want %+v", got.Compaction, want.Compaction)
	}
}

func TestErrCompactionConflict_IsStableDomainError(t *testing.T) {
	err := fmt.Errorf("commit checkpoint: %w", ErrCompactionConflict)
	if !errors.Is(err, ErrCompactionConflict) {
		t.Fatal("conflict must support errors.Is")
	}
}

func validSummaryJSON(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(validSummary())
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

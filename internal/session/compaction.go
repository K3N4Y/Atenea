package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

var (
	ErrCompactionConflict   = errors.New("compaction checkpoint conflicts with current epoch")
	ErrNoCompactableHistory = errors.New("no compactable history before current activity")
	ErrActivityTooLarge     = errors.New("current user activity does not fit the model context")
	ErrInvalidSummary       = errors.New("invalid structured compaction summary")
)

type CompactionReason string

const (
	CompactionPreventive CompactionReason = "preventive"
	CompactionOverflow   CompactionReason = "overflow"
)

type StructuredSummary struct {
	CurrentGoal string   `json:"current_goal"`
	Constraints []string `json:"constraints_and_instructions"`
	Decisions   []string `json:"decisions"`
	Completed   []string `json:"completed_work"`
	Files       []string `json:"files_and_changes"`
	ToolResults []string `json:"relevant_tool_results"`
	Failures    []string `json:"failures_and_attempts"`
	Pending     []string `json:"pending_and_next_step"`
	Invariants  []string `json:"facts_not_to_reinterpret"`
}

var summaryFields = map[string]bool{
	"current_goal":                 false,
	"constraints_and_instructions": true,
	"decisions":                    true,
	"completed_work":               true,
	"files_and_changes":            true,
	"relevant_tool_results":        true,
	"failures_and_attempts":        true,
	"pending_and_next_step":        true,
	"facts_not_to_reinterpret":     true,
}

func DecodeStructuredSummary(raw []byte, out *StructuredSummary) error {
	if out == nil {
		return fmt.Errorf("%w: output is nil", ErrInvalidSummary)
	}
	fields, err := decodeSummaryFields(raw)
	if err != nil {
		return err
	}

	var decoded StructuredSummary
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSummary, err)
	}
	decoded.CurrentGoal = strings.TrimSpace(decoded.CurrentGoal)
	if decoded.CurrentGoal == "" {
		return fmt.Errorf("%w: current_goal is empty", ErrInvalidSummary)
	}
	for name, isList := range summaryFields {
		if isList && bytes.Equal(bytes.TrimSpace(fields[name]), []byte("null")) {
			return fmt.Errorf("%w: %s must be an array", ErrInvalidSummary, name)
		}
	}
	*out = decoded
	return nil
}

func decodeSummaryFields(raw []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSummary, err)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, fmt.Errorf("%w: summary must be an object", ErrInvalidSummary)
	}

	fields := make(map[string]json.RawMessage, len(summaryFields))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidSummary, err)
		}
		name, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("%w: field name is not a string", ErrInvalidSummary)
		}
		if _, known := summaryFields[name]; !known {
			return nil, fmt.Errorf("%w: unknown field %s", ErrInvalidSummary, name)
		}
		if _, duplicate := fields[name]; duplicate {
			return nil, fmt.Errorf("%w: duplicate field %s", ErrInvalidSummary, name)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidSummary, err)
		}
		fields[name] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSummary, err)
	}
	if token, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidSummary, err)
		}
		return nil, fmt.Errorf("%w: unexpected token %v", ErrInvalidSummary, token)
	}
	for name := range summaryFields {
		if _, ok := fields[name]; !ok {
			return nil, fmt.Errorf("%w: missing %s", ErrInvalidSummary, name)
		}
	}
	return fields, nil
}

type CompactionCheckpoint struct {
	Summary              StructuredSummary `json:"summary"`
	ExpectedEpoch        ContextEpoch      `json:"expected_epoch"`
	CoveredThroughSeq    Seq               `json:"covered_through_seq"`
	AnchorUserSeq        Seq               `json:"anchor_user_seq"`
	PreservedFromSeq     Seq               `json:"preserved_from_seq"`
	Model                string            `json:"model"`
	Reason               CompactionReason  `json:"reason"`
	InputTokensBefore    int               `json:"input_tokens_before"`
	EstimatedTokensAfter int               `json:"estimated_tokens_after"`
}

type RunnerContext struct {
	Epoch      ContextEpoch
	Checkpoint *CompactionCheckpoint
	Anchor     *Message
	Messages   []Message
}

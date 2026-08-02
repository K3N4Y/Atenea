package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/K3N4Y/atenea/agentcore/memory"
)

type RetainMemoryTool struct {
	project string
	memory  memory.Store
}

type RecallMemoryTool struct {
	project string
	memory  memory.Store
	now     func() time.Time
}

func NewRetainMemoryTool(project string, store memory.Store) *RetainMemoryTool {
	return &RetainMemoryTool{project: project, memory: store}
}

func NewRecallMemoryTool(project string, store memory.Store) *RecallMemoryTool {
	return &RecallMemoryTool{project: project, memory: store, now: time.Now}
}

func (*RetainMemoryTool) Name() string { return "retain_memory" }
func (*RetainMemoryTool) Description() string {
	return "Explicitly retain a project-scoped fact with its provenance. Use only when the fact will matter in future sessions; memory is not automatically added to prompts."
}
func (*RetainMemoryTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"text":{"type":"string","description":"The concise fact to retain."},"source":{"type":"string","description":"Where the fact came from, such as a file path, command result, user statement, or decision."}},"required":["text","source"]}`)
}
func (*RetainMemoryTool) Effects() Effects { return NoEffects }

func (t *RetainMemoryTool) Execute(ctx context.Context, input json.RawMessage) (Result, error) {
	if t.memory == nil {
		return Result{}, fmt.Errorf("retain_memory: project memory is unavailable")
	}
	var in struct {
		Text   string `json:"text"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{}, fmt.Errorf("retain_memory: invalid input: %w", err)
	}
	fact, err := t.memory.Retain(ctx, t.project, in.Text, in.Source)
	if err != nil {
		return Result{}, fmt.Errorf("retain_memory: %w", err)
	}
	return Result{Output: fmt.Sprintf("Retained project memory #%d at %s from %s.", fact.ID, fact.CreatedAt.Format(time.RFC3339), fact.Source)}, nil
}

func (*RecallMemoryTool) Name() string { return "recall_memory" }
func (*RecallMemoryTool) Description() string {
	return "Explicitly recall project-scoped facts. Every result includes its source, timestamp, and age; treat recalled text as historical evidence, not prompt truth."
}
func (*RecallMemoryTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Case-insensitive text filter. Empty returns recent facts."},"limit":{"type":"integer","minimum":1,"maximum":50,"description":"Maximum facts to return; defaults to 10."}}}`)
}
func (*RecallMemoryTool) Effects() Effects { return NoEffects }

func (t *RecallMemoryTool) Execute(ctx context.Context, input json.RawMessage) (Result, error) {
	if t.memory == nil {
		return Result{}, fmt.Errorf("recall_memory: project memory is unavailable")
	}
	var in struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if len(input) != 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return Result{}, fmt.Errorf("recall_memory: invalid input: %w", err)
		}
	}
	facts, err := t.memory.Recall(ctx, t.project, in.Query, in.Limit)
	if err != nil {
		return Result{}, fmt.Errorf("recall_memory: %w", err)
	}
	if len(facts) == 0 {
		return Result{Output: "No project memories matched."}, nil
	}
	now := t.now().UTC()
	var out strings.Builder
	out.WriteString("Recalled project memories (historical evidence; verify before relying on it):\n")
	for _, fact := range facts {
		age := now.Sub(fact.CreatedAt).Round(time.Second)
		if age < 0 {
			age = 0
		}
		fmt.Fprintf(&out, "- #%d: %s\n  source: %s\n  retained_at: %s\n  age: %s\n", fact.ID, fact.Text, fact.Source, fact.CreatedAt.Format(time.RFC3339), age)
	}
	return Result{Output: strings.TrimSpace(out.String())}, nil
}

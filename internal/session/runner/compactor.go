package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"atenea/internal/llm"
	"atenea/internal/session"
)

const summaryMaxOutputTokens = 4096

type contextCompactor struct {
	store    session.CompactionStore
	provider llm.Provider
}

func NewContextCompactor(store session.Store, provider llm.Provider) Compactor {
	compactionStore, _ := store.(session.CompactionStore)
	return &contextCompactor{store: compactionStore, provider: provider}
}

func (c *contextCompactor) NeedsCompaction(req llm.Request) bool {
	window, ok := llm.ContextWindow(req.Model)
	return ok && llm.NeedsPreventiveCompaction(llm.EstimateRequestTokens(req), window)
}

func (c *contextCompactor) Compact(ctx context.Context, sessionID string) error {
	if c.store == nil {
		return errors.New("store does not support context compaction")
	}
	runnerContext, err := c.store.ContextForRunner(ctx, sessionID)
	if err != nil {
		return err
	}
	anchorIndex := lastUserIndex(runnerContext.Messages)
	if anchorIndex <= 0 {
		return session.ErrNoCompactableHistory
	}

	model := runnerContext.Epoch.Model
	providerSnapshot := llm.Acquire(c.provider)
	if providerSnapshot.Model != "" {
		model = providerSnapshot.Model
	}
	if model == "" {
		model = "unknown"
	}

	summary, err := c.generateSummary(ctx, providerSnapshot.Provider, model, runnerContext, runnerContext.Messages[:anchorIndex])
	if err != nil {
		return err
	}
	anchor := runnerContext.Messages[anchorIndex]
	covered := runnerContext.Messages[anchorIndex-1].Seq
	before := estimateCompactionTokens(model, runnerContext.Checkpoint, runnerContext.Messages)
	after := estimateCompactionTokens(model, &session.CompactionCheckpoint{Summary: summary}, runnerContext.Messages[anchorIndex:])
	if before <= after {
		before = after + 1
	}
	checkpoint := session.CompactionCheckpoint{
		Summary:              summary,
		ExpectedEpoch:        runnerContext.Epoch,
		CoveredThroughSeq:    covered,
		AnchorUserSeq:        anchor.Seq,
		PreservedFromSeq:     anchor.Seq,
		Model:                model,
		Reason:               session.CompactionPreventive,
		InputTokensBefore:    before,
		EstimatedTokensAfter: after,
	}
	_, err = c.store.CommitCompaction(ctx, sessionID, checkpoint)
	return err
}

func lastUserIndex(messages []session.Message) int {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == session.RoleUser {
			return index
		}
	}
	return -1
}

func (c *contextCompactor) generateSummary(ctx context.Context, provider llm.Provider, model string, runnerContext session.RunnerContext, messages []session.Message) (session.StructuredSummary, error) {
	if provider == nil {
		provider = c.provider
	}
	payload := struct {
		Previous *session.StructuredSummary `json:"previous_summary,omitempty"`
		Messages []session.Message          `json:"messages"`
	}{Messages: messages}
	if runnerContext.Checkpoint != nil {
		payload.Previous = &runnerContext.Checkpoint.Summary
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return session.StructuredSummary{}, err
	}
	req := llm.Request{
		Model:           model,
		System:          "Summarize the completed conversation prefix as one JSON object. Use exactly these keys: current_goal, constraints_and_instructions, decisions, completed_work, files_and_changes, relevant_tool_results, failures_and_attempts, pending_and_next_step, facts_not_to_reinterpret. current_goal must be a non-empty string and every other field must be an array of strings. Return JSON only.",
		Messages:        []llm.Message{{Role: "user", Text: string(encoded)}},
		MaxOutputTokens: summaryMaxOutputTokens,
	}
	stream, err := provider.Stream(ctx, req)
	if err != nil {
		return session.StructuredSummary{}, err
	}
	var text strings.Builder
	ended := false
	for event := range stream {
		switch event.Kind {
		case llm.TextDelta:
			text.WriteString(event.Text)
		case llm.StepFailed:
			if event.Err != nil {
				return session.StructuredSummary{}, event.Err
			}
			return session.StructuredSummary{}, fmt.Errorf("summary provider failed: %s", event.Text)
		case llm.StepEnded:
			ended = true
		}
	}
	if err := ctx.Err(); err != nil {
		return session.StructuredSummary{}, err
	}
	if !ended {
		return session.StructuredSummary{}, io.ErrUnexpectedEOF
	}
	var summary session.StructuredSummary
	if err := session.DecodeStructuredSummary([]byte(text.String()), &summary); err != nil {
		return session.StructuredSummary{}, err
	}
	return summary, nil
}

func estimateCompactionTokens(model string, checkpoint *session.CompactionCheckpoint, messages []session.Message) int {
	req := llm.Request{Model: model, Messages: toLLMMessages(messages)}
	if checkpoint != nil {
		req.System = renderCompactedSystem("", checkpoint.Summary)
	}
	return llm.EstimateRequestTokens(req)
}

func renderCompactedSystem(base string, summary session.StructuredSummary) string {
	encoded, _ := json.MarshalIndent(summary, "", "  ")
	block := "<COMPACTED_SESSION_CONTEXT>\n" + string(encoded) + "\n</COMPACTED_SESSION_CONTEXT>"
	if strings.TrimSpace(base) == "" {
		return block
	}
	return base + "\n\n" + block
}

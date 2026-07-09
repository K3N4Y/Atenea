package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type compactionStoreFactory func(t *testing.T) CompactionStore

func runCompactionStoreContract(t *testing.T, factory compactionStoreFactory) {
	t.Helper()

	t.Run("commit advances epoch and projects summary", func(t *testing.T) {
		store := factory(t)
		ctx := context.Background()
		userSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "first"})
		assistantSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "a1", Role: RoleAssistant, Text: "done"})
		anchorSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u2", Role: RoleUser, Text: "current"})
		epoch := compactionEpoch(t, store, ctx, "s1")

		checkpoint := CompactionCheckpoint{
			Summary:           validSummary(),
			ExpectedEpoch:     epoch,
			CoveredThroughSeq: assistantSeq,
			AnchorUserSeq:     anchorSeq,
			PreservedFromSeq:  anchorSeq,
			Reason:            CompactionPreventive,
		}
		seq, err := store.CommitCompaction(ctx, "s1", checkpoint)
		if err != nil {
			t.Fatalf("CommitCompaction: %v", err)
		}
		if seq <= anchorSeq || userSeq == 0 {
			t.Fatalf("checkpoint seq = %d, anchor = %d", seq, anchorSeq)
		}

		got, err := store.ContextForRunner(ctx, "s1")
		if err != nil {
			t.Fatalf("ContextForRunner: %v", err)
		}
		if got.Epoch.BaselineSeq != assistantSeq || got.Epoch.Revision != epoch.Revision+1 {
			t.Fatalf("epoch = %+v", got.Epoch)
		}
		if got.Checkpoint == nil || got.Checkpoint.Summary.CurrentGoal == "" {
			t.Fatalf("checkpoint = %+v", got.Checkpoint)
		}
		events, err := store.Events(ctx, "s1", 0)
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		last := events[len(events)-1]
		if last.Kind != KindContextCompacted || last.Compaction == nil || last.Compaction.CoveredThroughSeq != assistantSeq {
			t.Fatalf("durable compaction event = %+v", last)
		}
		if len(got.Messages) != 1 || got.Messages[0].Seq != anchorSeq {
			t.Fatalf("messages = %+v", got.Messages)
		}
	})

	t.Run("stale epoch is atomic conflict", func(t *testing.T) {
		store := factory(t)
		ctx := context.Background()
		first := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "one"})
		preserved := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "a1", Role: RoleAssistant, Text: "two"})
		epoch := compactionEpoch(t, store, ctx, "s1")
		checkpoint := CompactionCheckpoint{
			Summary:           validSummary(),
			ExpectedEpoch:     epoch,
			CoveredThroughSeq: first,
			AnchorUserSeq:     first,
			PreservedFromSeq:  preserved,
			Reason:            CompactionPreventive,
		}
		if _, err := store.CommitCompaction(ctx, "s1", checkpoint); err != nil {
			t.Fatal(err)
		}
		before, err := store.Events(ctx, "s1", 0)
		if err != nil {
			t.Fatal(err)
		}
		beforeContext, err := store.ContextForRunner(ctx, "s1")
		if err != nil {
			t.Fatal(err)
		}

		if _, err := store.CommitCompaction(ctx, "s1", checkpoint); !errors.Is(err, ErrCompactionConflict) {
			t.Fatalf("second commit error = %v", err)
		}
		after, err := store.Events(ctx, "s1", 0)
		if err != nil {
			t.Fatal(err)
		}
		afterContext, err := store.ContextForRunner(ctx, "s1")
		if err != nil {
			t.Fatal(err)
		}
		if len(after) != len(before) {
			t.Fatalf("conflict appended event: before=%d after=%d", len(before), len(after))
		}
		if afterContext.Epoch != beforeContext.Epoch || afterContext.Checkpoint.CoveredThroughSeq != beforeContext.Checkpoint.CoveredThroughSeq {
			t.Fatalf("conflict mutated checkpoint: before=%+v after=%+v", beforeContext, afterContext)
		}
	})

	t.Run("successive checkpoints replace projection", func(t *testing.T) {
		store := factory(t)
		ctx := context.Background()
		first := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "first"})
		second := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "a1", Role: RoleAssistant, Text: "second"})
		epoch := compactionEpoch(t, store, ctx, "s1")
		firstCheckpoint := CompactionCheckpoint{
			Summary:           validSummary(),
			ExpectedEpoch:     epoch,
			CoveredThroughSeq: first,
			AnchorUserSeq:     first,
			PreservedFromSeq:  second,
			Reason:            CompactionPreventive,
		}
		if _, err := store.CommitCompaction(ctx, "s1", firstCheckpoint); err != nil {
			t.Fatal(err)
		}

		third := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u2", Role: RoleUser, Text: "third"})
		epoch = compactionEpoch(t, store, ctx, "s1")
		secondSummary := validSummary()
		secondSummary.CurrentGoal = "continue after another checkpoint"
		secondCheckpoint := CompactionCheckpoint{
			Summary:           secondSummary,
			ExpectedEpoch:     epoch,
			CoveredThroughSeq: second,
			AnchorUserSeq:     third,
			PreservedFromSeq:  third,
			Reason:            CompactionPreventive,
		}
		if _, err := store.CommitCompaction(ctx, "s1", secondCheckpoint); err != nil {
			t.Fatal(err)
		}

		got, err := store.ContextForRunner(ctx, "s1")
		if err != nil {
			t.Fatal(err)
		}
		if got.Epoch.BaselineSeq != second || got.Epoch.Revision != 2 {
			t.Fatalf("epoch = %+v", got.Epoch)
		}
		if got.Checkpoint == nil || got.Checkpoint.Summary.CurrentGoal != secondSummary.CurrentGoal {
			t.Fatalf("checkpoint = %+v", got.Checkpoint)
		}
		if len(got.Messages) != 1 || got.Messages[0].Seq != third {
			t.Fatalf("messages = %+v", got.Messages)
		}
	})

	t.Run("fallback rehydrates anchor before preserved suffix", func(t *testing.T) {
		store := factory(t)
		ctx := context.Background()
		oldSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "old"})
		anchorSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u2", Role: RoleUser, Text: "current"})
		coveredSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "a1", Role: RoleAssistant, Text: "covered"})
		discardedSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "a2", Role: RoleAssistant, Text: "summarized middle"})
		preservedSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "a3", Role: RoleAssistant, Text: "recent suffix"})
		epoch := compactionEpoch(t, store, ctx, "s1")
		_, err := store.CommitCompaction(ctx, "s1", CompactionCheckpoint{
			Summary:           validSummary(),
			ExpectedEpoch:     epoch,
			CoveredThroughSeq: coveredSeq,
			AnchorUserSeq:     anchorSeq,
			PreservedFromSeq:  preservedSeq,
			Reason:            CompactionPreventive,
		})
		if err != nil {
			t.Fatal(err)
		}
		got, err := store.ContextForRunner(ctx, "s1")
		if err != nil {
			t.Fatal(err)
		}
		if oldSeq == 0 {
			t.Fatal("seed failed")
		}
		if got.Anchor == nil || got.Anchor.Seq != anchorSeq || got.Anchor.Text != "current" {
			t.Fatalf("anchor = %+v", got.Anchor)
		}
		if len(got.Messages) != 1 || got.Messages[0].Seq != preservedSeq {
			t.Fatalf("preserved suffix = %+v; discarded seq = %d", got.Messages, discardedSeq)
		}
	})

	t.Run("anchor at preserved suffix remains in messages", func(t *testing.T) {
		store := factory(t)
		ctx := context.Background()
		coveredSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "old"})
		anchorSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u2", Role: RoleUser, Text: "current"})
		epoch := compactionEpoch(t, store, ctx, "s1")
		_, err := store.CommitCompaction(ctx, "s1", CompactionCheckpoint{
			Summary:           validSummary(),
			ExpectedEpoch:     epoch,
			CoveredThroughSeq: coveredSeq,
			AnchorUserSeq:     anchorSeq,
			PreservedFromSeq:  anchorSeq,
			Reason:            CompactionPreventive,
		})
		if err != nil {
			t.Fatal(err)
		}
		got, err := store.ContextForRunner(ctx, "s1")
		if err != nil {
			t.Fatal(err)
		}
		if got.Anchor != nil {
			t.Fatalf("anchor = %+v, want nil", got.Anchor)
		}
		if len(got.Messages) != 1 || got.Messages[0].Seq != anchorSeq || got.Messages[0].Role != RoleUser {
			t.Fatalf("messages = %+v", got.Messages)
		}
	})

	t.Run("invalid checkpoint references are atomic conflicts", func(t *testing.T) {
		tests := []struct {
			name       string
			checkpoint func(epoch ContextEpoch, userSeq, assistantSeq, preservedSeq, nonMessageSeq, tailSeq Seq) CompactionCheckpoint
		}{
			{
				name: "anchor is not materialized",
				checkpoint: func(epoch ContextEpoch, userSeq, assistantSeq, preservedSeq, nonMessageSeq, tailSeq Seq) CompactionCheckpoint {
					return compactionCheckpoint(epoch, userSeq, nonMessageSeq, tailSeq)
				},
			},
			{
				name: "anchor is not a user message",
				checkpoint: func(epoch ContextEpoch, userSeq, assistantSeq, preservedSeq, nonMessageSeq, tailSeq Seq) CompactionCheckpoint {
					return compactionCheckpoint(epoch, userSeq, assistantSeq, preservedSeq)
				},
			},
			{
				name: "preserved seq is not materialized",
				checkpoint: func(epoch ContextEpoch, userSeq, assistantSeq, preservedSeq, nonMessageSeq, tailSeq Seq) CompactionCheckpoint {
					return compactionCheckpoint(epoch, userSeq, userSeq, nonMessageSeq)
				},
			},
			{
				name: "covered seq reaches preserved seq",
				checkpoint: func(epoch ContextEpoch, userSeq, assistantSeq, preservedSeq, nonMessageSeq, tailSeq Seq) CompactionCheckpoint {
					return compactionCheckpoint(epoch, preservedSeq, userSeq, preservedSeq)
				},
			},
			{
				name: "anchor follows preserved seq",
				checkpoint: func(epoch ContextEpoch, userSeq, assistantSeq, preservedSeq, nonMessageSeq, tailSeq Seq) CompactionCheckpoint {
					return compactionCheckpoint(epoch, userSeq, preservedSeq, assistantSeq)
				},
			},
		}

		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				store := factory(t)
				ctx := context.Background()
				userSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "current"})
				assistantSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "a1", Role: RoleAssistant, Text: "middle"})
				preservedSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "a2", Role: RoleAssistant, Text: "suffix"})
				nonMessageSeq, err := store.AppendEvent(ctx, "s1", SessionEvent{Kind: KindSessionTitle, Text: "not a message"})
				if err != nil {
					t.Fatal(err)
				}
				tailSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "a3", Role: RoleAssistant, Text: "tail"})
				epoch := compactionEpoch(t, store, ctx, "s1")
				beforeEvents, err := store.Events(ctx, "s1", 0)
				if err != nil {
					t.Fatal(err)
				}
				beforeContext, err := store.ContextForRunner(ctx, "s1")
				if err != nil {
					t.Fatal(err)
				}

				_, err = store.CommitCompaction(ctx, "s1", test.checkpoint(epoch, userSeq, assistantSeq, preservedSeq, nonMessageSeq, tailSeq))
				if !errors.Is(err, ErrCompactionConflict) {
					t.Fatalf("CommitCompaction error = %v, want ErrCompactionConflict", err)
				}

				afterEvents, err := store.Events(ctx, "s1", 0)
				if err != nil {
					t.Fatal(err)
				}
				afterContext, err := store.ContextForRunner(ctx, "s1")
				if err != nil {
					t.Fatal(err)
				}
				if len(afterEvents) != len(beforeEvents) || afterContext.Epoch != beforeContext.Epoch || afterContext.Checkpoint != nil {
					t.Fatalf("invalid commit mutated state: events %d -> %d, context %+v -> %+v", len(beforeEvents), len(afterEvents), beforeContext, afterContext)
				}
			})
		}
	})

	t.Run("anchor must be latest user through preserved activity", func(t *testing.T) {
		store := factory(t)
		ctx := context.Background()
		oldUserSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "old activity"})
		coveredSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "a1", Role: RoleAssistant, Text: "covered"})
		latestUserSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u2", Role: RoleUser, Text: "latest activity"})
		preservedSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "a2", Role: RoleAssistant, Text: "suffix"})

		assertCompactionConflictIsAtomic(t, store, ctx, "s1", compactionCheckpoint(
			compactionEpoch(t, store, ctx, "s1"), coveredSeq, oldUserSeq, preservedSeq,
		))
		if latestUserSeq == 0 {
			t.Fatal("latest user seed failed")
		}
	})

	t.Run("preserved suffix cannot start at tool result", func(t *testing.T) {
		store := factory(t)
		ctx := context.Background()
		anchorSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "current"})
		coveredSeq := appendCompactionMessage(t, store, ctx, "s1", Message{
			ID: "a1", Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call-1", Name: "read", Arguments: `{}`}},
		})
		toolSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "call-1", Role: RoleTool, Text: "result", ToolCallID: "call-1"})

		assertCompactionConflictIsAtomic(t, store, ctx, "s1", compactionCheckpoint(
			compactionEpoch(t, store, ctx, "s1"), coveredSeq, anchorSeq, toolSeq,
		))
	})

	t.Run("preserved assistant requires complete immediate tool results", func(t *testing.T) {
		store := factory(t)
		ctx := context.Background()
		anchorSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "current"})
		assistantSeq := appendCompactionMessage(t, store, ctx, "s1", Message{
			ID: "a1", Role: RoleAssistant,
			ToolCalls: []ToolCall{{ID: "call-1", Name: "read", Arguments: `{}`}, {ID: "call-2", Name: "write", Arguments: `{}`}},
		})
		appendCompactionMessage(t, store, ctx, "s1", Message{ID: "call-1", Role: RoleTool, Text: "first", ToolCallID: "call-1"})
		appendCompactionMessage(t, store, ctx, "s1", Message{ID: "a2", Role: RoleAssistant, Text: "interrupts results"})
		appendCompactionMessage(t, store, ctx, "s1", Message{ID: "call-2", Role: RoleTool, Text: "late", ToolCallID: "call-2"})

		assertCompactionConflictIsAtomic(t, store, ctx, "s1", compactionCheckpoint(
			compactionEpoch(t, store, ctx, "s1"), anchorSeq, anchorSeq, assistantSeq,
		))
	})

	t.Run("preserved assistant accepts complete immediate tool results", func(t *testing.T) {
		store := factory(t)
		ctx := context.Background()
		anchorSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "current"})
		assistantSeq := appendCompactionMessage(t, store, ctx, "s1", Message{
			ID: "a1", Role: RoleAssistant,
			ToolCalls: []ToolCall{{ID: "call-1", Name: "read", Arguments: `{}`}, {ID: "call-2", Name: "write", Arguments: `{}`}},
		})
		firstToolSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "call-1", Role: RoleTool, Text: "first", ToolCallID: "call-1"})
		secondToolSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "call-2", Role: RoleTool, Text: "second", ToolCallID: "call-2"})

		_, err := store.CommitCompaction(ctx, "s1", compactionCheckpoint(
			compactionEpoch(t, store, ctx, "s1"), anchorSeq, anchorSeq, assistantSeq,
		))
		if err != nil {
			t.Fatalf("CommitCompaction: %v", err)
		}
		got, err := store.ContextForRunner(ctx, "s1")
		if err != nil {
			t.Fatal(err)
		}
		if got.Anchor == nil || got.Anchor.Seq != anchorSeq {
			t.Fatalf("anchor = %+v", got.Anchor)
		}
		if len(got.Messages) != 3 || got.Messages[0].Seq != assistantSeq || got.Messages[1].Seq != firstToolSeq || got.Messages[2].Seq != secondToolSeq {
			t.Fatalf("messages = %+v", got.Messages)
		}
	})

	t.Run("preserved assistant accepts immediate tool results in completion order", func(t *testing.T) {
		store := factory(t)
		ctx := context.Background()
		anchorSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "current"})
		assistantSeq := appendCompactionMessage(t, store, ctx, "s1", Message{
			ID: "a1", Role: RoleAssistant,
			ToolCalls: []ToolCall{{ID: "call-1", Name: "read", Arguments: `{}`}, {ID: "call-2", Name: "write", Arguments: `{}`}},
		})
		secondToolSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "call-2", Role: RoleTool, Text: "second", ToolCallID: "call-2"})
		firstToolSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "call-1", Role: RoleTool, Text: "first", ToolCallID: "call-1"})

		_, err := store.CommitCompaction(ctx, "s1", compactionCheckpoint(
			compactionEpoch(t, store, ctx, "s1"), anchorSeq, anchorSeq, assistantSeq,
		))
		if err != nil {
			t.Fatalf("CommitCompaction: %v", err)
		}
		got, err := store.ContextForRunner(ctx, "s1")
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Messages) != 3 || got.Messages[0].Seq != assistantSeq || got.Messages[1].Seq != secondToolSeq || got.Messages[2].Seq != firstToolSeq {
			t.Fatalf("messages = %+v", got.Messages)
		}
	})

	t.Run("anchor must be last user in entire materialized history", func(t *testing.T) {
		store := factory(t)
		ctx := context.Background()
		anchorSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "anchor"})
		preservedSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "a1", Role: RoleAssistant, Text: "preserved"})
		appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u2", Role: RoleUser, Text: "later user"})

		assertCompactionConflictIsAtomic(t, store, ctx, "s1", compactionCheckpoint(
			compactionEpoch(t, store, ctx, "s1"), anchorSeq, anchorSeq, preservedSeq,
		))
	})

	t.Run("preserved suffix rejects orphan tool result after valid messages", func(t *testing.T) {
		store := factory(t)
		ctx := context.Background()
		anchorSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "current"})
		preservedSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "a1", Role: RoleAssistant, Text: "preserved"})
		appendCompactionMessage(t, store, ctx, "s1", Message{ID: "call-orphan", Role: RoleTool, Text: "orphan", ToolCallID: "call-orphan"})

		assertCompactionConflictIsAtomic(t, store, ctx, "s1", compactionCheckpoint(
			compactionEpoch(t, store, ctx, "s1"), anchorSeq, anchorSeq, preservedSeq,
		))
	})

	t.Run("preserved suffix rejects incomplete later assistant tool group", func(t *testing.T) {
		store := factory(t)
		ctx := context.Background()
		anchorSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "current"})
		preservedSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "a1", Role: RoleAssistant, Text: "preserved"})
		appendCompactionMessage(t, store, ctx, "s1", Message{
			ID: "a2", Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call-1", Name: "read", Arguments: `{}`}},
		})
		appendCompactionMessage(t, store, ctx, "s1", Message{ID: "a3", Role: RoleAssistant, Text: "interrupts missing result"})

		assertCompactionConflictIsAtomic(t, store, ctx, "s1", compactionCheckpoint(
			compactionEpoch(t, store, ctx, "s1"), anchorSeq, anchorSeq, preservedSeq,
		))
	})

	t.Run("stored and returned values are mutation isolated", func(t *testing.T) {
		store := factory(t)
		ctx := context.Background()
		anchorMessage := Message{ID: "u1", Role: RoleUser, Text: "current"}
		anchorSeq := appendCompactionMessage(t, store, ctx, "s1", anchorMessage)
		preservedMessage := Message{
			ID: "a1", Role: RoleAssistant, Text: "preserved",
			ToolCalls: []ToolCall{{ID: "call-1", Name: "read", Arguments: `{"path":"a"}`}},
		}
		preservedEvent := SessionEvent{
			Kind: KindStepEnded, Message: &preservedMessage,
			Input: []byte(`{"input":true}`), Usage: &Usage{InputTokens: 7},
		}
		preservedSeq, err := store.AppendEvent(ctx, "s1", preservedEvent)
		if err != nil {
			t.Fatal(err)
		}
		appendCompactionMessage(t, store, ctx, "s1", Message{ID: "call-1", Role: RoleTool, Text: "result", ToolCallID: "call-1"})
		checkpoint := compactionCheckpoint(compactionEpoch(t, store, ctx, "s1"), anchorSeq, anchorSeq, preservedSeq)
		checkpoint.Summary.Constraints = []string{"original constraint"}
		if _, err := store.CommitCompaction(ctx, "s1", checkpoint); err != nil {
			t.Fatal(err)
		}

		preservedMessage.ToolCalls[0].Name = "mutated input message"
		preservedEvent.Input[0] = 'x'
		preservedEvent.Usage.InputTokens = 999
		checkpoint.Summary.Constraints[0] = "mutated input checkpoint"

		firstContext, err := store.ContextForRunner(ctx, "s1")
		if err != nil {
			t.Fatal(err)
		}
		firstEvents, err := store.Events(ctx, "s1", 0)
		if err != nil {
			t.Fatal(err)
		}
		firstContext.Checkpoint.Summary.Constraints[0] = "mutated returned checkpoint"
		firstContext.Messages[0].ToolCalls[0].Name = "mutated returned message"
		firstEvents[1].Message.ToolCalls[0].Name = "mutated returned event message"
		firstEvents[1].Input[0] = 'y'
		firstEvents[1].Usage.InputTokens = 123
		lastEvent := firstEvents[len(firstEvents)-1]
		lastEvent.Compaction.Summary.Constraints[0] = "mutated returned event checkpoint"

		secondContext, err := store.ContextForRunner(ctx, "s1")
		if err != nil {
			t.Fatal(err)
		}
		secondEvents, err := store.Events(ctx, "s1", 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := secondContext.Checkpoint.Summary.Constraints[0]; got != "original constraint" {
			t.Fatalf("checkpoint constraint = %q", got)
		}
		if got := secondContext.Messages[0].ToolCalls[0].Name; got != "read" {
			t.Fatalf("message tool name = %q", got)
		}
		if got := secondEvents[1].Message.ToolCalls[0].Name; got != "read" {
			t.Fatalf("event message tool name = %q", got)
		}
		if string(secondEvents[1].Input) != `{"input":true}` || secondEvents[1].Usage.InputTokens != 7 {
			t.Fatalf("event payload = %s usage=%+v", secondEvents[1].Input, secondEvents[1].Usage)
		}
		if got := secondEvents[len(secondEvents)-1].Compaction.Summary.Constraints[0]; got != "original constraint" {
			t.Fatalf("event checkpoint constraint = %q", got)
		}
	})

	t.Run("canceled context returns without mutation", func(t *testing.T) {
		store := factory(t)
		ctx := context.Background()
		anchorSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "current"})
		preservedSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "a1", Role: RoleAssistant, Text: "preserved"})
		checkpoint := compactionCheckpoint(compactionEpoch(t, store, ctx, "s1"), anchorSeq, anchorSeq, preservedSeq)
		before, err := store.Events(ctx, "s1", 0)
		if err != nil {
			t.Fatal(err)
		}

		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := store.ContextForRunner(canceled, "s1"); !errors.Is(err, context.Canceled) {
			t.Fatalf("ContextForRunner error = %v", err)
		}
		if _, err := store.CommitCompaction(canceled, "s1", checkpoint); !errors.Is(err, context.Canceled) {
			t.Fatalf("CommitCompaction error = %v", err)
		}
		after, err := store.Events(ctx, "s1", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(after) != len(before) {
			t.Fatalf("canceled commit appended event: before=%d after=%d", len(before), len(after))
		}
	})

	t.Run("conflict includes diagnostics and preserves sentinel", func(t *testing.T) {
		store := factory(t)
		ctx := context.Background()
		anchorSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "current"})
		preservedSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "a1", Role: RoleAssistant, Text: "preserved"})
		checkpoint := compactionCheckpoint(ContextEpoch{Revision: 99}, anchorSeq, anchorSeq, preservedSeq)
		_, err := store.CommitCompaction(ctx, "s1", checkpoint)
		if !errors.Is(err, ErrCompactionConflict) {
			t.Fatalf("error = %v", err)
		}
		if err == ErrCompactionConflict || !strings.Contains(err.Error(), "expected") || !strings.Contains(err.Error(), "current") {
			t.Fatalf("diagnostic conflict = %v", err)
		}
	})

	t.Run("same epoch concurrent commits have one winner", func(t *testing.T) {
		store := factory(t)
		ctx := context.Background()
		anchorSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "current"})
		preservedSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "a1", Role: RoleAssistant, Text: "preserved"})
		checkpoint := compactionCheckpoint(compactionEpoch(t, store, ctx, "s1"), anchorSeq, anchorSeq, preservedSeq)

		start := make(chan struct{})
		results := make(chan error, 2)
		var ready sync.WaitGroup
		ready.Add(2)
		for range 2 {
			go func() {
				ready.Done()
				<-start
				_, err := store.CommitCompaction(ctx, "s1", checkpoint)
				results <- err
			}()
		}
		ready.Wait()
		close(start)

		var successes, conflicts int
		for range 2 {
			select {
			case err := <-results:
				switch {
				case err == nil:
					successes++
				case errors.Is(err, ErrCompactionConflict):
					conflicts++
				default:
					t.Fatalf("unexpected error: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("concurrent commits timed out")
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
		}
	})

	t.Run("deep copies preserve non nil empty slices", func(t *testing.T) {
		store := factory(t)
		ctx := context.Background()
		anchorSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "current"})
		preservedSeq := appendCompactionMessage(t, store, ctx, "s1", Message{
			ID: "a1", Role: RoleAssistant, Text: "preserved", ToolCalls: []ToolCall{},
		})
		checkpoint := compactionCheckpoint(compactionEpoch(t, store, ctx, "s1"), anchorSeq, anchorSeq, preservedSeq)
		checkpoint.Summary = StructuredSummary{
			CurrentGoal: "preserve empty arrays",
			Constraints: []string{}, Decisions: []string{}, Completed: []string{}, Files: []string{},
			ToolResults: []string{}, Failures: []string{}, Pending: []string{}, Invariants: []string{},
		}
		if _, err := store.CommitCompaction(ctx, "s1", checkpoint); err != nil {
			t.Fatal(err)
		}

		got, err := store.ContextForRunner(ctx, "s1")
		if err != nil {
			t.Fatal(err)
		}
		if got.Messages[0].ToolCalls == nil {
			t.Fatal("message ToolCalls became nil")
		}
		assertSummaryArraysMarshalEmpty(t, got.Checkpoint.Summary)

		events, err := store.Events(ctx, "s1", 0)
		if err != nil {
			t.Fatal(err)
		}
		if events[1].Message.ToolCalls == nil {
			t.Fatal("event message ToolCalls became nil")
		}
		assertSummaryArraysMarshalEmpty(t, events[len(events)-1].Compaction.Summary)
	})
}

func TestMemoryStore_CompactionContract(t *testing.T) {
	runCompactionStoreContract(t, func(t *testing.T) CompactionStore {
		t.Helper()
		return NewMemoryStore()
	})
}

func TestMemoryStore_CommitCompactionChecksCancellationAfterLock(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	anchorSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "current"})
	preservedSeq := appendCompactionMessage(t, store, ctx, "s1", Message{ID: "a1", Role: RoleAssistant, Text: "preserved"})
	checkpoint := compactionCheckpoint(compactionEpoch(t, store, ctx, "s1"), anchorSeq, anchorSeq, preservedSeq)

	store.mu.Lock()
	canceled, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := store.CommitCompaction(canceled, "s1", checkpoint)
		done <- err
	}()
	cancel()
	store.mu.Unlock()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CommitCompaction error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CommitCompaction did not return after lock release")
	}
	events, err := store.Events(ctx, "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("canceled commit appended event: %+v", events)
	}
}

func TestMemoryStore_ContextForRunnerChecksCancellationAfterLock(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	appendCompactionMessage(t, store, ctx, "s1", Message{ID: "u1", Role: RoleUser, Text: "current"})

	store.mu.Lock()
	canceled, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := store.ContextForRunner(canceled, "s1")
		done <- err
	}()
	cancel()
	store.mu.Unlock()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ContextForRunner error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ContextForRunner did not return after lock release")
	}
}

func appendCompactionMessage(t *testing.T, store Store, ctx context.Context, sessionID string, message Message) Seq {
	t.Helper()
	seq, err := store.AppendEvent(ctx, sessionID, SessionEvent{Message: &message})
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	return seq
}

func compactionEpoch(t *testing.T, store Store, ctx context.Context, sessionID string) ContextEpoch {
	t.Helper()
	epoch, err := store.Epoch(ctx, sessionID)
	if err != nil {
		t.Fatalf("Epoch: %v", err)
	}
	return epoch
}

func compactionCheckpoint(epoch ContextEpoch, coveredThroughSeq, anchorUserSeq, preservedFromSeq Seq) CompactionCheckpoint {
	return CompactionCheckpoint{
		Summary:           validSummary(),
		ExpectedEpoch:     epoch,
		CoveredThroughSeq: coveredThroughSeq,
		AnchorUserSeq:     anchorUserSeq,
		PreservedFromSeq:  preservedFromSeq,
		Reason:            CompactionPreventive,
	}
}

func assertCompactionConflictIsAtomic(t *testing.T, store CompactionStore, ctx context.Context, sessionID string, checkpoint CompactionCheckpoint) {
	t.Helper()
	beforeEvents, err := store.Events(ctx, sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	beforeContext, err := store.ContextForRunner(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.CommitCompaction(ctx, sessionID, checkpoint); !errors.Is(err, ErrCompactionConflict) {
		t.Fatalf("CommitCompaction error = %v, want ErrCompactionConflict", err)
	}

	afterEvents, err := store.Events(ctx, sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	afterContext, err := store.ContextForRunner(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterEvents) != len(beforeEvents) || afterContext.Epoch != beforeContext.Epoch || afterContext.Checkpoint != beforeContext.Checkpoint {
		t.Fatalf("conflict mutated state: events %d -> %d, context %+v -> %+v", len(beforeEvents), len(afterEvents), beforeContext, afterContext)
	}
}

func assertSummaryArraysMarshalEmpty(t *testing.T, summary StructuredSummary) {
	t.Helper()
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"constraints_and_instructions", "decisions", "completed_work", "files_and_changes",
		"relevant_tool_results", "failures_and_attempts", "pending_and_next_step", "facts_not_to_reinterpret",
	} {
		if string(fields[field]) != "[]" {
			t.Errorf("%s = %s, want []", field, fields[field])
		}
	}
}

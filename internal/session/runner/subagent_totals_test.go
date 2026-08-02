package runner

import (
	"context"
	"errors"
	"testing"

	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tool"
)

func TestPublisherDecoratesOnlyTaskSettlementsIncludingUnresolved(t *testing.T) {
	for _, tc := range []struct {
		name             string
		fail, unresolved bool
		total            int
	}{
		{"success", false, false, 2}, {"failure", true, false, 1}, {"unresolved-zero", false, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &recordingAppender{}
			p := NewPublisher(spy, "s", "a", 0)
			publishAll(t, p, llm.Event{Kind: llm.ToolCall, CallID: "c", ToolName: "task"})
			r := tool.NewSettlementRecorder()
			r.SetSubagentToolCalls(tc.total)
			p.RegisterSettlementRecorder("c", r)
			var err error
			switch {
			case tc.unresolved:
				err = p.FailUnresolvedTools(context.Background(), errors.New("stream"))
			case tc.fail:
				err = p.ToolFailed(context.Background(), "c", errors.New("failed"))
			default:
				err = p.ToolSuccess(context.Background(), "c", "ok", "")
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, ok := session.SubagentToolCalls(spy.events[len(spy.events)-1]); !ok || got != tc.total {
				t.Fatalf("total = %d, %v", got, ok)
			}
		})
	}
	// A provider-executed task has no local recorder. If a later stream failure
	// closes it, the durable settlement still states that no local child tools ran.
	t.Run("task without local recorder", func(t *testing.T) {
		spy := &recordingAppender{}
		p := NewPublisher(spy, "s", "a", 0)
		publishAll(t, p, llm.Event{Kind: llm.ToolCall, CallID: "c", ToolName: "task"})
		if err := p.FailUnresolvedTools(context.Background(), errors.New("stream")); err != nil {
			t.Fatal(err)
		}
		if got, ok := session.SubagentToolCalls(spy.events[len(spy.events)-1]); !ok || got != 0 {
			t.Fatalf("total = %d, %v; want durable zero without a recorder", got, ok)
		}
	})
	spy := &recordingAppender{}
	p := NewPublisher(spy, "s", "a", 0)
	publishAll(t, p, llm.Event{Kind: llm.ToolCall, CallID: "c", ToolName: "echo"})
	p.RegisterSettlementRecorder("c", tool.NewSettlementRecorder())
	_ = p.ToolSuccess(context.Background(), "c", "", "")
	if _, ok := session.SubagentToolCalls(spy.events[1]); ok {
		t.Fatal("non-task decorated")
	}
}

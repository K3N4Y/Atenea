package subagent

import (
	"encoding/json"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/K3N4Y/atenea/agentcore/tool/tooltest"
	"github.com/K3N4Y/atenea/internal/agent"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/tool"
)

// TestTaskTool_Contract runs the published kit over the tool that is a whole
// agent underneath: every Execute stands up a child runner with its own store and
// drives a turn. It is the widest thing in the registry, so it is the one where a
// panic or a call that never returns costs the most.
func TestTaskTool_Contract(t *testing.T) {
	tooltest.Contract(t, func(*testing.T) tooltest.Subject {
		child := llm.NewFakeProvider(
			llm.Event{Kind: llm.StepStarted},
			llm.Event{Kind: llm.TextStarted},
			llm.Event{Kind: llm.TextDelta, Text: "the subagent's report"},
			llm.Event{Kind: llm.TextEnded},
			llm.Event{Kind: llm.StepEnded},
		)
		children := tool.NewRegistry(tool.NewOutputStore(0), tool.Echo{})
		defs := []agent.Def{{Name: "reviewer", Description: "Reviews code.", Prompt: "You review code."}}

		var messages atomic.Int64
		nextID := func() string { return "m" + strconv.FormatInt(messages.Add(1), 10) }

		return tooltest.Subject{
			Tool:  NewTaskTool(defs, child, children, nextID),
			Input: json.RawMessage(`{"subagent_type":"reviewer","prompt":"review this"}`),
		}
	})
}

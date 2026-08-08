package subagent

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/K3N4Y/atenea/agentcore/tool/tooltest"
	"github.com/K3N4Y/atenea/internal/agent"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/tool"
	fixturetool "github.com/K3N4Y/atenea/internal/tool/tooltest"
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
		children := tool.NewRegistry(tool.NewOutputStore(0), fixturetool.Echo{})
		defs := []agent.Def{{Name: "reviewer", Description: "Reviews code.", Prompt: "You review code."}}

		var messages atomic.Int64
		nextID := func() string { return "m" + strconv.FormatInt(messages.Add(1), 10) }

		return tooltest.Subject{
			Tool:  newTaskTool(defs, child, children, nextID),
			Input: json.RawMessage(`{"subagent_type":"reviewer","prompt":"review this"}`),
		}
	})
}

func TestSupervisionTools_Contract(t *testing.T) {
	for _, name := range []string{"task_status", "task_wait", "task_cancel"} {
		t.Run(name, func(t *testing.T) {
			tooltest.Contract(t, func(*testing.T) tooltest.Subject {
				supervisor := NewSupervisor(func() string { return "job" })
				started, err := supervisor.start(func(context.Context, *jobProgress) (string, error) { return "done", nil })
				if err != nil {
					t.Fatal(err)
				}
				var job struct {
					ID string `json:"job_id"`
				}
				if err := json.Unmarshal([]byte(started.Output), &job); err != nil {
					t.Fatal(err)
				}
				for _, candidate := range supervisor.tools() {
					if candidate.Name() == name {
						return tooltest.Subject{Tool: candidate, Input: json.RawMessage(`{"job_id":"` + job.ID + `"}`)}
					}
				}
				t.Fatalf("supervision tool %q missing", name)
				return tooltest.Subject{}
			})
		})
	}
}

func TestSupervisionDescriptionsFollowStandardFormat(t *testing.T) {
	supervisor := NewSupervisor(func() string { return "job" })
	for _, candidate := range supervisor.tools() {
		t.Run(candidate.Name(), func(t *testing.T) {
			description := candidate.Description()
			previous := -1
			for _, heading := range []string{"## Input grammar", "## Examples", "## Recoverable failures", "## Anti-patterns", "<critical>", "</critical>"} {
				at := strings.Index(description, heading)
				if at < 0 {
					t.Errorf("description is missing %q", heading)
					continue
				}
				if at <= previous {
					t.Errorf("%q is out of order", heading)
				}
				previous = at
			}
		})
	}
}

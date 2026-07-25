package tooltest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/K3N4Y/atenea/agentcore/permission"
	"github.com/K3N4Y/atenea/agentcore/tool"
)

// A kit is only worth what it catches. These tests run every check twice: once
// against a tool that honors the contract, where nothing may be reported, and
// once against a tool that breaks exactly one clause, where the check that owns
// that clause must be the one to complain. A check that stops catching its own
// violation fails here instead of silently blessing a broken tool.

// recorder stands in for *testing.T so a check's complaints can be read back
// instead of failing this test. It is written from the goroutines of the
// concurrency check, so it locks.
type recorder struct {
	mu       sync.Mutex
	failures []string
}

func (r *recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

func (r *recorder) reported() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.failures...)
}

// compliant is a minimal tool that honors every clause of the contract: a stable
// name, a stable description, a stable object schema, and an Execute that parses
// its input as JSON and never panics.
type compliant struct{}

func (compliant) Name() string        { return "echo" }
func (compliant) Description() string { return "Returns the text it is given." }

func (compliant) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`)
}

func (compliant) Execute(_ context.Context, input json.RawMessage) (tool.Result, error) {
	var in struct {
		Text string `json:"text"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return tool.Result{}, fmt.Errorf("echo: invalid input: %w", err)
		}
	}
	return tool.Result{Output: in.Text}, nil
}

func compliantSubject() Subject {
	return Subject{Tool: compliant{}, Input: json.RawMessage(`{"text":"hola"}`)}
}

func TestContract_PassesForACompliantTool(t *testing.T) {
	Contract(t, func(*testing.T) Subject { return compliantSubject() })
}

func TestChecks_ReportNothingForACompliantTool(t *testing.T) {
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			r := &recorder{}
			c.run(r, compliantSubject)
			if reported := r.reported(); len(reported) > 0 {
				t.Errorf("%s reported a compliant tool:\n%s", c.name, strings.Join(reported, "\n"))
			}
		})
	}
}

// The violating tools. Each breaks one clause and nothing else, so the check
// that catches it is the check that owns that clause.

type unstableName struct {
	compliant
	calls int
}

func (u *unstableName) Name() string {
	u.calls++
	return fmt.Sprintf("echo%d", u.calls)
}

type unannounceableName struct{ compliant }

func (unannounceableName) Name() string { return "echo tool!" }

type namelessTool struct{ compliant }

func (namelessTool) Name() string { return "" }

type blankDescription struct{ compliant }

func (blankDescription) Description() string { return "   \n" }

type unstableDescription struct {
	compliant
	calls int
}

func (u *unstableDescription) Description() string {
	u.calls++
	return fmt.Sprintf("description %d", u.calls)
}

type schemaNotAnObject struct{ compliant }

func (schemaNotAnObject) Schema() json.RawMessage { return json.RawMessage(`["text"]`) }

type schemaOfABareValue struct{ compliant }

func (schemaOfABareValue) Schema() json.RawMessage { return json.RawMessage(`{"type":"string"}`) }

type emptySchema struct{ compliant }

func (emptySchema) Schema() json.RawMessage { return nil }

type unstableSchema struct {
	compliant
	calls int
}

func (u *unstableSchema) Schema() json.RawMessage {
	u.calls++
	return json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{"text%d":{"type":"string"}}}`, u.calls))
}

// rejectsEverything refuses the input it declares it accepts.
type rejectsEverything struct{ compliant }

func (rejectsEverything) Execute(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.Result{}, errors.New("nope")
}

// matchesStrings reads its input by looking at the bytes instead of parsing
// them, so it works right up until the model spaces the same JSON differently.
type matchesStrings struct{ compliant }

func (matchesStrings) Execute(_ context.Context, input json.RawMessage) (tool.Result, error) {
	if !strings.Contains(string(input), `"text":"hola"`) {
		return tool.Result{}, errors.New("no text field")
	}
	return tool.Result{Output: "hola"}, nil
}

// panicsOnMalformedInput dereferences what the model sent without checking it.
type panicsOnMalformedInput struct{ compliant }

func (panicsOnMalformedInput) Execute(_ context.Context, input json.RawMessage) (tool.Result, error) {
	var in *struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		panic("unparseable input: " + err.Error())
	}
	return tool.Result{Output: in.Text}, nil
}

// hangs ignores its context and blocks longer than any turn would wait.
type hangs struct{ compliant }

func (hangs) Execute(context.Context, json.RawMessage) (tool.Result, error) {
	time.Sleep(time.Minute)
	return tool.Result{}, nil
}

// panicsWhenConcurrent is safe on its own and unsafe under a turn that calls it
// twice at once.
type panicsWhenConcurrent struct {
	mu     sync.Mutex
	inside bool
}

func (p *panicsWhenConcurrent) Name() string        { return compliant{}.Name() }
func (p *panicsWhenConcurrent) Description() string { return compliant{}.Description() }

func (p *panicsWhenConcurrent) Schema() json.RawMessage { return compliant{}.Schema() }

func (p *panicsWhenConcurrent) Execute(context.Context, json.RawMessage) (tool.Result, error) {
	p.mu.Lock()
	if p.inside {
		p.mu.Unlock()
		panic("re-entered")
	}
	p.inside = true
	p.mu.Unlock()
	time.Sleep(10 * time.Millisecond)
	p.mu.Lock()
	p.inside = false
	p.mu.Unlock()
	return tool.Result{Output: "ok"}, nil
}

func TestChecks_CatchAViolatingTool(t *testing.T) {
	// The hang case has to outlast the kit's own patience, so the whole table
	// runs with a timeout short enough to keep this test fast.
	restore := executeTimeout
	executeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { executeTimeout = restore })

	cases := []struct {
		violation string
		check     string
		subject   func() Subject
	}{
		{"a name that changes between calls", "NameIsStableAndAnnounceable",
			func() Subject { return Subject{Tool: &unstableName{}} }},
		{"a name no provider accepts", "NameIsStableAndAnnounceable",
			func() Subject { return Subject{Tool: unannounceableName{}} }},
		{"no name at all", "NameIsStableAndAnnounceable",
			func() Subject { return Subject{Tool: namelessTool{}} }},
		{"a blank description", "DescriptionIsStableAndNotEmpty",
			func() Subject { return Subject{Tool: blankDescription{}} }},
		{"a description that changes between calls", "DescriptionIsStableAndNotEmpty",
			func() Subject { return Subject{Tool: &unstableDescription{}} }},
		{"a schema that is not a JSON object", "SchemaIsAStableObjectSchema",
			func() Subject { return Subject{Tool: schemaNotAnObject{}} }},
		{"a schema of a bare value", "SchemaIsAStableObjectSchema",
			func() Subject { return Subject{Tool: schemaOfABareValue{}} }},
		{"no schema at all", "SchemaIsAStableObjectSchema",
			func() Subject { return Subject{Tool: emptySchema{}} }},
		{"a schema that changes between calls", "SchemaIsAStableObjectSchema",
			func() Subject { return Subject{Tool: &unstableSchema{}} }},
		{"an Execute that rejects the declared input", "ExecuteAcceptsTheDeclaredInput",
			func() Subject { return Subject{Tool: rejectsEverything{}, Input: json.RawMessage(`{"text":"hola"}`)} }},
		{"an Execute that reads its input by matching strings", "ExecuteAcceptsTheSameInputReserialized",
			func() Subject { return Subject{Tool: matchesStrings{}, Input: json.RawMessage(`{"text":"hola"}`)} }},
		{"an Execute that panics on what the model sent", "ExecuteSurvivesMalformedInput",
			func() Subject {
				return Subject{Tool: panicsOnMalformedInput{}, Input: json.RawMessage(`{"text":"hola"}`)}
			}},
		{"an Execute that never returns", "ExecuteAcceptsTheDeclaredInput",
			func() Subject { return Subject{Tool: hangs{}, Input: json.RawMessage(`{"text":"hola"}`)} }},
		{"an Execute that ignores a cancelled context and hangs", "ExecuteReturnsOnCancelledContext",
			func() Subject { return Subject{Tool: hangs{}, Input: json.RawMessage(`{"text":"hola"}`)} }},
		{"an Execute that is not safe to call twice at once", "ExecuteIsSafeForConcurrentUse",
			func() Subject {
				return Subject{Tool: &panicsWhenConcurrent{}, Input: json.RawMessage(`{"text":"hola"}`)}
			}},
		{"effects that change between calls", "EffectsAreStable",
			func() Subject { return Subject{Tool: &unstableEffects{}} }},
		{"a grant naming another tool", "GrantRuleIsPureAndNamesTheTool",
			func() Subject { return Subject{Tool: grantsAnotherTool{}, Input: json.RawMessage(`{"text":"hola"}`)} }},
		{"a grant that changes between calls", "GrantRuleIsPureAndNamesTheTool",
			func() Subject { return Subject{Tool: &unstableGrant{}, Input: json.RawMessage(`{"text":"hola"}`)} }},
		{"a GrantRule that panics on what the model sent", "GrantRuleIsPureAndNamesTheTool",
			func() Subject { return Subject{Tool: panicsWhenGranting{}, Input: json.RawMessage(`{"text":"hola"}`)} }},
		{"a file card asked for by an unsettled call", "PresentationIsPureAndSurvivesAnyInput",
			func() Subject { return Subject{Tool: cardWithoutADiff{}, Input: json.RawMessage(`{"text":"hola"}`)} }},
		{"a presentation that changes between redraws", "PresentationIsPureAndSurvivesAnyInput",
			func() Subject {
				return Subject{Tool: &unstablePresentation{}, Input: json.RawMessage(`{"text":"hola"}`)}
			}},
		{"a Present that panics on what the model sent", "PresentationIsPureAndSurvivesAnyInput",
			func() Subject {
				return Subject{Tool: panicsWhenPresenting{}, Input: json.RawMessage(`{"text":"hola"}`)}
			}},
	}

	for _, c := range cases {
		t.Run(c.violation, func(t *testing.T) {
			run := checkByName(t, c.check)
			r := &recorder{}
			// One subject per case, shared by the calls a check makes, so a tool
			// that counts calls sees them all.
			subject := c.subject()
			run(r, func() Subject { return subject })
			if len(r.reported()) == 0 {
				t.Errorf("%s accepted a tool with %s", c.check, c.violation)
			}
		})
	}
}

// The optional capability interfaces. compliant implements none of them, which is
// legal, so each violator adds one badly.

// unstableEffects reports different effects on consecutive calls, so the same tool
// is gated one way and waved through the next.
type unstableEffects struct {
	compliant
	calls int
}

func (u *unstableEffects) Effects() tool.Effects {
	u.calls++
	if u.calls%2 == 0 {
		return tool.NoEffects
	}
	return tool.RunsCommands
}

// grantsAnotherTool derives a rule naming a tool that is not itself: approving one
// of its calls would silently authorize somebody else's.
type grantsAnotherTool struct{ compliant }

func (grantsAnotherTool) GrantRule(tool.Call) (permission.Rule, bool) {
	return permission.Rule{Tool: "bash"}, true
}

// unstableGrant derives a different prefix every time, so what the panel offered
// is not what a later call is checked against.
type unstableGrant struct {
	compliant
	calls int
}

func (u *unstableGrant) GrantRule(tool.Call) (permission.Rule, bool) {
	u.calls++
	return permission.Rule{Tool: "echo", Prefix: fmt.Sprintf("prefix-%d", u.calls)}, true
}

// panicsWhenGranting dereferences the input without checking it, on the goroutine
// that is drawing the permission prompt.
type panicsWhenGranting struct{ compliant }

func (panicsWhenGranting) GrantRule(call tool.Call) (permission.Rule, bool) {
	var in *struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(call.Input, &in); err != nil {
		panic("unparseable input: " + err.Error())
	}
	return permission.Rule{Tool: "echo", Prefix: in.Text}, true
}

// cardWithoutADiff asks for a file card from a call that has not settled, leaving
// the host to render a diff that does not exist.
type cardWithoutADiff struct{ compliant }

func (cardWithoutADiff) Present(tool.Call, tool.Result) tool.Presentation {
	return tool.Presentation{Kind: tool.FileChange, Label: "Echo"}
}

// unstablePresentation reads differently on every redraw.
type unstablePresentation struct {
	compliant
	calls int
}

func (u *unstablePresentation) Present(tool.Call, tool.Result) tool.Presentation {
	u.calls++
	return tool.Presentation{Label: fmt.Sprintf("Echo %d", u.calls)}
}

// panicsWhenPresenting takes the UI down with the model's malformed input.
type panicsWhenPresenting struct{ compliant }

func (panicsWhenPresenting) Present(call tool.Call, _ tool.Result) tool.Presentation {
	var in *struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(call.Input, &in); err != nil {
		panic("unparseable input: " + err.Error())
	}
	return tool.Presentation{Label: "Echo", Subject: in.Text}
}

func checkByName(t *testing.T, name string) func(reporter, fresh) {
	t.Helper()
	for _, c := range checks {
		if c.name == name {
			return c.run
		}
	}
	t.Fatalf("no check named %q", name)
	return nil
}

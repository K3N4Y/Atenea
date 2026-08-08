package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/K3N4Y/atenea/internal/tool"
)

func testCatalog(t *testing.T) tool.Catalog {
	t.Helper()
	root := t.TempDir()
	return tool.NewRegistry(tool.NewOutputStore(1024),
		tool.NewReadTool(root, nil), tool.NewGlobTool(root), tool.NewBashTool(root))
}

// TestValidate_ReportsUnknownToolNames is the point of the check: before it, a
// name the registry did not have simply never became a permission and the
// subagent ran with fewer tools than its definition asked for, silently.
func TestValidate_ReportsUnknownToolNames(t *testing.T) {
	defs := []Def{
		{Name: "typo", Tools: []string{"read", "bash_tool", "Glob"}, Location: "/w/.atenea/agents/typo.md"},
		{Name: "fine", Tools: []string{"read", "glob"}},
	}

	problems := Validate(defs, testCatalog(t))
	if len(problems) != 1 {
		t.Fatalf("Validate() reported %d problems, want 1: %v", len(problems), problems)
	}
	message := problems[0].Error()
	for _, want := range []string{`"typo"`, `"bash_tool"`, `"Glob"`, "/w/.atenea/agents/typo.md", "bash, glob, read"} {
		if !strings.Contains(message, want) {
			t.Errorf("Validate() message = %q, want it to contain %q", message, want)
		}
	}
}

// TestValidate_SaysNothingWhenEverythingResolves keeps the report worth reading: a
// catalog whose defs all resolve produces no output at all, and neither does a
// def with no tools — a subagent that only reasons is a legitimate definition, not
// a mistake.
func TestValidate_SaysNothingWhenEverythingResolves(t *testing.T) {
	defs := []Def{
		{Name: "explore", Tools: []string{"read", "glob"}},
		{Name: "thinker"},
	}
	if problems := Validate(defs, testCatalog(t)); len(problems) != 0 {
		t.Errorf("Validate() = %v, want no problems", problems)
	}
}

// TestValidate_WithoutACatalogReportsNothing: with nothing to validate against,
// every name is unverifiable rather than wrong.
func TestValidate_WithoutACatalogReportsNothing(t *testing.T) {
	if problems := Validate([]Def{{Name: "x", Tools: []string{"nope"}}}, nil); len(problems) != 0 {
		t.Errorf("Validate(nil catalog) = %v, want no problems", problems)
	}
}

// TestValidate_BuiltinsResolveAgainstTheChildRegistry: the built-in defs encode
// tool names by hand, so a rename in the tool package must show up here rather
// than as a subagent quietly losing a tool.
func TestValidate_BuiltinsResolveAgainstTheChildRegistry(t *testing.T) {
	root := t.TempDir()
	children := tool.NewRegistry(tool.NewOutputStore(1024),
		tool.NewReadTool(root, nil), tool.NewWriteTool(root, nil),
		tool.NewEditTool(root, nil, nil), tool.NewGlobTool(root),
		tool.NewGrepTool(root, nil), tool.NewBashTool(root), taskStub{})
	if problems := Validate(Builtins(), children); len(problems) != 0 {
		t.Errorf("Validate(Builtins()) = %v, want no problems", problems)
	}
}

type taskStub struct{}

func (taskStub) Name() string        { return "task" }
func (taskStub) Description() string { return "recursive task" }
func (taskStub) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (taskStub) Execute(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.Result{}, nil
}

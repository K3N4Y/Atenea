package permission

import (
	"encoding/json"
	"testing"

	"github.com/K3N4Y/atenea/internal/tool"
)

// grantable builds a tool that offers rule for any call.
func grantable(name string, rule Rule) grantableTool {
	return grantableTool{declaringTool: declaring(name, tool.WritesFiles), rule: rule, grantable: true}
}

func bashInput(command string) []byte {
	raw, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		panic(err)
	}
	return raw
}

// TestRuleFor_AsksTheToolThatWouldSettleTheCall: the grant comes from the tool,
// so a tool atenea does not ship — one an MCP server contributed — offers "allow
// for the session" on the same terms as bash or write.
func TestRuleFor_AsksTheToolThatWouldSettleTheCall(t *testing.T) {
	want := Rule{Tool: "mcp_github_create_issue"}
	c := catalog{"mcp_github_create_issue": grantable("mcp_github_create_issue", want)}

	rule, ok := RuleFor(c, tool.Call{Name: "mcp_github_create_issue", Input: []byte(`{"title":"x"}`)})
	if !ok || rule != want {
		t.Errorf("RuleFor(mcp tool) = %+v, %v; want %+v, true", rule, ok, want)
	}
}

// TestRuleFor_RefusesWhenThereIsNoHonestAnswer covers the three ways a call is
// not grantable. Each one keeps asking every time, which is the safe outcome: a
// grant must never claim more than what the user was shown.
func TestRuleFor_RefusesWhenThereIsNoHonestAnswer(t *testing.T) {
	declines := grantableTool{declaringTool: declaring("web_fetch", tool.ReachesNetwork)}
	c := catalog{
		"read":      declaring("read", tool.NoEffects), // does not implement Grantable
		"web_fetch": declines,                          // implements it and says no
	}
	cases := []struct {
		name    string
		call    string
		because string
	}{
		{"read", "read", "a tool that does not implement Grantable"},
		{"web_fetch", "web_fetch", "a tool that declines to summarize this input"},
		{"unregistered", "nope", "a name the catalog does not know"},
	}
	for _, tc := range cases {
		if rule, ok := RuleFor(c, tool.Call{Name: tc.call}); ok {
			t.Errorf("RuleFor(%s) = %+v, want not grantable: %s", tc.name, rule, tc.because)
		}
	}
	if rule, ok := RuleFor(nil, tool.Call{Name: "read"}); ok {
		t.Errorf("RuleFor with a nil catalog = %+v, want not grantable", rule)
	}
}

// TestCovers_OnlyTheGrantedShape: covers re-derives what the incoming call would
// grant instead of pattern-matching its input, so a call the user could not have
// granted can never be waved through by an existing grant either. It runs against
// the real bash and write tools, since the derivation being re-run is theirs.
func TestCovers_OnlyTheGrantedShape(t *testing.T) {
	root := t.TempDir()
	c := catalog{"bash": tool.NewBashTool(root), "write": tool.NewWriteTool(root, nil)}
	goTest := Rule{Tool: "bash", Prefix: "go test"}

	for _, command := range []string{"go test ./...", "go test -run TestX ./internal"} {
		if !covers(goTest, c, tool.Call{Name: "bash", Input: bashInput(command)}) {
			t.Errorf("covers(Rule{go test}, %q) = false, want true", command)
		}
	}
	for _, command := range []string{"go build ./...", "go test ./... && rm -rf /", "gotest ./..."} {
		if covers(goTest, c, tool.Call{Name: "bash", Input: bashInput(command)}) {
			t.Errorf("covers(Rule{go test}, %q) = true, want false", command)
		}
	}
	if covers(goTest, c, tool.Call{Name: "write", Input: []byte(`{"path":"a.txt"}`)}) {
		t.Error("a bash rule must not cover a write call")
	}

	write := Rule{Tool: "write"}
	if !covers(write, c, tool.Call{Name: "write", Input: []byte(`{"path":"b.txt"}`)}) {
		t.Error("covers(Rule{write}, any write) = false, want true")
	}
	if covers(Rule{}, c, tool.Call{Name: "bash", Input: bashInput("ls")}) {
		t.Error("the zero Rule must cover nothing")
	}
}

// TestRule_LabelNamesTheAuthorizedSubject: the panel's action reads "Allow <x>
// this session", so the label must be exactly what the grant covers.
func TestRule_LabelNamesTheAuthorizedSubject(t *testing.T) {
	if got := (Rule{Tool: "bash", Prefix: "go test"}).Label(); got != "go test" {
		t.Errorf("Label() = %q, want %q", got, "go test")
	}
	if got := (Rule{Tool: "write"}).Label(); got != "write" {
		t.Errorf("Label() = %q, want %q", got, "write")
	}
}

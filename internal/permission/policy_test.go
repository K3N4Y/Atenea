package permission

import (
	"testing"

	"atenea/internal/tool"
)

// TestDecision_ZeroValueIsAsk pins the fail-safe invariant: an unclassified
// (zero-valued) Decision asks instead of silently allowing.
func TestDecision_ZeroValueIsAsk(t *testing.T) {
	var d Decision
	if d != Ask {
		t.Fatalf("zero Decision = %v, want Ask", d)
	}
}

// TestStaticPolicy_AsksListedToolsOnly asserts the classification contract:
// the listed names Ask, every other name — including MCP tools the policy has
// never heard of — is allowed.
func TestStaticPolicy_AsksListedToolsOnly(t *testing.T) {
	p := NewStaticPolicy("bash", "write", "edit", "web_fetch")

	cases := []struct {
		name string
		want Decision
	}{
		{"bash", Ask},
		{"write", Ask},
		{"edit", Ask},
		{"web_fetch", Ask},
		{"read", Allow},
		{"glob", Allow},
		{"grep", Allow},
		{"skill", Allow},
		{"todo_write", Allow},
		{"present_plan", Allow},
		{"task", Allow},
		{"mcp__github__get_issue", Allow},
		{"", Allow},
	}
	for _, tc := range cases {
		if got := p.Decide(tool.Call{Name: tc.name}); got != tc.want {
			t.Errorf("Decide(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestStaticPolicy_EmptyAllowsEverything: a StaticPolicy with no listed tools
// gates nothing.
func TestStaticPolicy_EmptyAllowsEverything(t *testing.T) {
	p := NewStaticPolicy()
	if got := p.Decide(tool.Call{Name: "bash"}); got != Allow {
		t.Errorf("Decide(bash) with empty policy = %v, want Allow", got)
	}
}

package permission

import "atenea/internal/tool"

// Decision is the policy verdict for a tool call.
type Decision int

const (
	// Ask blocks the call until the user approves or denies it. It is the
	// zero value on purpose: an unclassified decision fails safe by asking.
	Ask Decision = iota
	// Allow settles the call without asking.
	Allow
	// Deny fails the call without asking. No shipped policy returns it yet;
	// the runner handles it so a future rule-based policy can.
	Deny
)

// Policy decides what to do with a tool call before it settles. It receives
// the full call (name and raw input) so richer implementations can match on
// command prefixes or paths; StaticPolicy only looks at the name.
type Policy interface {
	Decide(call tool.Call) Decision
}

// StaticPolicy is the fixed classification by tool name: the listed tools
// ask, everything else (including MCP tools) is allowed. Deny is not
// expressible here; it arrives with rule-based policies.
type StaticPolicy struct {
	ask map[string]bool
}

// NewStaticPolicy builds a StaticPolicy that asks for the given tool names.
func NewStaticPolicy(ask ...string) StaticPolicy {
	m := make(map[string]bool, len(ask))
	for _, name := range ask {
		m[name] = true
	}
	return StaticPolicy{ask: m}
}

// Decide returns Ask for the classified tools and Allow for everything else.
func (p StaticPolicy) Decide(call tool.Call) Decision {
	if p.ask[call.Name] {
		return Ask
	}
	return Allow
}

package permission

import "github.com/K3N4Y/atenea/internal/tool"

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
// The classification is fixed, so the session plays no part in it.
func (p StaticPolicy) Decide(_ string, call tool.Call) Decision {
	if p.ask[call.Name] {
		return Ask
	}
	return Allow
}

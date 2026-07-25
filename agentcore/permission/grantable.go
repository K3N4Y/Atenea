package permission

import "github.com/K3N4Y/atenea/agentcore/tool"

// Grantable is the optional interface a tool implements to say what "allow this
// for the rest of the session" would authorize for one of its calls.
//
// Only the tool can answer honestly. A grant must never claim more than what the
// user was shown, and what a given input amounts to is the tool's own semantics:
// a shell command reduces to a prefix, a file write reduces to the tool itself,
// and a call whose input cannot be summarized safely reduces to nothing. So
// GrantRule returns false for a call it cannot describe without overreaching, and
// the host keeps asking every time instead of granting something broader than the
// question it put on screen.
//
// The same derivation runs again on every later call, to decide whether an
// existing grant covers it. That makes GrantRule the whole of the grant's
// semantics, and it has to be a pure function of the call: same input, same rule,
// no state, safe to call concurrently. A tool that does not implement it can be
// allowed once or denied, never granted.
type Grantable interface {
	tool.Tool
	GrantRule(call tool.Call) (Rule, bool)
}

// GrantRuleFor derives the grant approving this call for the whole session would
// create, and reports whether the call is grantable at all. It resolves the
// optional interface in one place, so a tool that cannot be granted and a tool
// that refuses to grant this particular call are handled the same way by the
// host: keep asking.
func GrantRuleFor(t tool.Tool, call tool.Call) (Rule, bool) {
	g, ok := t.(Grantable)
	if !ok {
		return Rule{}, false
	}
	return g.GrantRule(call)
}

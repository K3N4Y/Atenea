package permission

import (
	contract "github.com/K3N4Y/atenea/agentcore/permission"
	"github.com/K3N4Y/atenea/internal/tool"
)

// RuleFor derives the grant that approving this call for the whole session would
// create, and reports whether the call is grantable at all. It asks the tool that
// would settle it (permission.Grantable) instead of deciding by name, so a tool
// atenea does not ship — one an MCP server contributed — can offer "allow for the
// session" on the same terms as bash or write.
//
// It refuses whenever there is no honest answer: a tool that is not registered,
// one that does not implement Grantable, or one that declines to summarize this
// particular input. In each case the call keeps asking every time, which is the
// safe outcome — a grant must never claim more than what the user was shown.
func RuleFor(catalog tool.Catalog, call tool.Call) (Rule, bool) {
	if catalog == nil {
		return Rule{}, false
	}
	t, ok := catalog.Lookup(call.Name)
	if !ok {
		return Rule{}, false
	}
	return contract.GrantRuleFor(t, call)
}

// covers reports whether the call falls under the rule. It re-derives what the
// call would grant and compares that with what was granted, rather than
// pattern-matching the input: the grantability test therefore runs again on every
// call, and a command the user could never have granted can never be matched by a
// grant either.
func covers(rule Rule, catalog tool.Catalog, call tool.Call) bool {
	derived, ok := RuleFor(catalog, call)
	return ok && derived == rule
}

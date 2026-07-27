package permission

import (
	"slices"
	"sync"

	"github.com/K3N4Y/atenea/internal/tool"
)

// SessionGrants is the store of the user's "allow this for the rest of the
// session" answers ("allow `go test` this session", "allow write this session").
// It only records them; GrantedPolicy is what turns them into decisions.
//
// The split follows their lifetimes. Grants belong to the sitting, so the caller
// owns this store and keeps it across a rewire (an MCP connect, a workspace
// change) instead of dropping the user's answers mid-session. The policy that
// reads them is rebuilt with every registry, because deciding whether a grant
// covers a call means asking the tool that would settle it, and that tool comes
// from the registry of the moment.
//
// Grants live in the process only. Reopening a session asks again, so a grant
// never outlives the sitting that justified it and nothing on disk has to be
// audited later. They are keyed by session id, so one session's grants cannot
// answer for another — including a subagent's child session, which asks on its
// own behalf.
//
// Safe for concurrent use: the runner decides the tool calls of a turn from
// several goroutines while the UI adds grants.
type SessionGrants struct {
	mu    sync.RWMutex
	rules map[string][]Rule // sessionID -> grants given in that session
}

// NewSessionGrants builds an empty grant store.
func NewSessionGrants() *SessionGrants {
	return &SessionGrants{rules: make(map[string][]Rule)}
}

// Grant records a rule for the rest of the session. A zero rule (the call was
// not grantable) and an empty session are ignored, and a repeated grant is not
// stored twice.
func (g *SessionGrants) Grant(sessionID string, rule Rule) {
	if sessionID == "" || rule.Tool == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, existing := range g.rules[sessionID] {
		if existing == rule {
			return
		}
	}
	g.rules[sessionID] = append(g.rules[sessionID], rule)
}

// rulesFor returns a copy of what the session has granted. The copy is what
// makes the caller's loop safe: the store keeps taking grants while a decision
// is being made.
func (g *SessionGrants) rulesFor(sessionID string) []Rule {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return slices.Clone(g.rules[sessionID])
}

// GrantedPolicy layers a session's grants over a base policy: a call the base
// would ask about is allowed without asking once a grant of that same session
// covers it.
//
// It is built per registry, alongside the base classification, because covers
// asks the tool to re-derive what the call would grant (see RuleFor).
type GrantedPolicy struct {
	base    Policy
	grants  *SessionGrants
	catalog tool.Catalog
}

// NewRulePolicy layers durable, caller-owned grants over base. Unlike
// SessionGrants these rules have no session key: their lifetime and persistence
// are owned by the configuration that supplied them. They only upgrade Ask and
// are revalidated through the live catalog on every call.
func NewRulePolicy(base Policy, rules []Rule, catalog tool.Catalog) Policy {
	if len(rules) == 0 {
		return base
	}
	return rulePolicy{base: base, rules: slices.Clone(rules), catalog: catalog}
}

type rulePolicy struct {
	base    Policy
	rules   []Rule
	catalog tool.Catalog
}

func (p rulePolicy) Decide(sessionID string, call tool.Call) Decision {
	decision := p.base.Decide(sessionID, call)
	if decision != Ask {
		return decision
	}
	for _, rule := range p.rules {
		if covers(rule, p.catalog, call) {
			return Allow
		}
	}
	return Ask
}

// NewGrantedPolicy wraps base so the grants recorded in the store can skip its
// questions. A nil store yields the base untouched, which is how a UI without
// the "allow for the session" affordance gates exactly as it would without one.
func NewGrantedPolicy(base Policy, grants *SessionGrants, catalog tool.Catalog) Policy {
	if grants == nil {
		return base
	}
	return GrantedPolicy{base: base, grants: grants, catalog: catalog}
}

// Decide defers to the base policy and upgrades Ask to Allow when a grant of
// this session covers the call. Allow and Deny pass through untouched: a grant
// can only skip a question, never overrule a denial.
func (p GrantedPolicy) Decide(sessionID string, call tool.Call) Decision {
	decision := p.base.Decide(sessionID, call)
	if decision != Ask {
		return decision
	}
	for _, rule := range p.grants.rulesFor(sessionID) {
		if covers(rule, p.catalog, call) {
			return Allow
		}
	}
	return Ask
}

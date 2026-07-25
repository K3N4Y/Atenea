package permission

import (
	"sync"

	"atenea/internal/tool"
)

// SessionGrants layers session-scoped approvals over a base policy: a call the
// base would ask for is allowed without asking once a grant the user gave in
// that same session covers it ("allow `go test` this session", "allow write
// this session").
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
	base  Policy
	mu    sync.RWMutex
	rules map[string][]Rule // sessionID -> grants given in that session
}

// NewSessionGrants wraps base with an empty grant store.
func NewSessionGrants(base Policy) *SessionGrants {
	return &SessionGrants{base: base, rules: make(map[string][]Rule)}
}

// Decide defers to the base policy and upgrades Ask to Allow when a grant of
// this session covers the call. Allow and Deny pass through untouched: a grant
// can only skip a question, never overrule a denial.
func (g *SessionGrants) Decide(sessionID string, call tool.Call) Decision {
	decision := g.base.Decide(sessionID, call)
	if decision != Ask {
		return decision
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, rule := range g.rules[sessionID] {
		if rule.Matches(call) {
			return Allow
		}
	}
	return Ask
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

package permission

import (
	"sync"
	"testing"

	"github.com/K3N4Y/atenea/internal/tool"
)

// gatedCatalog is the world the grant tests run in: bash and write ask (they
// declare effects) and read does not, with the real derivations behind the
// grants.
func gatedCatalog(t *testing.T) catalog {
	root := t.TempDir()
	return catalog{
		"bash":  tool.NewBashTool(root),
		"write": tool.NewWriteTool(root, nil),
		"edit":  tool.NewEditTool(root, nil, nil),
		"read":  tool.NewReadTool(root, nil),
	}
}

// granted builds the policy under test: the effects classification with a fresh
// grant store layered over it.
func granted(t *testing.T) (Policy, *SessionGrants) {
	c := gatedCatalog(t)
	grants := NewSessionGrants()
	return NewGrantedPolicy(NewEffectsPolicy(c), grants, c), grants
}

func bashCall(command string) tool.Call {
	return tool.Call{Name: "bash", Input: bashInput(command)}
}

// TestGrantedPolicy_WithoutGrantsBehavesLikeTheBase: an empty store changes
// nothing, so wiring it in never widens what the classification allows.
func TestGrantedPolicy_WithoutGrantsBehavesLikeTheBase(t *testing.T) {
	p, _ := granted(t)
	if got := p.Decide("s1", bashCall("go test ./...")); got != Ask {
		t.Errorf("Decide(bash) = %v, want Ask", got)
	}
	if got := p.Decide("s1", tool.Call{Name: "read"}); got != Allow {
		t.Errorf("Decide(read) = %v, want Allow", got)
	}
}

// TestGrantedPolicy_NilStoreIsTheBase: a UI without the "allow for the session"
// affordance gates exactly as it would without one.
func TestGrantedPolicy_NilStoreIsTheBase(t *testing.T) {
	c := gatedCatalog(t)
	p := NewGrantedPolicy(NewEffectsPolicy(c), nil, c)
	if _, wrapped := p.(GrantedPolicy); wrapped {
		t.Error("NewGrantedPolicy(base, nil, …) wrapped the base, want it returned untouched")
	}
	if got := p.Decide("s1", bashCall("go test ./...")); got != Ask {
		t.Errorf("Decide(bash) = %v, want Ask", got)
	}
}

// TestGrantedPolicy_GrantedPrefixStopsAsking: the granted shape is allowed for
// the rest of the session and nothing else is.
func TestGrantedPolicy_GrantedPrefixStopsAsking(t *testing.T) {
	p, grants := granted(t)
	c := gatedCatalog(t)
	rule, ok := RuleFor(c, bashCall("go test ./..."))
	if !ok {
		t.Fatal("RuleFor(go test) = not grantable")
	}
	grants.Grant("s1", rule)

	if got := p.Decide("s1", bashCall("go test -run TestX ./internal")); got != Allow {
		t.Errorf("Decide(go test -run …) = %v, want Allow after the grant", got)
	}
	if got := p.Decide("s1", bashCall("go build ./...")); got != Ask {
		t.Errorf("Decide(go build) = %v, want Ask: the grant covers `go test` only", got)
	}
	if got := p.Decide("s1", tool.Call{Name: "write", Input: []byte(`{"path":"a.txt"}`)}); got != Ask {
		t.Errorf("Decide(write) = %v, want Ask: a bash grant says nothing about write", got)
	}
}

// TestGrantedPolicy_AreScopedToOneSession: a grant given in one session must not
// answer for another — including a subagent's child session, which asks on its
// own behalf.
func TestGrantedPolicy_AreScopedToOneSession(t *testing.T) {
	p, grants := granted(t)
	grants.Grant("s1", Rule{Tool: "write"})

	if got := p.Decide("s1", tool.Call{Name: "write"}); got != Allow {
		t.Errorf("Decide(s1, write) = %v, want Allow", got)
	}
	for _, other := range []string{"s2", "child-1", ""} {
		if got := p.Decide(other, tool.Call{Name: "write"}); got != Ask {
			t.Errorf("Decide(%q, write) = %v, want Ask: grants do not cross sessions", other, got)
		}
	}
}

// TestGrantedPolicy_DoNotOverruleTheBase: a grant can only skip a question. A
// policy that allows or denies outright is untouched, so a future deny rule
// cannot be granted away.
func TestGrantedPolicy_DoNotOverruleTheBase(t *testing.T) {
	c := gatedCatalog(t)
	grants := NewSessionGrants()
	p := NewGrantedPolicy(policyFunc(func(string, tool.Call) Decision { return Deny }), grants, c)
	grants.Grant("s1", Rule{Tool: "write"})
	if got := p.Decide("s1", tool.Call{Name: "write"}); got != Deny {
		t.Errorf("Decide(write) = %v, want Deny: a grant must not overrule the base policy", got)
	}
}

func TestRulePolicy_PersistedRulesApplyAcrossSessionsButOnlyToAsk(t *testing.T) {
	c := gatedCatalog(t)
	p := NewRulePolicy(NewEffectsPolicy(c), []Rule{{Tool: "write"}}, c)
	for _, session := range []string{"s1", "s2", ""} {
		if got := p.Decide(session, tool.Call{Name: "write"}); got != Allow {
			t.Errorf("Decide(%q, write) = %v, want Allow", session, got)
		}
	}
	denied := NewRulePolicy(policyFunc(func(string, tool.Call) Decision { return Deny }), []Rule{{Tool: "write"}}, c)
	if got := denied.Decide("s1", tool.Call{Name: "write"}); got != Deny {
		t.Errorf("persisted grant overruled Deny: %v", got)
	}
}

// TestSessionGrants_IgnoresUngrantableInput keeps the store clean: a zero rule
// (the call was not grantable) or an empty session records nothing, so nothing
// can be allowed by accident.
func TestSessionGrants_IgnoresUngrantableInput(t *testing.T) {
	p, grants := granted(t)
	grants.Grant("s1", Rule{})
	grants.Grant("", Rule{Tool: "write"})
	if got := p.Decide("s1", tool.Call{Name: "write"}); got != Ask {
		t.Errorf("Decide(write) = %v, want Ask", got)
	}
	if got := p.Decide("", tool.Call{Name: "write"}); got != Ask {
		t.Errorf("Decide(\"\", write) = %v, want Ask", got)
	}
}

// TestGrantedPolicy_ConcurrentGrantAndDecide covers the real access pattern under
// -race: the runner decides a turn's calls from several goroutines while the UI
// adds grants.
func TestGrantedPolicy_ConcurrentGrantAndDecide(t *testing.T) {
	p, grants := granted(t)
	var wg sync.WaitGroup
	for index := 0; index < 20; index++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			grants.Grant("s1", Rule{Tool: "write"})
		}()
		go func() {
			defer wg.Done()
			p.Decide("s1", bashCall("go test ./..."))
		}()
	}
	wg.Wait()
	if got := p.Decide("s1", tool.Call{Name: "write"}); got != Allow {
		t.Errorf("Decide(write) = %v, want Allow", got)
	}
}

// policyFunc adapts a function to Policy for the tests.
type policyFunc func(sessionID string, call tool.Call) Decision

func (f policyFunc) Decide(sessionID string, call tool.Call) Decision { return f(sessionID, call) }

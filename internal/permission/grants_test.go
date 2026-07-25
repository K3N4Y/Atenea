package permission

import (
	"sync"
	"testing"

	"github.com/K3N4Y/atenea/internal/tool"
)

func bashCall(command string) tool.Call {
	return tool.Call{Name: "bash", Input: bashInput(command)}
}

// TestSessionGrants_WithoutGrantsBehavesLikeTheBase: an empty store changes
// nothing, so wiring it in never widens what the fixed policy allows.
func TestSessionGrants_WithoutGrantsBehavesLikeTheBase(t *testing.T) {
	g := NewSessionGrants(NewStaticPolicy("bash", "write"))
	if got := g.Decide("s1", bashCall("go test ./...")); got != Ask {
		t.Errorf("Decide(bash) = %v, want Ask", got)
	}
	if got := g.Decide("s1", tool.Call{Name: "read"}); got != Allow {
		t.Errorf("Decide(read) = %v, want Allow", got)
	}
}

// TestSessionGrants_GrantedPrefixStopsAsking: the granted shape is allowed for
// the rest of the session and nothing else is.
func TestSessionGrants_GrantedPrefixStopsAsking(t *testing.T) {
	g := NewSessionGrants(NewStaticPolicy("bash", "write"))
	rule, ok := RuleFor("bash", bashInput("go test ./..."))
	if !ok {
		t.Fatal("RuleFor(go test) = not grantable")
	}
	g.Grant("s1", rule)

	if got := g.Decide("s1", bashCall("go test -run TestX ./internal")); got != Allow {
		t.Errorf("Decide(go test -run …) = %v, want Allow after the grant", got)
	}
	if got := g.Decide("s1", bashCall("go build ./...")); got != Ask {
		t.Errorf("Decide(go build) = %v, want Ask: the grant covers `go test` only", got)
	}
	if got := g.Decide("s1", tool.Call{Name: "write", Input: []byte(`{"path":"a.txt"}`)}); got != Ask {
		t.Errorf("Decide(write) = %v, want Ask: a bash grant says nothing about write", got)
	}
}

// TestSessionGrants_AreScopedToOneSession: a grant given in one session must not
// answer for another — including a subagent's child session, which asks on its
// own behalf.
func TestSessionGrants_AreScopedToOneSession(t *testing.T) {
	g := NewSessionGrants(NewStaticPolicy("bash", "write"))
	g.Grant("s1", Rule{Tool: "write"})

	if got := g.Decide("s1", tool.Call{Name: "write"}); got != Allow {
		t.Errorf("Decide(s1, write) = %v, want Allow", got)
	}
	for _, other := range []string{"s2", "child-1", ""} {
		if got := g.Decide(other, tool.Call{Name: "write"}); got != Ask {
			t.Errorf("Decide(%q, write) = %v, want Ask: grants do not cross sessions", other, got)
		}
	}
}

// TestSessionGrants_DoNotOverruleTheBase: a grant can only skip a question. A
// policy that allows or denies outright is untouched, so a future deny rule
// cannot be granted away.
func TestSessionGrants_DoNotOverruleTheBase(t *testing.T) {
	g := NewSessionGrants(policyFunc(func(string, tool.Call) Decision { return Deny }))
	g.Grant("s1", Rule{Tool: "write"})
	if got := g.Decide("s1", tool.Call{Name: "write"}); got != Deny {
		t.Errorf("Decide(write) = %v, want Deny: a grant must not overrule the base policy", got)
	}
}

// TestSessionGrants_IgnoresUngrantableInput keeps the store clean: a zero rule
// (the call was not grantable) or an empty session records nothing, so nothing
// can be allowed by accident.
func TestSessionGrants_IgnoresUngrantableInput(t *testing.T) {
	g := NewSessionGrants(NewStaticPolicy("bash", "write"))
	g.Grant("s1", Rule{})
	g.Grant("", Rule{Tool: "write"})
	if got := g.Decide("s1", tool.Call{Name: "write"}); got != Ask {
		t.Errorf("Decide(write) = %v, want Ask", got)
	}
	if got := g.Decide("", tool.Call{Name: "write"}); got != Ask {
		t.Errorf("Decide(\"\", write) = %v, want Ask", got)
	}
}

// TestSessionGrants_ConcurrentGrantAndDecide covers the real access pattern
// under -race: the runner decides a turn's calls from several goroutines while
// the UI adds grants.
func TestSessionGrants_ConcurrentGrantAndDecide(t *testing.T) {
	g := NewSessionGrants(NewStaticPolicy("bash", "write"))
	var wg sync.WaitGroup
	for index := 0; index < 20; index++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			g.Grant("s1", Rule{Tool: "write"})
		}()
		go func() {
			defer wg.Done()
			g.Decide("s1", bashCall("go test ./..."))
		}()
	}
	wg.Wait()
	if got := g.Decide("s1", tool.Call{Name: "write"}); got != Allow {
		t.Errorf("Decide(write) = %v, want Allow", got)
	}
}

// policyFunc adapts a function to Policy for the tests.
type policyFunc func(sessionID string, call tool.Call) Decision

func (f policyFunc) Decide(sessionID string, call tool.Call) Decision { return f(sessionID, call) }

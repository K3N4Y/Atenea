package permission

import (
	"context"
	"testing"
	"time"

	"github.com/K3N4Y/atenea/internal/tool"
)

// TestUnattendedPolicy_NeverAsks is the contract of the whole type: there is
// nobody to ask, so no input may produce Ask. Every other test here is about
// which of the remaining two answers is right.
func TestUnattendedPolicy_NeverAsks(t *testing.T) {
	catalog := shippedCatalog()
	budgets := []tool.Effects{
		tool.NoEffects,
		tool.WritesFiles,
		tool.WritesFiles | tool.RunsCommands | tool.ReachesNetwork,
	}
	for _, budget := range budgets {
		p := NewUnattendedPolicy(catalog, budget)
		for _, name := range append(catalog.Names(), "nope", "") {
			if got := p.Decide("s1", tool.Call{Name: name}); got == Ask {
				t.Errorf("Decide(%q) with budget %v = Ask; an unattended host cannot ask", name, budget)
			}
		}
	}
}

// TestUnattendedPolicy_EmptyBudgetAllowsOnlyDeclaredNoEffects is the deny mode of
// the headless CLI. It is not a policy that refuses everything: the whole
// read-only half of the catalog declares tool.NoEffects and still runs, which is
// what makes an unattended investigation possible without granting anything.
func TestUnattendedPolicy_EmptyBudgetAllowsOnlyDeclaredNoEffects(t *testing.T) {
	p := NewUnattendedPolicy(shippedCatalog(), tool.NoEffects)

	cases := []struct {
		name string
		want Decision
	}{
		{"read", Allow},
		{"glob", Allow},
		{"grep", Allow},
		{"skill", Allow},
		{"todo_write", Allow},
		{"present_plan", Allow},
		{"task", Allow},
		{"bash", Deny},
		{"write", Deny},
		{"edit", Deny},
		{"web_fetch", Deny},
	}
	for _, tc := range cases {
		if got := p.Decide("s1", tool.Call{Name: tc.name}); got != tc.want {
			t.Errorf("Decide(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestUnattendedPolicy_BudgetIsASubsetTest: a call is allowed when every effect
// its tool declares is inside the budget, and refused when any one of them is
// outside it. The budget is a ceiling on consequences, not a list of matches.
func TestUnattendedPolicy_BudgetIsASubsetTest(t *testing.T) {
	catalog := shippedCatalog()
	catalog["both"] = declaring("both", tool.WritesFiles|tool.ReachesNetwork)

	files := NewUnattendedPolicy(catalog, tool.WritesFiles)
	if got := files.Decide("s1", tool.Call{Name: "write"}); got != Allow {
		t.Errorf("Decide(write) with a writes-files budget = %v, want Allow", got)
	}
	if got := files.Decide("s1", tool.Call{Name: "bash"}); got != Deny {
		t.Errorf("Decide(bash) with a writes-files budget = %v, want Deny", got)
	}
	if got := files.Decide("s1", tool.Call{Name: "both"}); got != Deny {
		t.Errorf("Decide(writes-files|reaches-network) with a writes-files budget = %v, want Deny", got)
	}

	wider := NewUnattendedPolicy(catalog, tool.WritesFiles|tool.ReachesNetwork)
	if got := wider.Decide("s1", tool.Call{Name: "both"}); got != Allow {
		t.Errorf("Decide(writes-files|reaches-network) with both allowed = %v, want Allow", got)
	}
}

func TestUnattendedPolicy_UsesPerCallEffects(t *testing.T) {
	c := catalog{"deep": callDeclaringTool{declaring("deep", tool.NoEffects)}}
	readOnly := tool.Call{Name: "deep"}
	mutating := tool.Call{Name: "deep", Input: []byte(`{"write":true}`)}

	strict := NewUnattendedPolicy(c, tool.NoEffects)
	if got := strict.Decide("s1", readOnly); got != Allow {
		t.Fatalf("read-only call = %v, want Allow", got)
	}
	if got := strict.Decide("s1", mutating); got != Deny {
		t.Fatalf("mutating call = %v, want Deny", got)
	}
	if got := NewUnattendedPolicy(c, tool.WritesFiles).Decide("s1", mutating); got != Allow {
		t.Fatalf("mutating call with write budget = %v, want Allow", got)
	}
}

// TestUnattendedPolicy_UndeclaredToolIsDeniedByEveryBudget is the security
// property that makes the allowlist mode worth having next to auto: even with
// every effect this build knows allowed, a tool that says nothing about itself is
// refused. Silence is not evidence.
func TestUnattendedPolicy_UndeclaredToolIsDeniedByEveryBudget(t *testing.T) {
	everything := tool.WritesFiles | tool.RunsCommands | tool.ReachesNetwork
	p := NewUnattendedPolicy(shippedCatalog(), everything)
	if got := p.Decide("s1", tool.Call{Name: "mcp_github_create_issue"}); got != Deny {
		t.Errorf("Decide(mcp tool) with every effect allowed = %v, want Deny", got)
	}
}

// TestUnattendedPolicy_UnknownEffectIsOutsideEveryBudget: an operator can only
// name the flags this binary can spell, so a tool declaring one from a newer
// vocabulary can never be inside the budget. Adding a flag can only make this
// policy more careful.
func TestUnattendedPolicy_UnknownEffectIsOutsideEveryBudget(t *testing.T) {
	const future = tool.Effects(1 << 15)
	everything := tool.WritesFiles | tool.RunsCommands | tool.ReachesNetwork
	p := NewUnattendedPolicy(catalog{"x": declaring("x", future)}, everything)
	if got := p.Decide("s1", tool.Call{Name: "x"}); got != Deny {
		t.Errorf("Decide(tool declaring an unknown effect) = %v, want Deny", got)
	}
}

// TestUnattendedPolicy_UnregisteredToolIsAllowed keeps the reasoning
// EffectsPolicy applies: Settle refuses a name the registry does not know before
// executing anything, so denying here would tell the model its call was refused
// when the truth is that it named a tool that does not exist.
func TestUnattendedPolicy_UnregisteredToolIsAllowed(t *testing.T) {
	p := NewUnattendedPolicy(shippedCatalog(), tool.NoEffects)
	for _, name := range []string{"nope", "Bash", ""} {
		if got := p.Decide("s1", tool.Call{Name: name}); got != Allow {
			t.Errorf("Decide(%q) = %v, want Allow: an unregistered name is refused by Settle", name, got)
		}
	}
}

// TestUnattendedPolicy_NilCatalogDeniesEverything: without a catalog nothing can
// be shown to be within budget, so nothing is.
func TestUnattendedPolicy_NilCatalogDeniesEverything(t *testing.T) {
	p := NewUnattendedPolicy(nil, tool.WritesFiles|tool.RunsCommands|tool.ReachesNetwork)
	if got := p.Decide("s1", tool.Call{Name: "read"}); got != Deny {
		t.Errorf("Decide(read) with a nil catalog = %v, want Deny", got)
	}
}

// TestUnrestrictedPolicy_AllowsWhatEveryBudgetRefuses pins the one difference
// that justifies the auto mode existing as its own thing: the tool that declared
// nothing runs.
func TestUnrestrictedPolicy_AllowsWhatEveryBudgetRefuses(t *testing.T) {
	var p UnrestrictedPolicy
	for _, name := range []string{"bash", "write", "mcp_github_create_issue", "nope", ""} {
		if got := p.Decide("s1", tool.Call{Name: name}); got != Allow {
			t.Errorf("Decide(%q) = %v, want Allow", name, got)
		}
	}
}

// TestUnattendedGate_RefusesWithoutBlocking: the gate an unattended host installs
// so refusal is enforced even if a policy asks. It must answer, not block, and the
// answer must be a denial rather than an error — an error leaves the call unsettled
// for the turn's cleanup, which describes a stop rather than a refusal.
func TestUnattendedGate_RefusesWithoutBlocking(t *testing.T) {
	type answer struct {
		approved bool
		err      error
	}
	answers := make(chan answer, 1)
	go func() {
		approved, err := UnattendedGate{}.Ask(context.Background(), Request{SessionID: "s1", CallID: "c1"})
		answers <- answer{approved, err}
	}()
	var got answer
	select {
	case got = <-answers:
	case <-time.After(2 * time.Second):
		t.Fatal("UnattendedGate.Ask blocked; a headless host must never wait for an answer nobody can give")
	}
	approved, err := got.approved, got.err
	if approved {
		t.Error("UnattendedGate approved a request; there is nobody to approve it")
	}
	if err != nil {
		t.Errorf("UnattendedGate returned err = %v, want nil so the call fails as denied", err)
	}
}

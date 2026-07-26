package permission

import (
	"context"

	"github.com/K3N4Y/atenea/internal/tool"
)

// UnattendedPolicy classifies tool calls for a host that has nobody to ask. It
// never returns Ask: every call is decided from what its tool declares about
// itself, measured against an effect budget the operator stated up front.
//
// It is the counterpart of EffectsPolicy rather than a variant of it. Both derive
// the decision from tool.Effects, but they answer different questions: with a user
// present, "I cannot show this to be harmless" means *ask*, and the interesting
// design work is in what a prompt costs. With no user present, that same sentence
// means *refuse*, because the only other option is to run it on nobody's
// authority.
//
// The budget is a set of effects, not a set of tool names, and that is the whole
// point. A name list is the disease R2 cured — a host deciding what a tool is
// allowed to do by recognizing its name, with a new tool arriving either
// unclassified or invisible. An effect budget is a claim about consequences: "this
// run may write files, and may not run commands". A tool joins it by declaring
// what it does, an MCP server's tool included, and nothing in this package has to
// have heard of it.
//
// The catalog is consulted on every decision, so it must be the registry the call
// will be settled against.
type UnattendedPolicy struct {
	catalog tool.Catalog
	allowed tool.Effects
}

// NewUnattendedPolicy builds the classification over the registry that will
// settle the calls, allowing a call whose tool declares effects within allowed.
//
// allowed = tool.NoEffects is the strictest useful setting rather than a
// degenerate one: it still runs every tool that declares it affects nothing
// outside the conversation, which is the whole read-only half of the catalog
// (read, glob, grep, skill, todo_write). That is what makes an unattended
// investigation possible without granting anything.
//
// A nil catalog denies everything: nothing can be shown to be within budget, so
// nothing is. It is the same fail-safe EffectsPolicy applies with a nil catalog,
// expressed in the vocabulary of a host that cannot ask.
func NewUnattendedPolicy(catalog tool.Catalog, allowed tool.Effects) UnattendedPolicy {
	return UnattendedPolicy{catalog: catalog, allowed: allowed}
}

// Decide answers from the tool's own declaration. The session plays no part in
// it: an unattended run has no grants, because a grant is an answer a user gave.
//
// Two cases are worth the words:
//
// A tool that declared *nothing* is denied. This is the R2 rule read in the
// unattended direction — silence is not evidence — and it is what keeps the
// budget honest: an operator who allows writing files has authorized the tools
// that say they write files, not every tool that might. Everything an MCP server
// contributes lands here until it declares itself.
//
// A tool declaring an effect this build cannot name is denied too, and falls out
// of the subset test rather than needing a case: the operator can only spell the
// flags this binary knows, so an unrecognized one can never be inside the budget.
// Adding a flag to the vocabulary can therefore only ever leave this policy more
// careful.
//
// A name the catalog does not know is allowed, for the reason EffectsPolicy.Decide
// spells out: Settle refuses an unregistered tool before executing anything, so
// the decision only picks which failure is reported, and "unknown tool" is the one
// the model can act on.
func (p UnattendedPolicy) Decide(_ string, call tool.Call) Decision {
	if p.catalog == nil {
		return Deny
	}
	t, ok := p.catalog.Lookup(call.Name)
	if !ok {
		return Allow
	}
	effects, declared := tool.EffectsOf(t)
	if !declared {
		return Deny
	}
	if effects&^p.allowed != 0 {
		return Deny
	}
	return Allow
}

// UnrestrictedPolicy allows every tool call, including one whose tool declared
// nothing at all. It takes no catalog, and the absence is the statement: it does
// not read what a tool says about itself, because the answer would not change
// anything.
//
// It is a separate type from UnattendedPolicy, and not that policy with every
// flag set, because the two differ on the case that matters. A full budget still
// refuses a tool that declared nothing; this one runs it. That is the honest
// difference between deciding on evidence and deciding in its absence, and it is
// why a host must not present the two as neighbouring settings on one dial.
type UnrestrictedPolicy struct{}

// Decide allows unconditionally.
func (UnrestrictedPolicy) Decide(string, tool.Call) Decision { return Allow }

// UnattendedGate is the gate of a host with no user: it answers every request
// immediately, and the answer is no.
//
// It exists because refusal has to be enforced in two places to be enforced at
// all. The runner only consults its policy when a gate is present (a nil gate
// means "gate nothing"), so an unattended host that passed no gate would settle
// every call regardless of what its policy decided. And a gate that could block —
// the interactive MemoryGate, say — would hang the turn forever the first time
// some policy returned Ask, which for a CI job is the one failure worse than a
// wrong answer.
//
// So an Ask reaching this gate is a bug in whichever policy produced it, and the
// safe response to a bug in a permission decision is to refuse and let the model
// report it. The call fails as a denied call; the turn continues.
type UnattendedGate struct{}

// Ask refuses without blocking and without an error. The error path would leave
// the call unsettled for the turn's cleanup to close as interrupted, which
// describes a stop rather than a refusal; (false, nil) is the denial the runner
// already knows how to publish.
func (UnattendedGate) Ask(context.Context, Request) (bool, error) { return false, nil }

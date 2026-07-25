package permission

import "github.com/K3N4Y/atenea/internal/tool"

// EffectsPolicy classifies a tool call by what its tool declares its calls affect
// (tool.Effects), rather than by a list of names the host keeps in sync by hand.
// A tool that declares no effect outside the conversation is allowed; a tool that
// declares any effect is asked about; and a tool that declares nothing at all is
// asked about too, because there is no basis for treating it as harmless.
//
// That last clause is the one that matters. It is the default for every tool this
// build has never seen — everything an MCP server contributes — and it makes
// adding an effect to the vocabulary a change that can only ever leave the host
// more careful, never less. A tool becomes unattended by saying what it does,
// which is a claim its author signs, not by being added to a list somewhere else
// in the tree.
//
// The catalog is consulted on every decision, so it has to be the same registry
// the call will be settled against. Deny is not expressible here; it arrives with
// rule-based policies.
type EffectsPolicy struct{ catalog tool.Catalog }

// NewEffectsPolicy builds the classification over the registry that will settle
// the calls. A nil catalog asks about everything: nothing can be shown to be
// harmless, so nothing is.
func NewEffectsPolicy(catalog tool.Catalog) EffectsPolicy {
	return EffectsPolicy{catalog: catalog}
}

// Decide asks the tool about itself. The classification does not depend on the
// session, so the session id plays no part in it.
//
// A name the catalog does not know is allowed on purpose, and it is the one case
// worth explaining. Such a call cannot run either way — Settle refuses an
// unregistered tool before executing anything, with no side effects — so the
// decision only picks which failure the model and the user see. Asking would put
// a permission prompt on screen for a call that can never happen. Denying would
// tell the model its call was refused, when the truth is that it named a tool
// that does not exist, which is something it can fix on the next turn.
func (p EffectsPolicy) Decide(_ string, call tool.Call) Decision {
	if p.catalog == nil {
		return Ask
	}
	t, ok := p.catalog.Lookup(call.Name)
	if !ok {
		return Allow
	}
	effects, declared := tool.EffectsOf(t)
	if !declared {
		return Ask
	}
	if effects == tool.NoEffects {
		return Allow
	}
	return Ask
}

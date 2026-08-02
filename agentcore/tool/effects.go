package tool

import "strings"

// Effects is the set of things a tool's calls can affect beyond the
// conversation. A tool declares it once, as a fact about itself, and the host
// decides what to do with that fact: which effects are worth interrupting the
// user for, which ones mean the workspace has to be re-read afterwards. The
// split matters — a tool knows what it does, only the host knows how cautious
// this deployment wants to be about it.
//
// The vocabulary is deliberately narrow: only effects a host has a distinct
// reaction to are flags. Reading files is not one, because nothing changes if a
// tool reads; neither is mutating the agent's own state (a todo list, a plan),
// because that never escapes the session. Both of those are NoEffects.
//
// New flags may be added, and a host must treat an unknown flag as one more
// reason to be careful rather than as none: for anything it cannot recognize,
// fail safe.
type Effects uint16

const (
	// WritesFiles: the call creates, modifies or deletes files in the
	// workspace.
	WritesFiles Effects = 1 << iota
	// RunsCommands: the call executes something neither the host nor the tool
	// wrote — a shell command, a subprocess. It is the widest flag there is:
	// the real effects are whatever that executes, so it implies every other
	// one and a tool that declares it need not also declare them.
	RunsCommands
	// ReachesNetwork: the call contacts a destination taken from its input,
	// on the model's behalf. The host's own connections — to the model
	// provider, say — are not this: they are the host's, already accounted
	// for, and not chosen by the call.
	ReachesNetwork
)

// NoEffects is the empty set: the call affects nothing outside the conversation.
// It is a declaration, not the absence of one — a tool that returns it says "I
// only read, or I only touch the agent's own state". A tool that says nothing at
// all (does not implement Declaring) is a different case, and a host must not
// confuse the two.
const NoEffects Effects = 0

// Declaring is the optional interface a tool implements to declare its Effects.
//
// It is optional so the Tool contract stays a four-method interface, and it is
// discovered by type assertion:
//
//	if d, ok := t.(tool.Declaring); ok { effects = d.Effects() }
//
// A tool that does not implement it has declared nothing, which is not the same
// as declaring NoEffects: the host has no basis to treat it as harmless and must
// fail safe. Implement it — it is how a tool becomes first-class instead of
// merely runnable.
type Declaring interface {
	Tool
	Effects() Effects
}

// CallDeclaring optionally refines a tool's effects for one call. Deep tools
// that combine read-only and mutating operations use it to keep harmless calls
// unattended without understating the mutating operation.
type CallDeclaring interface {
	Tool
	CallEffects(Call) Effects
}

// EffectsOf returns what t declares its calls affect, and whether t declared
// anything at all. It is the one place a host should resolve the optional
// interface, so "undeclared" and "declares nothing" never get flattened into the
// same value by accident.
func EffectsOf(t Tool) (Effects, bool) {
	d, ok := t.(Declaring)
	if !ok {
		return NoEffects, false
	}
	return d.Effects(), true
}

// EffectsForCall returns the most specific effects t declares for call. A
// per-call declaration takes precedence over the tool-wide declaration.
func EffectsForCall(t Tool, call Call) (Effects, bool) {
	if d, ok := t.(CallDeclaring); ok {
		return d.CallEffects(call), true
	}
	return EffectsOf(t)
}

// Any reports whether e includes at least one of the flags in of. It is the
// question a host actually asks — "does this touch the filesystem or run
// anything?" — rather than an equality test against a specific combination.
func (e Effects) Any(of Effects) bool { return e&of != 0 }

// String renders the set for a log line or a test failure: the flag names joined
// by "|", "none" for the empty set, and any bit this build does not know about
// as "unknown" so an unrecognized effect is visible rather than dropped.
func (e Effects) String() string {
	named := []struct {
		flag Effects
		name string
	}{
		{WritesFiles, "writes-files"},
		{RunsCommands, "runs-commands"},
		{ReachesNetwork, "reaches-network"},
	}
	var parts []string
	rest := e
	for _, n := range named {
		if e&n.flag != 0 {
			parts = append(parts, n.name)
			rest &^= n.flag
		}
	}
	if rest != 0 {
		parts = append(parts, "unknown")
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, "|")
}

package cli

import (
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/tool"
)

// The permission modes, spelled as a user types them.
const (
	modeDeny      = "deny"
	modeAllowlist = "allowlist"
	modeAuto      = "auto"
)

// permissionModes lists them safest first, which is also the order the help shows
// and the order of the argument that the default is the first of.
var permissionModes = []string{modeDeny, modeAllowlist, modeAuto}

// permissionMode is a resolved --permission-mode: the classification it installs
// over the assembly's catalog, and the warning a user must be shown while it is in
// force.
//
// A mode is a policy factory rather than a policy because the classification has
// to see the registry of the moment — the reason wiring.Config.Policy has that
// shape. See .okf/architecture/composition-root.md.
type permissionMode struct {
	name   string
	policy func(tool.Catalog) permission.Policy
	// warning is written to stderr before the first turn when the mode is in force.
	// Empty means the mode needs no warning, which is true of exactly the modes
	// that refuse something.
	warning string
}

// resolvePermissionMode turns the two flags into the mode they name.
//
// Every mode here is honest about what it does, and the two rules that make that
// true are both refusals rather than defaults:
//
//   - allowlist with no effects allowed is a usage error, not deny under a second
//     name. A mode that silently behaves like another one is worse than a missing
//     mode: it makes a CI configuration that reads as permissive behave as strict,
//     and nothing in the output would say so.
//   - --allow-effects outside allowlist is a usage error too. It is the same rule
//     from the other end — a flag that would be silently ignored is a flag that
//     lies about what the run allowed.
func resolvePermissionMode(name, effects string) (permissionMode, error) {
	switch name {
	case modeDeny:
		if effects != "" {
			return permissionMode{}, fmt.Errorf("--allow-effects only applies to --permission-mode %s", modeAllowlist)
		}
		return permissionMode{
			name: modeDeny,
			// The strictest budget still runs every tool that declares it affects
			// nothing outside the conversation, so an unattended investigation works
			// with nothing granted. A call outside it is refused and the model is told,
			// which is what lets it adapt — report the change instead of making it.
			policy: func(catalog tool.Catalog) permission.Policy {
				return permission.NewUnattendedPolicy(catalog, tool.NoEffects)
			},
		}, nil
	case modeAllowlist:
		allowed, err := parseEffects(effects)
		if err != nil {
			return permissionMode{}, err
		}
		if allowed == tool.NoEffects {
			return permissionMode{}, fmt.Errorf("--permission-mode %s requires --allow-effects (one or more of %s); "+
				"with nothing allowed it would be --permission-mode %s under another name",
				modeAllowlist, strings.Join(effectNames(), ", "), modeDeny)
		}
		return permissionMode{
			name: modeAllowlist,
			policy: func(catalog tool.Catalog) permission.Policy {
				return permission.NewUnattendedPolicy(catalog, allowed)
			},
		}, nil
	case modeAuto:
		if effects != "" {
			return permissionMode{}, fmt.Errorf("--allow-effects only applies to --permission-mode %s", modeAllowlist)
		}
		return permissionMode{
			name:   modeAuto,
			policy: func(tool.Catalog) permission.Policy { return permission.UnrestrictedPolicy{} },
			warning: "permission-mode auto: every tool call runs unattended, including tools that declare " +
				"nothing about what they do. Nothing in this run will ask.",
		}, nil
	default:
		return permissionMode{}, fmt.Errorf("unknown permission mode %q; valid modes are %s",
			name, strings.Join(permissionModes, ", "))
	}
}

// effectNames lists the effects this build can allow, spelled exactly as
// tool.Effects spells them.
//
// It is derived from Effects.String() rather than restated, so the CLI accepts a
// flag added to the vocabulary on the commit that defines it and cannot misspell
// one. A single unknown bit renders as "unknown", which is what marks the end of
// what this build knows.
func effectNames() []string {
	names := make([]string, 0, 4)
	for bit := 0; bit < effectBits; bit++ {
		name := tool.Effects(1 << bit).String()
		if name == unknownEffect {
			continue
		}
		names = append(names, name)
	}
	return names
}

// effectBits is the width of tool.Effects. A flag beyond it cannot be declared, so
// it cannot be allowed either.
const effectBits = 16

// unknownEffect is what tool.Effects.String() renders a bit it does not know as.
const unknownEffect = "unknown"

// parseEffects reads the comma-separated budget. An unknown name is an error that
// lists what this build does know, rather than a silently narrower budget than the
// operator asked for.
func parseEffects(list string) (tool.Effects, error) {
	var allowed tool.Effects
	for _, field := range strings.Split(list, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		flag, ok := effectNamed(field)
		if !ok {
			return 0, fmt.Errorf("unknown effect %q; valid effects are %s",
				field, strings.Join(effectNames(), ", "))
		}
		allowed |= flag
	}
	return allowed, nil
}

func effectNamed(name string) (tool.Effects, bool) {
	for bit := 0; bit < effectBits; bit++ {
		flag := tool.Effects(1 << bit)
		if flag.String() == name {
			return flag, true
		}
	}
	return 0, false
}

// denials counts the tool calls a run's permission mode refused, which is what
// the closing document reports and what decides the ExitPermissionDenied code.
//
// The count is taken at the decision rather than by recognizing denials in the
// event stream. A denial reaches the stream as a Tool.Failed carrying a message, so
// reading it back means matching that message — the string coupling the TUI's
// transcript already has and that R5 exists to remove. An exit code must not be
// built on it.
type denials struct{ n atomic.Int64 }

// policy wraps a classification so the calls it refuses are counted. It outlives
// any single assembly: the counter is shared, while base belongs to the registry
// the classification was built over.
func (d *denials) policy(base permission.Policy) permission.Policy {
	return countingPolicy{base: base, denials: d}
}

// over adapts a mode's factory into wiring's, counting whatever the
// classification built over that catalog refuses. Nothing is assigned after
// construction, so a caller that assembles more than once gets one counter and no
// shared mutable field.
func (d *denials) over(mode func(tool.Catalog) permission.Policy) func(tool.Catalog) permission.Policy {
	return func(catalog tool.Catalog) permission.Policy {
		return d.policy(mode(catalog))
	}
}

func (d *denials) count() int { return int(d.n.Load()) }

type countingPolicy struct {
	base    permission.Policy
	denials *denials
}

func (p countingPolicy) Decide(sessionID string, call tool.Call) permission.Decision {
	decision := p.base.Decide(sessionID, call)
	if decision == permission.Deny {
		p.denials.n.Add(1)
	}
	return decision
}

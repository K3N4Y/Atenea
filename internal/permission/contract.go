package permission

import contract "github.com/K3N4Y/atenea/agentcore/permission"

// The ask-before-run contract lives in agentcore/permission: Policy, Gate and
// the vocabulary they answer in. This package is its private side: the
// classification atenea ships, the session grants layered over it, the in-memory
// gate broker, and the derivation of a grant from a specific tool's input. The
// aliases below re-export the contract so implementation code keeps one spelling
// (permission.Decision, permission.Rule) whichever side of the boundary a type
// is defined on.
//
// A new contract type belongs in agentcore/permission and gets an alias here; a
// new implementation detail belongs here and nowhere else. R2's tool-declared
// capability interfaces belong on the contract side too — defined there rather
// than in agentcore/tool, so the dependency stays permission -> tool and never
// becomes a cycle.

type (
	Decision  = contract.Decision
	Policy    = contract.Policy
	Gate      = contract.Gate
	Request   = contract.Request
	Verdict   = contract.Verdict
	Rule      = contract.Rule
	Grantable = contract.Grantable
)

const (
	Ask   = contract.Ask
	Allow = contract.Allow
	Deny  = contract.Deny

	Denied         = contract.Denied
	AllowedOnce    = contract.AllowedOnce
	AllowedSession = contract.AllowedSession
)

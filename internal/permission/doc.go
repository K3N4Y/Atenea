// Package permission is the private side of the ask-before-run boundary: the
// classification atenea ships, the session grants layered over it, the in-memory
// gate broker, and the derivation of a grant from a tool's input. The contract
// itself — Policy, Gate, Decision, Verdict, Rule — is published in
// agentcore/permission and re-exported here by contract.go.
//
// StaticPolicy is the base classification by tool name, wired once in
// internal/wiring and shared by the main runner and the subagents, so a child
// cannot evade the gate the main chat enforces. SessionGrants layers the user's
// "allow this for the rest of the session" answers over it (a bash command
// prefix, or a whole filesystem-mutating tool), so a granted call is allowed
// without ever reaching the gate and without leaving a permission request in the
// session's history. MemoryGate is the broker the UI resolves against.
//
// RuleFor is where a call becomes a grant, and it is deliberately conservative:
// only a bash command that reduces to an honest prefix and the local filesystem
// mutations are grantable. A grant must never claim more than what the user was
// shown.
package permission

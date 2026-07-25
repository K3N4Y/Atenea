// Package permission is the published contract of the ask-before-run boundary.
//
// Two seams, one per side of the question. A Policy classifies a tool call
// before it settles — Allow, Ask or Deny — and a Gate blocks a call that must
// ask until the user's answer arrives. A host consults both before settling any
// tool call, and both are implementable from outside: a persisted rule set, a
// permission mode, a headless gate that answers from a flag instead of a person.
//
// Rule is the shape of a session grant: what "allow this for the rest of the
// session" authorized. A policy that wants to skip the question for calls the
// user already approved matches against rules; a UI shows Rule.Label as the
// subject it is about to grant.
//
// Deliberately not here: the classification atenea ships, the in-memory gate
// broker, and the derivation of a rule from a specific tool's input. Those are
// private under internal/permission, because which tools ask and how a bash
// command is reduced to a grantable prefix are decisions of this product, not of
// the contract.
package permission

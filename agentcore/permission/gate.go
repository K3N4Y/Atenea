package permission

import "context"

// Request describes the tool call waiting for user approval (ask-before-run). A
// host builds it and hands it to the Gate; the implementation correlates it by
// (SessionID, CallID) with the answer arriving from wherever the user is.
type Request struct {
	SessionID string
	CallID    string
	ToolName  string
	Input     []byte // raw JSON input of the tool call (informational, for display)
}

// Gate is the ask-before-run boundary: Ask blocks until the user approves or
// denies the tool call, or the ctx is cancelled. A nil Gate never asks, so a
// host that cannot ask must not classify anything as Ask.
//
// A cancelled ctx (a stop) must unblock Ask with an error rather than leaving
// the turn hanging.
type Gate interface {
	Ask(ctx context.Context, req Request) (approved bool, err error)
}

// Verdict is the user's answer to a Request. It is the UI's vocabulary rather
// than the policy's: the gate only cares whether the call may run, and it is the
// host that turns AllowedSession into a grant so calls of the same shape stop
// asking for the rest of the session.
type Verdict int

const (
	// Denied fails the call. Zero value: an unanswered request denies.
	Denied Verdict = iota
	// AllowedOnce runs this call and nothing more.
	AllowedOnce
	// AllowedSession runs this call and grants its shape (see Rule) for the rest
	// of the session.
	AllowedSession
)

// Approved reports whether the verdict lets the call run.
func (v Verdict) Approved() bool { return v != Denied }

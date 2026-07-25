package permission

// Rule is the shape of the tool calls a session grant covers: the tool it
// applies to and, when the grant is narrower than the whole tool, the input
// prefix the user authorized (a bash command prefix, for instance). An empty
// Prefix grants the tool itself for the rest of the session.
//
// It is comparable on purpose, so a host stores grants as values and recognizes
// a repeated grant without any matching logic. Deriving a rule from a given
// call, and deciding whether a call is grantable at all, is the tool's business
// rather than the contract's: a grant must never claim more than what the user
// was shown.
type Rule struct {
	Tool   string
	Prefix string
}

// Label names the subject the rule authorizes, for a UI's action row ("go test",
// "write").
func (r Rule) Label() string {
	if r.Prefix != "" {
		return r.Prefix
	}
	return r.Tool
}

package llm

import "context"

// OAuthToken is one resolved OAuth credential: the bearer token a request
// carries, plus the account the endpoint routes it to.
//
// Both halves travel together because they are one credential. The account id is
// a header the issuer told us to send back, not something an adapter may derive,
// and a plain string could only have carried the first half — which is the
// concrete reason the seam below exists at all rather than an apiKey argument.
type OAuthToken struct {
	AccessToken string
	AccountID   string
}

// OAuthTokenSource hands an adapter the credential for ONE request.
//
// It is a seam rather than a value because an OAuth access token lives about an
// hour and a conversation outlives that. A credential resolved once, when the
// model was selected, turns the second hour of a session into the endpoint's own
// 401 — and the only recovery a user has is to re-select the model. Asking here
// means the renewal happens where the request is, at the moment it is needed.
//
// An implementation owns everything that makes the renewal safe: refreshing
// before the token expires rather than after, doing it once when several turns
// notice at the same moment, and persisting a rotated refresh token. An adapter
// owns none of it and must not cache what it gets back.
//
// It is called on every request, from the goroutine streaming a turn, and one
// adapter serves several concurrent turns — so it must be safe for concurrent
// use. A failure it cannot fix is an error a user can act on ("log in again"),
// never a silent empty token.
type OAuthTokenSource interface {
	OAuthToken(ctx context.Context) (OAuthToken, error)
}

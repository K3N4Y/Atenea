package providerconfig

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/openaiauth"
)

// DefaultOAuthRefreshMargin is how long before expiry a credential is renewed.
//
// It is generous on purpose. A token that expires mid-request fails the turn, and
// a turn is minutes long; renewing early costs one request against a subscription
// that is not billed per token. The number that would be wrong here is a small
// one.
const DefaultOAuthRefreshMargin = 5 * time.Minute

// oauthRefreshTimeout bounds a renewal that is deliberately not the caller's to
// cancel. It is generous because the transport has a tighter timeout of its own;
// what this one exists for is to keep a refresh that hangs past every layer from
// holding the provider's gate — and every turn queued behind it — forever.
const oauthRefreshTimeout = time.Minute

// OAuthRefresher renews an OAuth credential from its refresh token.
//
// It returns a whole [OAuthCredential] rather than just an access token because
// the refresh token ROTATES: what comes back is the credential to store, and a
// caller that keeps the old one has logged the user out at the next renewal.
type OAuthRefresher func(ctx context.Context, refreshToken string) (OAuthCredential, error)

// OAuthTokenSource binds this resolver to one provider, so an adapter can ask for
// a credential without knowing whose it is or where it is stored.
//
// It is the private side of [llm.OAuthTokenSource]: internal/llm defines what an
// adapter may ask for and this package answers it, which is what keeps the adapter
// package from having to know about credential storage, files and refresh
// protocols.
func (r *CredentialResolver) OAuthTokenSource(providerID string, refresh OAuthRefresher) llm.OAuthTokenSource {
	return oauthTokenSource{resolver: r, providerID: providerID, refresh: refresh}
}

type oauthTokenSource struct {
	resolver   *CredentialResolver
	providerID string
	refresh    OAuthRefresher
}

func (s oauthTokenSource) OAuthToken(ctx context.Context) (llm.OAuthToken, error) {
	return s.resolver.oauthToken(ctx, s.providerID, s.refresh)
}

// oauthToken resolves the credential one request will carry, renewing it first if
// it is about to expire.
//
// The renewals of one provider are serialized. Several turns share an adapter and
// all of them notice the same expiry at the same moment; without this, each would
// refresh, each would rotate the refresh token, and every rotation but the last
// would be invalidated by the next — so the stored credential would end up naming
// a refresh token the server has already retired. The waiters re-read the store
// after the gate, which is why the winner's renewal serves all of them instead of
// each doing its own.
func (r *CredentialResolver) oauthToken(ctx context.Context, providerID string, refresh OAuthRefresher) (llm.OAuthToken, error) {
	credential, err := r.storedOAuth(providerID)
	if err != nil {
		return llm.OAuthToken{}, err
	}
	if r.oauthFresh(credential) {
		return oauthToken(credential), nil
	}
	if refresh == nil {
		return llm.OAuthToken{}, fmt.Errorf("the credential for provider %q expired and this build cannot renew it; log in again", providerID)
	}

	gate := r.oauthGate(providerID)
	gate.Lock()
	defer gate.Unlock()

	// Another turn may have renewed while this one waited. Re-reading is also what
	// makes the refresh token used below the CURRENT one: the winner rotated it,
	// and the copy read before the wait is already dead.
	credential, err = r.storedOAuth(providerID)
	if err != nil {
		return llm.OAuthToken{}, err
	}
	if r.oauthFresh(credential) {
		return oauthToken(credential), nil
	}

	// The renewal does NOT run on the caller's context. ctx here is the turn's, and
	// the runner cancels it the moment the user presses Stop — which, in the window
	// between OpenAI writing the rotated pair and this process reading the body,
	// throws away a rotation the server has already committed to. The refresh token
	// left on disk is then one the server retired, and the next request pushes the
	// user back through the login. A rotation is one of the two operations here that
	// must outlive the click that triggered it; the other is the device-login
	// polling, detached the same way in devicelogin.go.
	refreshCtx, cancelRefresh := context.WithTimeout(context.WithoutCancel(ctx), oauthRefreshTimeout)
	defer cancelRefresh()
	renewed, err := refresh(refreshCtx, credential.RefreshToken)
	if err != nil {
		// The gate is in-process and the store is not. The TUI and the desktop app
		// notice the same expiry in the same second, both renew, and OpenAI retires
		// the refresh token on whichever lands first — so the loser reads
		// invalid_grant about a credential that is now perfectly good. Looking once
		// more before telling the user to log in again is what turns that race into
		// a no-op instead of a logout they have to undo by hand.
		if current, reread := r.storedOAuth(providerID); reread == nil && r.oauthFresh(current) {
			return oauthToken(current), nil
		}
		return llm.OAuthToken{}, fmt.Errorf("could not renew the credential for provider %q; log in again: %w", providerID, err)
	}
	next := Credential{Type: CredentialTypeOAuth, OAuth: &renewed}
	if err := next.Validate(); err != nil {
		return llm.OAuthToken{}, fmt.Errorf("renewed credential for provider %q: %w", providerID, err)
	}
	// The rotation is only real once it is on disk. Serving the token and failing
	// to persist it would leave a session that works for an hour and a stored
	// credential the server has already retired — a logout with no cause the user
	// could see. Reporting it now is the only version of this that is debuggable.
	if err := r.store.Put(providerID, next); err != nil {
		return llm.OAuthToken{}, fmt.Errorf("could not save the renewed credential for provider %q: %w", providerID, err)
	}
	return oauthToken(&renewed), nil
}

// storedOAuth reads the provider's credential and insists it is an oauth login.
// Both failures are the user's to act on, so both name what to do.
func (r *CredentialResolver) storedOAuth(providerID string) (*OAuthCredential, error) {
	credential, ok := r.stored(providerID)
	if !ok {
		return nil, fmt.Errorf("no credential stored for provider %q; run /connect to log in", providerID)
	}
	if credential.Type != CredentialTypeOAuth {
		return nil, fmt.Errorf("the credential stored for provider %q is a %s credential, and this provider authenticates with a login; run /connect", providerID, credential.Type)
	}
	if err := credential.Validate(); err != nil {
		return nil, fmt.Errorf("credential for provider %q: %w; log in again", providerID, err)
	}
	return credential.OAuth, nil
}

// oauthFresh reports whether the access token will still be accepted for long
// enough to be worth sending. A zero expiry is not fresh: an unknown lifetime and
// an expired one cost the same to get wrong, and one refresh settles it.
func (r *CredentialResolver) oauthFresh(credential *OAuthCredential) bool {
	if credential.ExpiresAt.IsZero() {
		return false
	}
	return r.now().Add(r.refreshMargin()).Before(credential.ExpiresAt)
}

func (r *CredentialResolver) refreshMargin() time.Duration {
	if r.oauthMargin > 0 {
		return r.oauthMargin
	}
	return DefaultOAuthRefreshMargin
}

func (r *CredentialResolver) oauthGate(providerID string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	gate, ok := r.gates[providerID]
	if !ok {
		gate = &sync.Mutex{}
		r.gates[providerID] = gate
	}
	return gate
}

func oauthToken(credential *OAuthCredential) llm.OAuthToken {
	return llm.OAuthToken{AccessToken: credential.AccessToken, AccountID: credential.AccountID}
}

// openAIOAuthFlow is how a ChatGPT subscription is obtained and kept: OpenAI's own
// device-code flow, against the issuer each provider declares.
func openAIOAuthFlow() *OAuthFlow {
	return &OAuthFlow{Refresh: openAIRefresher, Login: openAIDeviceLogin}
}

// openAIRefresher renews the ChatGPT subscription credential of one provider,
// against the issuer that provider declares.
func openAIRefresher(provider Provider) OAuthRefresher {
	client := openaiauth.NewClient(openaiauth.WithIssuer(provider.OAuthIssuer))
	return func(ctx context.Context, refreshToken string) (OAuthCredential, error) {
		tokens, err := client.Refresh(ctx, refreshToken)
		if err != nil {
			return OAuthCredential{}, err
		}
		return oauthCredential(tokens), nil
	}
}

// openAIDeviceLogin mints a code and hands back the wait that completes it.
func openAIDeviceLogin(ctx context.Context, provider Provider) (DeviceCode, error) {
	client := openaiauth.NewClient(openaiauth.WithIssuer(provider.OAuthIssuer))
	device, err := client.RequestDevice(ctx)
	if err != nil {
		return DeviceCode{}, err
	}
	return DeviceCode{
		UserCode:        device.UserCode,
		VerificationURI: device.VerificationURI,
		ExpiresAt:       device.ExpiresAt,
		Await: func(ctx context.Context) (OAuthCredential, error) {
			tokens, err := client.Await(ctx, device)
			if err != nil {
				return OAuthCredential{}, err
			}
			return oauthCredential(tokens), nil
		},
	}, nil
}

// oauthCredential is the storage shape of what a login or a renewal produced.
func oauthCredential(tokens openaiauth.Tokens) OAuthCredential {
	return OAuthCredential{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresAt:    tokens.ExpiresAt,
		AccountID:    tokens.AccountID,
	}
}

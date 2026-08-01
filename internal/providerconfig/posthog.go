package providerconfig

import (
	"context"

	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/posthogauth"
)

// posthogOAuthFlow is how a PostHog credential is obtained and kept: the
// authorization-code + PKCE flow against the cloud each provider declares as
// its issuer. It is a browser login, not a device code — the "code" the user
// approves lands back on a local port instead of being typed — but it maps
// onto the same DeviceCode shape: a page to open, a deadline, and a wait.
func posthogOAuthFlow() *OAuthFlow {
	return &OAuthFlow{Refresh: posthogRefresher, Login: posthogLogin}
}

// posthogRefresher renews the PostHog credential of one provider, against the
// issuer that provider declares.
func posthogRefresher(provider Provider) OAuthRefresher {
	client := posthogauth.NewClient(posthogauth.WithIssuer(provider.OAuthIssuer))
	return func(ctx context.Context, refreshToken string) (OAuthCredential, error) {
		tokens, err := client.Refresh(ctx, refreshToken)
		if err != nil {
			return OAuthCredential{}, err
		}
		return posthogCredential(tokens), nil
	}
}

// posthogLogin binds the callback listener and hands back the wait that
// completes the login. UserCode is empty on purpose: there is nothing to type,
// the browser carries the approval back to the listener, and a host reads the
// empty code as "open the page and wait".
func posthogLogin(ctx context.Context, provider Provider) (DeviceCode, error) {
	client := posthogauth.NewClient(posthogauth.WithIssuer(provider.OAuthIssuer))
	login, err := client.BeginLogin(ctx)
	if err != nil {
		return DeviceCode{}, err
	}
	return DeviceCode{
		UserCode:        "",
		VerificationURI: login.AuthorizeURL,
		ExpiresAt:       login.ExpiresAt,
		Await: func(ctx context.Context) (OAuthCredential, error) {
			tokens, err := login.Await(ctx)
			if err != nil {
				return OAuthCredential{}, err
			}
			return posthogCredential(tokens), nil
		},
	}, nil
}

// posthogCredential is the storage shape of what a login or a renewal
// produced. There is no account id: the gateway routes on the bearer alone.
func posthogCredential(tokens posthogauth.Tokens) OAuthCredential {
	return OAuthCredential{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresAt:    tokens.ExpiresAt,
	}
}

// posthogDiscover asks the gateway what it serves this account. It is the one
// format with a Discover of its own because the gateway gates models by plan —
// a curated list would offer models that fail at selection — and its models
// endpoint hangs off /v1 with a bearer, which the generic lister cannot know.
func posthogDiscover(ctx context.Context, def Provider, bearer string) ([]string, error) {
	return llm.ListPosthogModels(ctx, def.BaseURL, bearer)
}

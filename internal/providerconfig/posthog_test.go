package providerconfig

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/K3N4Y/atenea/internal/llm"
)

// posthogIssuer is a stub PostHog cloud: a token endpoint that answers every
// grant with a fresh rotated pair.
func posthogIssuer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		io.WriteString(w, `{"access_token":"at-rotated","refresh_token":"rt-rotated","expires_in":3600}`)
	}))
	t.Cleanup(server.Close)
	return server
}

// TestPosthogLogin_MapsThePKCEFlowOntoDeviceCode: no user code — the browser
// carries the approval back — the verification URI is the consent page, and the
// wait is cancellable. This is the shape the device-login service and both
// hosts consume, so it is the mapping that must hold.
//
// The login binds the real registered port; a machine where it is taken skips
// rather than fails, the same way the posthogauth suite does.
func TestPosthogLogin_MapsThePKCEFlowOntoDeviceCode(t *testing.T) {
	issuer := posthogIssuer(t)
	code, err := posthogLogin(context.Background(), Provider{ID: "posthog", OAuthIssuer: issuer.URL})
	if err != nil {
		if strings.Contains(err.Error(), "already in use") {
			t.Skipf("the callback port is taken on this machine: %v", err)
		}
		t.Fatalf("posthogLogin: %v", err)
	}
	if code.UserCode != "" {
		t.Errorf("UserCode = %q, want empty: there is nothing to type in this flow", code.UserCode)
	}
	if !strings.HasPrefix(code.VerificationURI, issuer.URL+"/oauth/authorize?") {
		t.Errorf("VerificationURI = %q, want the issuer's consent page", code.VerificationURI)
	}
	if !code.ExpiresAt.After(time.Now()) {
		t.Errorf("ExpiresAt = %v, want a live deadline", code.ExpiresAt)
	}
	if code.Await == nil {
		t.Fatal("a login with no wait cannot complete")
	}
	// Cancelling frees the listener; ErrLoginCancelled mapping needs the bare
	// context.Canceled back.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := code.Await(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Await = %v, want context.Canceled", err)
	}
}

// TestPosthogRefresher_RotationPersistsThroughTheResolver: the resolver notices
// an expired credential, renews through the posthog flow, and what lands back
// in the store is the rotated pair — with no account id, which the store now
// accepts.
func TestPosthogRefresher_RotationPersistsThroughTheResolver(t *testing.T) {
	issuer := posthogIssuer(t)
	provider := Provider{ID: "posthog", Type: Posthog, OAuthIssuer: issuer.URL}
	store := NewFileCredentialStore(filepath.Join(t.TempDir(), "credentials.json"))
	if err := store.Put("posthog", Credential{Type: CredentialTypeOAuth, OAuth: &OAuthCredential{
		AccessToken: "at-stale", RefreshToken: "rt-stale", ExpiresAt: time.Now().Add(-time.Minute),
	}}); err != nil {
		t.Fatalf("seed the store: %v", err)
	}
	resolver := NewCredentialResolver(store)

	token, err := resolver.OAuthTokenSource("posthog", posthogRefresher(provider)).OAuthToken(context.Background())
	if err != nil {
		t.Fatalf("OAuthToken: %v", err)
	}
	if token.AccessToken != "at-rotated" {
		t.Fatalf("AccessToken = %q, want the renewed one", token.AccessToken)
	}
	if token.AccountID != "" {
		t.Fatalf("AccountID = %q, want none: the gateway routes on the bearer alone", token.AccountID)
	}
	stored, ok := store.Get("posthog")
	if !ok || stored.OAuth == nil || stored.OAuth.RefreshToken != "rt-rotated" {
		t.Fatalf("stored credential = %+v, want the rotated pair persisted", stored)
	}
}

// TestRegistry_PosthogRefusesToBuildWithNoTokenSource: same property codex
// pins — an adapter with no credential seam could only produce 401s, and the
// refusal has to name the wiring.
func TestRegistry_PosthogRefusesToBuildWithNoTokenSource(t *testing.T) {
	_, err := DefaultRegistry().Build(BuildParams{Provider: Provider{ID: "posthog", Type: Posthog}, Model: "claude-opus-4-8"})
	if err == nil || !strings.Contains(err.Error(), "no credential source") {
		t.Fatalf("Build = %v, want a refusal naming the wiring", err)
	}
}

// TestService_DeviceLoginAcceptsACodelessLogin: the guard that used to insist
// on a user code now insists on what every flow owes — a page and a wait — so
// a browser-redirect login runs through the same service path, stores its
// credential and activates the provider.
func TestService_DeviceLoginAcceptsACodelessLogin(t *testing.T) {
	dir := t.TempDir()
	store := NewFileCredentialStore(filepath.Join(dir, "credentials.json"))
	registry := DefaultRegistry()
	registry[Posthog] = Format{
		Build: func(BuildParams) (llm.Provider, error) { return inertProvider{}, nil },
		OAuth: &OAuthFlow{Login: func(context.Context, Provider) (DeviceCode, error) {
			return DeviceCode{
				VerificationURI: "https://us.posthog.com/oauth/authorize?state=x",
				ExpiresAt:       time.Now().Add(time.Minute),
				Await: func(context.Context) (OAuthCredential, error) {
					return OAuthCredential{AccessToken: "at", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Hour)}, nil
				},
			}, nil
		}},
	}
	catalog := Config{Providers: []Provider{{ID: "posthog", Name: "PostHog", Type: Posthog, BaseURL: "https://gateway.test", DisableModelDiscovery: true, Models: []string{"claude-opus-4-8"}}}}
	offline := func(context.Context, string, string) ([]string, error) { return nil, errors.New("offline") }
	service, err := Open(context.Background(), filepath.Join(dir, "providers.json"), "", fallbackSnapshot(),
		func(string) string { return "" }, registry, nil, offline, store, catalog)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	login, err := service.StartDeviceLogin(context.Background(), "posthog")
	if err != nil {
		t.Fatalf("StartDeviceLogin: %v", err)
	}
	if login.UserCode != "" {
		t.Fatalf("UserCode = %q, want empty", login.UserCode)
	}
	active, err := service.AwaitDeviceLogin(context.Background(), "posthog")
	if err != nil {
		t.Fatalf("AwaitDeviceLogin: %v", err)
	}
	if active.ProviderID != "posthog" || active.Model != "claude-opus-4-8" {
		t.Fatalf("active = %+v, want the provider activated on its default model", active)
	}
	stored, ok := store.Get("posthog")
	if !ok || stored.OAuth == nil || stored.OAuth.AccountID != "" {
		t.Fatalf("stored = %+v, want an oauth credential with no account id", stored)
	}
}

// TestCatalog_DiscoverDispatchesWithTheResolvedBearer: a format that declares
// its own Discover is asked through it, authenticated with the OAuth access
// token the resolver already holds.
func TestCatalog_DiscoverDispatchesWithTheResolvedBearer(t *testing.T) {
	var sawAuth string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		sawAuth = r.Header.Get("Authorization")
		io.WriteString(w, `{"data":[{"id":"claude-opus-4-8","owned_by":"anthropic"},{"id":"claude-opus-5","owned_by":"anthropic","allowed":false}]}`)
	}))
	defer gateway.Close()

	store := NewFileCredentialStore(filepath.Join(t.TempDir(), "credentials.json"))
	if err := store.Put("posthog", Credential{Type: CredentialTypeOAuth, OAuth: &OAuthCredential{
		AccessToken: "at-live", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Hour),
	}}); err != nil {
		t.Fatalf("seed the store: %v", err)
	}
	cfg := Config{Providers: []Provider{{ID: "posthog", Name: "PostHog", Type: Posthog, BaseURL: gateway.URL, Models: []string{"claude-haiku-4-5"}}}}
	catalog := NewCatalog(cfg, "", nil, nil, NewCredentialResolver(store), DefaultRegistry())

	providers, err := catalog.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if sawAuth != "Bearer at-live" {
		t.Fatalf("Authorization = %q, want the stored bearer", sawAuth)
	}
	models := providers[0].Models
	joined := strings.Join(models, ",")
	if !strings.Contains(joined, "claude-opus-4-8") || strings.Contains(joined, "claude-opus-5") {
		t.Fatalf("models = %v, want the allowed gateway model merged and the gated one absent", models)
	}
}

// TestCatalog_DiscoverSkipsSilentlyWhenNotConnected: an unconnected login
// provider sits in every default catalog, and a refresh that warned about it
// would tell the user to log in to something they never asked for.
func TestCatalog_DiscoverSkipsSilentlyWhenNotConnected(t *testing.T) {
	cfg := Config{Providers: []Provider{{ID: "posthog", Name: "PostHog", Type: Posthog, BaseURL: "http://gateway.invalid", Models: []string{"claude-opus-4-8"}}}}
	store := NewFileCredentialStore(filepath.Join(t.TempDir(), "credentials.json"))
	catalog := NewCatalog(cfg, "", nil, nil, NewCredentialResolver(store), DefaultRegistry())

	providers, err := catalog.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh = %v, want a silent skip", err)
	}
	if got := strings.Join(providers[0].Models, ","); got != "claude-opus-4-8" {
		t.Fatalf("models = %q, want the curated list untouched", got)
	}
}

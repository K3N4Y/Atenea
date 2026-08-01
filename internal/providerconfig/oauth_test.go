package providerconfig

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/K3N4Y/atenea/internal/llm"
)

// oauthCredentialFor is a stored login whose access token expires at the given
// instant. Everything else is the minimum Validate accepts.
func oauthCredentialFor(expiresAt time.Time) Credential {
	return Credential{Type: CredentialTypeOAuth, OAuth: &OAuthCredential{
		AccessToken:  "access-stored",
		RefreshToken: "refresh-stored",
		ExpiresAt:    expiresAt,
		AccountID:    "acct_1",
	}}
}

// countingRefresher hands out a fresh credential per call and records how many
// times it was asked, which is the only way to see a double refresh.
type countingRefresher struct {
	mu    sync.Mutex
	calls int
	seen  []string
	err   error
	// shortLived makes every renewal come back already expired, so a second
	// resolution has to renew again — which is how the rotation can be observed
	// twice.
	shortLived bool
}

func (r *countingRefresher) refresh(_ context.Context, refreshToken string) (OAuthCredential, error) {
	r.mu.Lock()
	r.calls++
	call := r.calls
	r.seen = append(r.seen, refreshToken)
	err := r.err
	r.mu.Unlock()
	if err != nil {
		return OAuthCredential{}, err
	}
	expiresAt := time.Now().Add(time.Hour)
	if r.shortLived {
		expiresAt = time.Now().Add(-time.Minute)
	}
	return OAuthCredential{
		AccessToken:  "access-renewed",
		RefreshToken: "refresh-rotated-" + strings.Repeat("x", call),
		ExpiresAt:    expiresAt,
		AccountID:    "acct_1",
	}, nil
}

func (r *countingRefresher) tokensSeen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

func (r *countingRefresher) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// TestCredentialResolver_ServesAStoredOAuthCredentialWithoutRenewingIt: a token
// with an hour left is the common case, and renewing it every turn would be one
// wasted round trip per prompt.
func TestCredentialResolver_ServesAStoredOAuthCredentialWithoutRenewingIt(t *testing.T) {
	store := memoryCredentials{"p": oauthCredentialFor(time.Now().Add(time.Hour))}
	refresher := &countingRefresher{}
	source := NewCredentialResolver(store).OAuthTokenSource("p", refresher.refresh)

	token, err := source.OAuthToken(context.Background())
	if err != nil {
		t.Fatalf("OAuthToken: %v", err)
	}
	if token.AccessToken != "access-stored" || token.AccountID != "acct_1" {
		t.Fatalf("OAuthToken() = %#v, want the stored credential", token)
	}
	if refresher.count() != 0 {
		t.Fatalf("refreshed %d times for a credential that is still good", refresher.count())
	}
}

// TestCredentialResolver_RenewsBeforeTheTokenExpiresRatherThanAfter: a token that
// dies mid-request fails a turn that is minutes long, so the margin is the whole
// point — a credential inside it is treated as expired.
func TestCredentialResolver_RenewsBeforeTheTokenExpiresRatherThanAfter(t *testing.T) {
	store := memoryCredentials{"p": oauthCredentialFor(time.Now().Add(2 * time.Minute))}
	refresher := &countingRefresher{}
	resolver := NewCredentialResolver(store)
	resolver.oauthMargin = 10 * time.Minute
	source := resolver.OAuthTokenSource("p", refresher.refresh)

	token, err := source.OAuthToken(context.Background())
	if err != nil {
		t.Fatalf("OAuthToken: %v", err)
	}
	if token.AccessToken != "access-renewed" {
		t.Fatalf("OAuthToken() = %#v, want the renewed credential: the stored one expires inside the margin", token)
	}
	if refresher.count() != 1 {
		t.Fatalf("refreshed %d times, want once", refresher.count())
	}
}

// TestCredentialResolver_RenewsACredentialWithNoKnownExpiry: an unknown lifetime
// and an expired one cost the same to get wrong, and one refresh settles it.
func TestCredentialResolver_RenewsACredentialWithNoKnownExpiry(t *testing.T) {
	store := memoryCredentials{"p": oauthCredentialFor(time.Time{})}
	refresher := &countingRefresher{}
	source := NewCredentialResolver(store).OAuthTokenSource("p", refresher.refresh)

	if _, err := source.OAuthToken(context.Background()); err != nil {
		t.Fatalf("OAuthToken: %v", err)
	}
	if refresher.count() != 1 {
		t.Fatalf("refreshed %d times for a credential with no expiry, want once", refresher.count())
	}
}

// TestCredentialResolver_PersistsTheRotatedRefreshToken: the refresh token rotates
// on every renewal, and a resolver that serves the new access token without
// writing the new refresh token logs the user out at the FOLLOWING renewal — an
// hour later, with nothing on screen to connect the two.
func TestCredentialResolver_PersistsTheRotatedRefreshToken(t *testing.T) {
	dir := t.TempDir()
	store := NewFileCredentialStore(filepath.Join(dir, "credentials.json"))
	if err := store.Put("p", oauthCredentialFor(time.Now().Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	refresher := &countingRefresher{}
	source := NewCredentialResolver(store).OAuthTokenSource("p", refresher.refresh)

	if _, err := source.OAuthToken(context.Background()); err != nil {
		t.Fatalf("OAuthToken: %v", err)
	}
	stored, ok := store.Get("p")
	if !ok || stored.OAuth == nil {
		t.Fatalf("stored credential = %#v, want the renewed login", stored)
	}
	if stored.OAuth.RefreshToken != "refresh-rotated-x" {
		t.Fatalf("stored refresh_token = %q, want the rotated one: the old one is already retired", stored.OAuth.RefreshToken)
	}
	if stored.OAuth.AccessToken != "access-renewed" {
		t.Fatalf("stored access_token = %q, want the renewed one", stored.OAuth.AccessToken)
	}
	// A second resolution reads the rotated token off disk rather than the copy
	// that was in memory when the first one started.
	if _, err := source.OAuthToken(context.Background()); err != nil {
		t.Fatalf("second OAuthToken: %v", err)
	}
	if refresher.count() != 1 {
		t.Fatalf("refreshed %d times, want once: the renewal is good for an hour", refresher.count())
	}
	data, err := os.ReadFile(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"type": "oauth"`) || strings.Contains(string(data), `"api_key"`) {
		t.Fatalf("credentials.json does not hold an oauth arm:\n%s", data)
	}
	info, err := os.Stat(filepath.Join(dir, "credentials.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials.json permissions = %v, %v; want 0600", info.Mode().Perm(), err)
	}
}

// TestCredentialResolver_PersistsARotationTheCancelledTurnAbandoned: the ctx that
// reaches this resolver is the TURN's, and the runner cancels it the instant the
// user presses Stop. A cancellation that reached the renewal would abort it in the
// window between OpenAI committing the rotation and this process reading the body:
// nothing gets persisted, the re-read fallback finds the credential the server has
// just retired, and the next prompt sends the user back through the login for a
// reason nothing on screen explains. Stop cancels a turn, not a rotation.
func TestCredentialResolver_PersistsARotationTheCancelledTurnAbandoned(t *testing.T) {
	store := memoryCredentials{"p": oauthCredentialFor(time.Now().Add(-time.Minute))}
	turn, stop := context.WithCancel(context.Background())
	var refreshSaw error
	refresh := func(ctx context.Context, refreshToken string) (OAuthCredential, error) {
		// The user presses Stop with the token endpoint's answer already written.
		stop()
		if refreshSaw = ctx.Err(); refreshSaw != nil {
			return OAuthCredential{}, refreshSaw
		}
		return OAuthCredential{
			AccessToken:  "access-renewed",
			RefreshToken: "refresh-rotated",
			ExpiresAt:    time.Now().Add(time.Hour),
			AccountID:    "acct_1",
		}, nil
	}
	source := NewCredentialResolver(store).OAuthTokenSource("p", refresh)

	token, err := source.OAuthToken(turn)
	if err != nil {
		t.Fatalf("OAuthToken: %v; the renewal was cancelled with %v", err, refreshSaw)
	}
	if token.AccessToken != "access-renewed" {
		t.Fatalf("OAuthToken() = %#v, want the renewal the cancelled turn started", token)
	}
	stored, ok := store.Get("p")
	if !ok || stored.OAuth == nil || stored.OAuth.RefreshToken != "refresh-rotated" {
		t.Fatalf("stored credential = %#v, want the rotated refresh token: the old one is retired server-side", stored)
	}
}

// TestCredentialResolver_RenewsFromTheTokenTheLastRenewalRotatedIn: the rotation
// only means something if the NEXT renewal uses it. A resolver that kept the
// original refresh token would work once and then be rejected, which is the same
// silent logout a hour later that persisting protects against.
func TestCredentialResolver_RenewsFromTheTokenTheLastRenewalRotatedIn(t *testing.T) {
	store := memoryCredentials{"p": oauthCredentialFor(time.Now().Add(-time.Hour))}
	refresher := &countingRefresher{shortLived: true}
	source := NewCredentialResolver(store).OAuthTokenSource("p", refresher.refresh)

	for range 2 {
		if _, err := source.OAuthToken(context.Background()); err != nil {
			t.Fatalf("OAuthToken: %v", err)
		}
	}
	seen := refresher.tokensSeen()
	if len(seen) != 2 {
		t.Fatalf("refreshed with %v, want two renewals", seen)
	}
	if seen[0] != "refresh-stored" {
		t.Fatalf("first renewal used %q, want the stored token", seen[0])
	}
	if seen[1] != "refresh-rotated-x" {
		t.Fatalf("second renewal used %q, want the token the first one rotated in", seen[1])
	}
}

// TestCredentialResolver_RenewsOnceWhenSeveralTurnsNoticeTheSameExpiry: a main
// turn and the subagents it spawned share one adapter and all notice the same
// expiry at the same moment. Without single-flight each would refresh, each would
// rotate the refresh token, and every rotation but the last would already be
// retired — so the stored credential would name a token the server has dropped.
//
// Run under -race.
func TestCredentialResolver_RenewsOnceWhenSeveralTurnsNoticeTheSameExpiry(t *testing.T) {
	store := &lockedCredentials{entries: map[string]Credential{"p": oauthCredentialFor(time.Now().Add(-time.Minute))}}
	refresher := &countingRefresher{}
	source := NewCredentialResolver(store).OAuthTokenSource("p", refresher.refresh)

	const concurrent = 8
	tokens := make([]string, concurrent)
	errs := make([]error, concurrent)
	var wg sync.WaitGroup
	wg.Add(concurrent)
	for i := range concurrent {
		go func() {
			defer wg.Done()
			token, err := source.OAuthToken(context.Background())
			tokens[i], errs[i] = token.AccessToken, err
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
		if tokens[i] != "access-renewed" {
			t.Fatalf("turn %d got %q, want every turn served the one renewal", i, tokens[i])
		}
	}
	if refresher.count() != 1 {
		t.Fatalf("refreshed %d times for %d concurrent turns, want exactly one", refresher.count(), concurrent)
	}
	stored, _ := store.Get("p")
	if stored.OAuth.RefreshToken != "refresh-rotated-x" {
		t.Fatalf("stored refresh_token = %q, want the single rotation", stored.OAuth.RefreshToken)
	}
}

// lockedCredentials is a store safe for concurrent use, which memoryCredentials is
// not: the single-flight check has several goroutines reading and writing at once
// and a map would fail on the map rather than on the behavior.
type lockedCredentials struct {
	mu      sync.Mutex
	entries map[string]Credential
	putErr  error
}

func (s *lockedCredentials) Get(providerID string) (Credential, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	credential, ok := s.entries[providerID]
	return credential, ok
}

func (s *lockedCredentials) Put(providerID string, credential Credential) error {
	if err := credential.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.putErr != nil {
		return s.putErr
	}
	s.entries[providerID] = credential
	return nil
}

// TestCredentialResolver_ReportsAFailedRenewalAsSomethingTheUserCanFix: a revoked
// or expired login is the one provider failure a user can act on, and "log in
// again" is the sentence that says how.
func TestCredentialResolver_ReportsAFailedRenewalAsSomethingTheUserCanFix(t *testing.T) {
	store := memoryCredentials{"p": oauthCredentialFor(time.Now().Add(-time.Hour))}
	refresher := &countingRefresher{err: errors.New("invalid_grant")}
	source := NewCredentialResolver(store).OAuthTokenSource("p", refresher.refresh)

	_, err := source.OAuthToken(context.Background())
	if err == nil {
		t.Fatal("OAuthToken: expected the failed renewal to surface")
	}
	if !strings.Contains(err.Error(), "log in again") || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("OAuthToken() error = %v, want what to do plus the reason", err)
	}
}

// TestCredentialResolver_ServesTheRotationAnotherProcessLandedFirst: the gate is
// in-process and credentials.json is shared. Two hosts noticing the same expiry in
// the same second both renew, and OpenAI retires the refresh token on the first one
// that lands — so the loser reads invalid_grant about a credential that is now
// good. Telling the user to log in again there is a logout they have to undo by
// hand, for a race neither of them can see.
func TestCredentialResolver_ServesTheRotationAnotherProcessLandedFirst(t *testing.T) {
	store := &lockedCredentials{entries: map[string]Credential{"p": oauthCredentialFor(time.Now().Add(-time.Minute))}}
	rotatedElsewhere := Credential{Type: CredentialTypeOAuth, OAuth: &OAuthCredential{
		AccessToken:  "access-from-the-other-process",
		RefreshToken: "refresh-rotated-elsewhere",
		ExpiresAt:    time.Now().Add(time.Hour),
		AccountID:    "acct_1",
	}}
	refreshes := 0
	// The other host's renewal lands while this one is in flight, which is what
	// retires the token this one is presenting.
	refresh := func(context.Context, string) (OAuthCredential, error) {
		refreshes++
		if err := store.Put("p", rotatedElsewhere); err != nil {
			t.Fatal(err)
		}
		return OAuthCredential{}, errors.New("invalid_grant")
	}
	source := NewCredentialResolver(store).OAuthTokenSource("p", refresh)

	token, err := source.OAuthToken(context.Background())
	if err != nil {
		t.Fatalf("OAuthToken() = %v, want the credential the other process left on disk", err)
	}
	if token.AccessToken != "access-from-the-other-process" {
		t.Fatalf("OAuthToken() = %#v, want the rotation that landed first", token)
	}
	if refreshes != 1 {
		t.Fatalf("refreshed %d times, want the one attempt that lost the race", refreshes)
	}
}

// TestCredentialResolver_ReportsARotationItCouldNotSave: the credential is already
// broken at that point — the server retired the old refresh token — so serving the
// turn anyway would buy an hour and then a logout with no visible cause.
func TestCredentialResolver_ReportsARotationItCouldNotSave(t *testing.T) {
	store := &lockedCredentials{
		entries: map[string]Credential{"p": oauthCredentialFor(time.Now().Add(-time.Hour))},
		putErr:  errors.New("read-only file system"),
	}
	refresher := &countingRefresher{}
	source := NewCredentialResolver(store).OAuthTokenSource("p", refresher.refresh)

	_, err := source.OAuthToken(context.Background())
	if err == nil || !strings.Contains(err.Error(), "could not save") {
		t.Fatalf("OAuthToken() error = %v, want the persistence failure named", err)
	}
}

// TestCredentialResolver_ReportsAMissingOrMismatchedLogin: both failures are the
// user's to act on, and both have to say which action.
func TestCredentialResolver_ReportsAMissingOrMismatchedLogin(t *testing.T) {
	cases := map[string]struct {
		store CredentialStore
		want  string
	}{
		"nothing stored": {memoryCredentials{}, "run /connect"},
		"an api key instead": {
			memoryCredentials{"p": {Type: CredentialTypeAPIKey, APIKey: "sk-pasted"}},
			"authenticates with a login",
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			source := NewCredentialResolver(test.store).OAuthTokenSource("p", (&countingRefresher{}).refresh)
			_, err := source.OAuthToken(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("OAuthToken() error = %v, want it to contain %q", err, test.want)
			}
		})
	}
}

// TestCredentialResolver_OAuthCredentialHasNoStaticTokenToResolve: Token is what a
// selection bakes into an adapter, and a login has nothing to bake. Answering
// "nothing static" is what makes the provider build on the keyless placeholder
// instead of on a secret the adapter would ignore.
func TestCredentialResolver_OAuthCredentialHasNoStaticTokenToResolve(t *testing.T) {
	resolver := NewCredentialResolver(memoryCredentials{"p": oauthCredentialFor(time.Now().Add(time.Hour))})
	token, err := resolver.Token(context.Background(), "p")
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if token != "" {
		t.Fatalf("Token() = %q, want empty: the bearer of a login is resolved per request", token)
	}
}

// deviceIssuer is a stub authorization server that answers the whole device-code
// flow. It is the real openaiauth client that talks to it, driven through the
// provider's declared oauth_issuer — which is what makes the login testable end to
// end from here rather than against a fake of our own flow.
func deviceIssuer(t *testing.T, approve bool) *httptest.Server {
	t.Helper()
	claims, err := json.Marshal(map[string]any{"chatgpt_account_id": "acct_from_login"})
	if err != nil {
		t.Fatal(err)
	}
	access := "h." + base64.RawURLEncoding.EncodeToString(claims) + ".s"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			io.WriteString(w, `{"device_auth_id":"deviceauth_1","user_code":"V3H5-1MW96","interval":"0","expires_at":"`+
				time.Now().Add(10*time.Minute).UTC().Format(time.RFC3339)+`"}`)
		case "/api/accounts/deviceauth/token":
			if !approve {
				io.WriteString(w, `{"error":"deviceauth_authorization_pending"}`)
				return
			}
			io.WriteString(w, `{"authorization_code":"ac","code_verifier":"cv"}`)
		case "/oauth/token":
			io.WriteString(w, `{"access_token":"`+access+`","refresh_token":"refresh-from-login","expires_in":3600}`)
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// loginService is a service whose only provider is connected by logging in,
// against the stub issuer.
func loginService(t *testing.T, issuerURL string) (*Service, *FileCredentialStore) {
	t.Helper()
	dir := t.TempDir()
	credentials := NewFileCredentialStore(filepath.Join(dir, "credentials.json"))
	catalog := Config{Providers: []Provider{
		{
			ID: "openai-codex", Name: "OpenAI (ChatGPT subscription)", Type: OpenAICodex,
			BaseURL: "https://chatgpt.com/backend-api/codex", OAuthIssuer: issuerURL,
			DisableModelDiscovery: true, Models: []string{"gpt-5.5", "gpt-5.4"},
		},
		{ID: "openrouter", Name: "OpenRouter", Type: OpenRouter, BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "OPENROUTER_API_KEY", Models: []string{"openrouter/free"}},
	}}
	registry := DefaultRegistry()
	offline := func(context.Context, string, string) ([]string, error) { return nil, errors.New("offline") }
	s, err := Open(context.Background(), filepath.Join(dir, "providers.json"), "", fallbackSnapshot(),
		func(string) string { return "" }, registry, nil, offline, credentials, catalog)
	if err != nil {
		t.Fatalf("open provider service: %v", err)
	}
	return s, credentials
}

// TestService_DeviceLoginStoresTheApprovedCredentialAndActivatesTheProvider: the
// whole flow, in the service both hosts drive — a code to show, a wait, a stored
// oauth credential, and a provider left active on its default model so the user
// can talk to it with no further step.
func TestService_DeviceLoginStoresTheApprovedCredentialAndActivatesTheProvider(t *testing.T) {
	issuer := deviceIssuer(t, true)
	s, credentials := loginService(t, issuer.URL)

	login, err := s.StartDeviceLogin(context.Background(), "openai-codex")
	if err != nil {
		t.Fatalf("StartDeviceLogin: %v", err)
	}
	if login.UserCode != "V3H5-1MW96" {
		t.Fatalf("login = %#v, want the code the server issued", login)
	}
	if login.VerificationURI == "" || login.ExpiresAt.IsZero() {
		t.Fatalf("login = %#v, want somewhere to type the code and a deadline to show", login)
	}

	active, err := s.AwaitDeviceLogin(context.Background(), "openai-codex")
	if err != nil {
		t.Fatalf("AwaitDeviceLogin: %v", err)
	}
	if active.ProviderID != "openai-codex" || active.Model != "gpt-5.5" {
		t.Fatalf("active = %#v, want the provider on its first curated model", active)
	}
	stored, ok := credentials.Get("openai-codex")
	if !ok || stored.Type != CredentialTypeOAuth || stored.OAuth == nil {
		t.Fatalf("stored credential = %#v, want the oauth arm", stored)
	}
	if stored.OAuth.AccountID != "acct_from_login" || stored.OAuth.RefreshToken != "refresh-from-login" {
		t.Fatalf("stored credential = %#v, want the account and refresh token from the login", stored.OAuth)
	}
	if stored.OAuth.ExpiresAt.IsZero() {
		t.Error("stored credential has no expiry: renewal would then happen on every single request")
	}
	// A second await has nothing to collect: the login completed and was forgotten.
	if _, err := s.AwaitDeviceLogin(context.Background(), "openai-codex"); !errors.Is(err, ErrNoPendingLogin) {
		t.Fatalf("second AwaitDeviceLogin error = %v, want ErrNoPendingLogin", err)
	}
}

// TestService_CancelDeviceLoginStopsWaitingForAnUnapprovedCode: closing the panel
// has to end the polling, or a login the user walked away from keeps a goroutine
// asking about a dead code until it expires.
func TestService_CancelDeviceLoginStopsWaitingForAnUnapprovedCode(t *testing.T) {
	issuer := deviceIssuer(t, false)
	s, credentials := loginService(t, issuer.URL)

	if _, err := s.StartDeviceLogin(context.Background(), "openai-codex"); err != nil {
		t.Fatalf("StartDeviceLogin: %v", err)
	}
	s.CancelDeviceLogin("openai-codex")

	if _, err := s.AwaitDeviceLogin(context.Background(), "openai-codex"); !errors.Is(err, ErrNoPendingLogin) {
		t.Fatalf("AwaitDeviceLogin after cancelling = %v, want ErrNoPendingLogin", err)
	}
	if _, ok := credentials.Get("openai-codex"); ok {
		t.Fatal("a cancelled login stored a credential")
	}
	// Cancelling again is a no-op: a host may act on a code that already resolved.
	s.CancelDeviceLogin("openai-codex")
}

// TestService_AwaitDeviceLoginNamesACancelledLoginRatherThanLeakingTheCancellation:
// a host that awaits and is cancelled underneath — the order both UIs use, since
// the wait starts as soon as the code is on screen — must be able to tell "the user
// pressed Cancel" from "the login failed". A raw context.Canceled cannot be told
// apart from any other cancellation, and the desktop paints it in red.
//
// The finished login is installed directly rather than raced against a goroutine:
// what is under test is which condition a waiter collects, not who wins.
func TestService_AwaitDeviceLoginNamesACancelledLoginRatherThanLeakingTheCancellation(t *testing.T) {
	s, _ := loginService(t, deviceIssuer(t, false).URL)
	pending := &pendingLogin{
		login:   DeviceLogin{ProviderID: "openai-codex"},
		cancel:  func() {},
		settled: make(chan struct{}),
	}
	pending.settle(loginResult{err: context.Canceled})
	s.replacePendingLogin("openai-codex", pending)

	_, err := s.AwaitDeviceLogin(context.Background(), "openai-codex")
	if !errors.Is(err, ErrLoginCancelled) {
		t.Fatalf("AwaitDeviceLogin() = %v, want ErrLoginCancelled", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("AwaitDeviceLogin() = %v, still reads as a bare cancellation a host cannot tell from its own", err)
	}
}

// TestService_AwaitDeviceLoginStopsWaitingWithoutAbandoningTheLogin: a host that
// stops waiting — a redraw, a reload — must not throw away a code the user is in
// the middle of typing.
func TestService_AwaitDeviceLoginStopsWaitingWithoutAbandoningTheLogin(t *testing.T) {
	issuer := deviceIssuer(t, false)
	s, _ := loginService(t, issuer.URL)

	if _, err := s.StartDeviceLogin(context.Background(), "openai-codex"); err != nil {
		t.Fatalf("StartDeviceLogin: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.AwaitDeviceLogin(ctx, "openai-codex"); !errors.Is(err, context.Canceled) {
		t.Fatalf("AwaitDeviceLogin error = %v, want the caller's cancellation", err)
	}
	if _, ok := s.pendingLogin("openai-codex"); !ok {
		t.Fatal("the login was forgotten because a waiter gave up; only CancelDeviceLogin may end it")
	}
	s.CancelDeviceLogin("openai-codex")
}

// TestService_StartDeviceLoginRefusesAProviderConnectedWithAKey, and Connect
// refuses the reverse: storing a pasted string for a login provider would produce
// a credential nothing can refresh, and starting a login for a key provider would
// hit an issuer that does not exist.
func TestService_LoginAndKeyConnectionsRefuseEachOther(t *testing.T) {
	issuer := deviceIssuer(t, true)
	s, _ := loginService(t, issuer.URL)

	if _, err := s.StartDeviceLogin(context.Background(), "openrouter"); err == nil ||
		!strings.Contains(err.Error(), "API key") {
		t.Fatalf("StartDeviceLogin(openrouter) error = %v, want it to say that provider takes a key", err)
	}
	if _, err := s.Connect(context.Background(), "openai-codex", "sk-pasted"); err == nil ||
		!strings.Contains(err.Error(), "logging in") {
		t.Fatalf("Connect(openai-codex) error = %v, want it to say that provider is connected by logging in", err)
	}
	if _, err := s.StartDeviceLogin(context.Background(), "nope"); err == nil {
		t.Fatal("StartDeviceLogin on a provider that is not configured: expected an error")
	}
}

// TestService_ConnectableTellsTheTwoKindsOfConnectionApart: a UI draws a masked
// input for one and a code for the other, so it cannot be left to guess from the
// provider id.
func TestService_ConnectableTellsTheTwoKindsOfConnectionApart(t *testing.T) {
	issuer := deviceIssuer(t, true)
	s, _ := loginService(t, issuer.URL)

	kinds := map[string]string{}
	connected := map[string]bool{}
	for _, provider := range s.Connectable() {
		kinds[provider.ID] = provider.Kind
		connected[provider.ID] = provider.Connected
	}
	if kinds["openai-codex"] != ConnectDeviceCode {
		t.Errorf("openai-codex kind = %q, want %q", kinds["openai-codex"], ConnectDeviceCode)
	}
	if kinds["openrouter"] != ConnectAPIKey {
		t.Errorf("openrouter kind = %q, want %q", kinds["openrouter"], ConnectAPIKey)
	}
	if connected["openai-codex"] {
		t.Error("openai-codex reports connected before any login")
	}

	if _, err := s.StartDeviceLogin(context.Background(), "openai-codex"); err != nil {
		t.Fatalf("StartDeviceLogin: %v", err)
	}
	if _, err := s.AwaitDeviceLogin(context.Background(), "openai-codex"); err != nil {
		t.Fatalf("AwaitDeviceLogin: %v", err)
	}
	for _, provider := range s.Connectable() {
		if provider.ID == "openai-codex" && !provider.Connected {
			t.Fatalf("openai-codex = %#v, want reported connected after the login", provider)
		}
	}
}

// TestService_DeviceLoginReplacesTheCodeOfAnEarlierAttempt: a retry retires the
// previous code server-side, so leaving its goroutine polling would leak one per
// attempt.
func TestService_DeviceLoginReplacesTheCodeOfAnEarlierAttempt(t *testing.T) {
	issuer := deviceIssuer(t, false)
	s, _ := loginService(t, issuer.URL)

	if _, err := s.StartDeviceLogin(context.Background(), "openai-codex"); err != nil {
		t.Fatalf("first StartDeviceLogin: %v", err)
	}
	first, _ := s.pendingLogin("openai-codex")
	if _, err := s.StartDeviceLogin(context.Background(), "openai-codex"); err != nil {
		t.Fatalf("second StartDeviceLogin: %v", err)
	}
	second, _ := s.pendingLogin("openai-codex")
	if first == second {
		t.Fatal("a second login reused the first attempt's state")
	}
	// The retired attempt reports the cancellation and stops asking.
	select {
	case <-first.settled:
		if !errors.Is(first.result.err, context.Canceled) {
			t.Fatalf("the retired login ended with %v, want the cancellation", first.result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the retired login is still polling a code the server has retired")
	}
	s.CancelDeviceLogin("openai-codex")
}

// TestService_ALateMintDoesNotRetireTheCodeThatReplacedIt: two mints can be in
// flight at once — one attempt the user dismissed and the retry whose code is now
// on screen — and the network decides which answers first, not the user.
//
// Retiring on arrival order lets the older attempt install itself over the newer
// one and cancel it on the way in. The live wait then settles as "the login was
// cancelled" over a code nobody cancelled, and the host retires the survivor too.
// The late arrival is the one with nothing to lose: its code was never painted.
//
// The two mints are held on channels rather than raced, so the inversion the bug
// needs happens on every run instead of on the unlucky one.
func TestService_ALateMintDoesNotRetireTheCodeThatReplacedIt(t *testing.T) {
	s, _ := loginService(t, deviceIssuer(t, false).URL)

	codes := []string{"CODE-1", "CODE-2"}
	entered := make(chan int, len(codes))
	release := []chan struct{}{make(chan struct{}), make(chan struct{})}
	// cancelled names the code whose polling was retired. Waiting on it is what
	// makes the assertion deterministic: exactly one of the two loses, and the test
	// blocks until it is known which.
	cancelled := make(chan string, len(codes))
	minted := 0
	s.registry = Registry{OpenAICodex: Format{
		Build: func(BuildParams) (llm.Provider, error) { return inertProvider{}, nil },
		OAuth: &OAuthFlow{Login: func(context.Context, Provider) (DeviceCode, error) {
			index := minted
			minted++
			entered <- index
			<-release[index]
			return DeviceCode{
				UserCode:        codes[index],
				VerificationURI: "https://issuer.test/device",
				Await: func(ctx context.Context) (OAuthCredential, error) {
					<-ctx.Done()
					cancelled <- codes[index]
					return OAuthCredential{}, ctx.Err()
				},
			}, nil
		}},
	}}

	type started struct {
		login DeviceLogin
		err   error
	}
	begin := func() chan started {
		out := make(chan started, 1)
		go func() {
			login, err := s.StartDeviceLogin(context.Background(), "openai-codex")
			out <- started{login, err}
		}()
		return out
	}
	// The handshake is what fixes the START order: the first attempt is already
	// inside its mint before the second one is even launched.
	first := begin()
	if index := <-entered; index != 0 {
		t.Fatalf("the first attempt entered its mint as %d, want the first", index)
	}
	second := begin()
	if index := <-entered; index != 1 {
		t.Fatalf("the second attempt entered its mint as %d, want the second", index)
	}

	close(release[1])
	if result := <-second; result.err != nil {
		t.Fatalf("second StartDeviceLogin: %v", result.err)
	}
	close(release[0])
	older := <-first
	if older.err != nil {
		t.Fatalf("first StartDeviceLogin: %v", older.err)
	}

	if retired := <-cancelled; retired != "CODE-1" {
		t.Fatalf("the login retired was %s, want CODE-1: the late arrival is the one with no code on screen to lose", retired)
	}
	pending, ok := s.pendingLogin("openai-codex")
	if !ok || pending.login.UserCode != "CODE-2" {
		t.Fatalf("pending login = %#v, want the newer code left installed", pending)
	}
	if older.login.Attempt >= pending.login.Attempt {
		t.Fatalf("attempts = %d then %d, want the handle to number them by start order", older.login.Attempt, pending.login.Attempt)
	}
	// The abandoned attempt's handle is not a licence to cancel whatever is pending.
	s.CancelDeviceLoginAttempt("openai-codex", older.login.Attempt)
	if _, ok := s.pendingLogin("openai-codex"); !ok {
		t.Fatal("an attempt the user walked away from cancelled the login that replaced it")
	}
	s.CancelDeviceLoginAttempt("openai-codex", pending.login.Attempt)
	if _, ok := s.pendingLogin("openai-codex"); ok {
		t.Fatal("cancelling by its own handle left the login installed")
	}
	if retired := <-cancelled; retired != "CODE-2" {
		t.Fatalf("the second cancellation retired %s, want the login it named", retired)
	}
}

// TestPendingLogin_ReleasesEveryWaiterWithTheSameOutcome: one login can have more
// than one waiter. A retry installs a second attempt while the first host's wait is
// still dispatched, and both then resolve to whichever attempt the map holds; the
// desktop and the terminal can also be watching one code at once. A one-shot
// handoff answers whoever arrives first and parks the rest until the process exits,
// which is a goroutine leaked per collision.
func TestPendingLogin_ReleasesEveryWaiterWithTheSameOutcome(t *testing.T) {
	pending := &pendingLogin{settled: make(chan struct{})}
	collected := make(chan loginResult, 3)
	for range cap(collected) {
		go func() {
			<-pending.settled
			collected <- pending.result
		}()
	}

	pending.settle(loginResult{active: Active{ProviderID: "openai-codex"}, err: ErrLoginCancelled})

	for i := range cap(collected) {
		select {
		case result := <-collected:
			if !errors.Is(result.err, ErrLoginCancelled) || result.active.ProviderID != "openai-codex" {
				t.Fatalf("waiter %d collected %#v, want the outcome the login settled on", i, result)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("waiter %d never woke up: it is parked on a result another waiter took", i)
		}
	}
}

// TestService_LoginProviderBuildsOnAPerRequestCredentialSource: the point of the
// whole seam. The adapter the selection produces must have a way to ask for a
// credential, because the one it would have been handed expires inside the hour.
func TestService_LoginProviderBuildsOnAPerRequestCredentialSource(t *testing.T) {
	issuer := deviceIssuer(t, true)
	s, _ := loginService(t, issuer.URL)
	var params []BuildParams
	s.registry = Registry{OpenAICodex: Format{
		Build: func(p BuildParams) (llm.Provider, error) {
			params = append(params, p)
			return inertProvider{}, nil
		},
		OAuth: openAIOAuthFlow(),
	}}

	if _, err := s.StartDeviceLogin(context.Background(), "openai-codex"); err != nil {
		t.Fatalf("StartDeviceLogin: %v", err)
	}
	if _, err := s.AwaitDeviceLogin(context.Background(), "openai-codex"); err != nil {
		t.Fatalf("AwaitDeviceLogin: %v", err)
	}
	if len(params) != 1 {
		t.Fatalf("the factory ran %d times, want once for the activated selection", len(params))
	}
	if params[0].Tokens == nil {
		t.Fatal("the adapter was built with no credential source: every turn past the first hour would be a 401")
	}
	token, err := params[0].Tokens.OAuthToken(context.Background())
	if err != nil {
		t.Fatalf("the credential source cannot answer: %v", err)
	}
	if token.AccountID != "acct_from_login" {
		t.Fatalf("credential = %#v, want the account the login named", token)
	}
	if params[0].APIKey == "" {
		t.Error("the adapter was built with an empty API key: a login resolves to the keyless placeholder, not to nothing")
	}
}

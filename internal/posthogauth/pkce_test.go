package posthogauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// tokenEndpoint is a stub issuer that records every token request it serves.
type tokenEndpoint struct {
	mu       sync.Mutex
	requests []tokenRequest
	status   int
	body     string
}

type tokenRequest struct {
	contentType string
	fields      map[string]string
}

func (e *tokenEndpoint) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		data, _ := io.ReadAll(r.Body)
		fields := map[string]string{}
		_ = json.Unmarshal(data, &fields)
		e.mu.Lock()
		e.requests = append(e.requests, tokenRequest{contentType: r.Header.Get("Content-Type"), fields: fields})
		e.mu.Unlock()
		status := e.status
		if status == 0 {
			status = http.StatusOK
		}
		body := e.body
		if body == "" {
			body = `{"access_token":"at-secret","refresh_token":"rt-secret","expires_in":3600}`
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

func (e *tokenEndpoint) last(t *testing.T) tokenRequest {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.requests) == 0 {
		t.Fatal("the token endpoint was never called")
	}
	return e.requests[len(e.requests)-1]
}

// newLoginFixture stands up a stub issuer and a client whose callback listener
// binds port 0, so tests never collide with the registered port.
func newLoginFixture(t *testing.T, endpoint *tokenEndpoint) (*Client, *net.TCPAddr) {
	t.Helper()
	issuer := httptest.NewServer(endpoint.handler())
	t.Cleanup(issuer.Close)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind test listener: %v", err)
	}
	client := NewClient(
		WithIssuer(issuer.URL),
		WithListener(func() (net.Listener, error) { return listener, nil }),
	)
	return client, listener.Addr().(*net.TCPAddr)
}

// browse plays the browser: it follows the redirect back to the local callback
// and returns the page it was shown.
func browse(t *testing.T, addr *net.TCPAddr, path string) (int, string) {
	t.Helper()
	response, err := http.Get(fmt.Sprintf("http://%s%s", addr, path))
	if err != nil {
		t.Fatalf("reach the callback listener: %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(body)
}

func loginState(t *testing.T, login Login) string {
	t.Helper()
	parsed, err := url.Parse(login.AuthorizeURL)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	return parsed.Query().Get("state")
}

// awaitAsync collects Await's outcome without blocking the browser driving.
func awaitAsync(login Login) (<-chan Tokens, <-chan error) {
	tokens := make(chan Tokens, 1)
	errs := make(chan error, 1)
	go func() {
		got, err := login.Await(context.Background())
		if err != nil {
			errs <- err
			return
		}
		tokens <- got
	}()
	return tokens, errs
}

func TestBeginLogin_AuthorizeURLCarriesTheWholeContract(t *testing.T) {
	client, _ := newLoginFixture(t, &tokenEndpoint{})
	login, err := client.BeginLogin(context.Background())
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	parsed, err := url.Parse(login.AuthorizeURL)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	if parsed.Path != "/oauth/authorize" {
		t.Fatalf("authorize path = %q, want /oauth/authorize", parsed.Path)
	}
	query := parsed.Query()
	for param, want := range map[string]string{
		"client_id":             usClientID,
		"redirect_uri":          "http://localhost:8237/callback",
		"response_type":         "code",
		"code_challenge_method": "S256",
		"scope":                 "*",
		"required_access_level": "project",
	} {
		if got := query.Get(param); got != want {
			t.Errorf("authorize URL %s = %q, want %q", param, got, want)
		}
	}
	challenge := query.Get("code_challenge")
	if decoded, err := base64.RawURLEncoding.DecodeString(challenge); err != nil || len(decoded) != 32 {
		t.Errorf("code_challenge %q is not base64url of a SHA-256 digest", challenge)
	}
	state := query.Get("state")
	if decoded, err := base64.RawURLEncoding.DecodeString(state); err != nil || len(decoded) != 16 {
		t.Errorf("state %q is not base64url of 16 random bytes", state)
	}
	if !login.ExpiresAt.After(time.Now()) {
		t.Errorf("ExpiresAt = %v, want in the future", login.ExpiresAt)
	}
}

func TestAwait_ExchangesTheCallbackCode(t *testing.T) {
	endpoint := &tokenEndpoint{}
	client, addr := newLoginFixture(t, endpoint)
	login, err := client.BeginLogin(context.Background())
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	tokens, errs := awaitAsync(login)

	status, page := browse(t, addr, "/callback?code=auth-code-secret&state="+url.QueryEscape(loginState(t, login)))
	if status != http.StatusOK {
		t.Fatalf("callback status = %d, want 200", status)
	}
	if !strings.Contains(page, "Authentication complete") {
		t.Fatalf("browser was shown %q, want the success page", page)
	}

	select {
	case got := <-tokens:
		if got.AccessToken != "at-secret" || got.RefreshToken != "rt-secret" {
			t.Fatalf("tokens = %+v, want the issued pair", got)
		}
	case err := <-errs:
		t.Fatalf("Await: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Await never returned")
	}

	request := endpoint.last(t)
	if request.contentType != "application/json" {
		t.Errorf("token request Content-Type = %q, want application/json", request.contentType)
	}
	want := map[string]bool{"grant_type": true, "code": true, "redirect_uri": true, "client_id": true, "code_verifier": true}
	for field := range request.fields {
		if !want[field] {
			t.Errorf("token request carries unexpected field %q", field)
		}
	}
	for field := range want {
		if request.fields[field] == "" {
			t.Errorf("token request is missing field %q", field)
		}
	}
	if request.fields["grant_type"] != "authorization_code" {
		t.Errorf("grant_type = %q", request.fields["grant_type"])
	}
	if request.fields["code"] != "auth-code-secret" {
		t.Errorf("code = %q, want the callback's code", request.fields["code"])
	}
	if request.fields["redirect_uri"] != "http://localhost:8237/callback" {
		t.Errorf("redirect_uri = %q", request.fields["redirect_uri"])
	}
	// The verifier sent at the exchange must be the one the authorize URL
	// committed to, or the server rejects a login the user just approved.
	parsed, _ := url.Parse(login.AuthorizeURL)
	if challengeS256(request.fields["code_verifier"]) != parsed.Query().Get("code_challenge") {
		t.Error("code_verifier does not match the code_challenge the authorize URL carried")
	}
}

func TestAwait_CallbackFailures(t *testing.T) {
	cases := []struct {
		name  string
		query func(state string) string
		wants string
	}{
		{"oauth error", func(string) string { return "error=access_denied" }, "access_denied"},
		{"missing code", func(state string) string { return "state=" + url.QueryEscape(state) }, "no code"},
		{"state mismatch", func(string) string { return "code=stolen&state=forged" }, "did not match"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, addr := newLoginFixture(t, &tokenEndpoint{})
			login, err := client.BeginLogin(context.Background())
			if err != nil {
				t.Fatalf("BeginLogin: %v", err)
			}
			_, errs := awaitAsync(login)

			status, page := browse(t, addr, "/callback?"+tc.query(loginState(t, login)))
			if status != http.StatusOK {
				t.Fatalf("callback status = %d, want 200", status)
			}
			if !strings.Contains(page, "Authentication failed") {
				t.Fatalf("browser was shown %q, want the error page", page)
			}

			select {
			case err := <-errs:
				if !strings.Contains(err.Error(), tc.wants) {
					t.Fatalf("Await error = %q, want it to mention %q", err, tc.wants)
				}
				if strings.Contains(err.Error(), "stolen") {
					t.Fatalf("Await error %q quotes the authorization code", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Await never returned")
			}
		})
	}
}

func TestAwait_OtherPathsAre404AndDoNotSettleTheLogin(t *testing.T) {
	endpoint := &tokenEndpoint{}
	client, addr := newLoginFixture(t, endpoint)
	login, err := client.BeginLogin(context.Background())
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	tokens, errs := awaitAsync(login)

	if status, _ := browse(t, addr, "/favicon.ico"); status != http.StatusNotFound {
		t.Fatalf("stray path status = %d, want 404", status)
	}
	select {
	case <-tokens:
		t.Fatal("a stray request settled the login")
	case err := <-errs:
		t.Fatalf("a stray request failed the login: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	// The login is still live: the real redirect completes it.
	browse(t, addr, "/callback?code=late-code&state="+url.QueryEscape(loginState(t, login)))
	select {
	case <-tokens:
	case err := <-errs:
		t.Fatalf("Await: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Await never returned")
	}
}

func TestAwait_FirstCallbackWins(t *testing.T) {
	endpoint := &tokenEndpoint{}
	client, addr := newLoginFixture(t, endpoint)
	login, err := client.BeginLogin(context.Background())
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	state := url.QueryEscape(loginState(t, login))
	// Both redirects land before Await collects, so delivery order — not
	// collection timing — is what decides.
	browse(t, addr, "/callback?code=first&state="+state)
	browse(t, addr, "/callback?code=second&state="+state)

	tokens, errs := awaitAsync(login)
	select {
	case <-tokens:
	case err := <-errs:
		t.Fatalf("Await: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Await never returned")
	}
	if got := endpoint.last(t).fields["code"]; got != "first" {
		t.Fatalf("exchanged code = %q, want the first callback's", got)
	}
}

func TestAwait_CancellationFreesThePort(t *testing.T) {
	client, addr := newLoginFixture(t, &tokenEndpoint{})
	login, err := client.BeginLogin(context.Background())
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		_, err := login.Await(ctx)
		errs <- err
	}()
	cancel()
	select {
	case err := <-errs:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Await = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Await never returned")
	}
	// The listener must be gone, or the next login collides with this one.
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr.String(), 100*time.Millisecond)
		if err != nil {
			break
		}
		conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("the callback listener survived cancellation")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAwait_TimesOut(t *testing.T) {
	client, _ := newLoginFixture(t, &tokenEndpoint{})
	// A clock this far behind makes the login's deadline already past by the
	// time Await computes it.
	client.now = func() time.Time { return time.Now().Add(-2 * loginTimeout) }
	login, err := client.BeginLogin(context.Background())
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	_, err = login.Await(context.Background())
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Await = %v, want a timeout the user can read", err)
	}
}

func TestBeginLogin_ReportsTheBusyPort(t *testing.T) {
	client := NewClient(WithListener(func() (net.Listener, error) {
		return nil, fmt.Errorf("start a PostHog login: port %d is already in use or cannot be bound (another login in progress?): boom", callbackPort)
	}))
	_, err := client.BeginLogin(context.Background())
	if err == nil || !strings.Contains(err.Error(), "8237") {
		t.Fatalf("BeginLogin = %v, want an error naming the port", err)
	}
}

func TestListenCallback_NamesThePortWhenBusy(t *testing.T) {
	holder, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", callbackPort))
	if err != nil {
		t.Skipf("port %d is not free on this machine: %v", callbackPort, err)
	}
	defer holder.Close()
	_, err = listenCallback()
	if err == nil || !strings.Contains(err.Error(), "8237") {
		t.Fatalf("listenCallback = %v, want an error naming the busy port", err)
	}
}

func TestRefresh_RotatesThePair(t *testing.T) {
	endpoint := &tokenEndpoint{body: `{"access_token":"at-2","refresh_token":"rt-2","expires_in":600}`}
	issuer := httptest.NewServer(endpoint.handler())
	defer issuer.Close()
	client := NewClient(WithIssuer(issuer.URL))
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	client.now = func() time.Time { return now }

	tokens, err := client.Refresh(context.Background(), "rt-1")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tokens.AccessToken != "at-2" || tokens.RefreshToken != "rt-2" {
		t.Fatalf("tokens = %+v, want the rotated pair", tokens)
	}
	if want := now.Add(600*time.Second - expirySkew); !tokens.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt = %v, want %v (expires_in minus the skew)", tokens.ExpiresAt, want)
	}
	request := endpoint.last(t)
	if len(request.fields) != 3 {
		t.Fatalf("refresh request carries %d fields %v, want exactly grant_type, refresh_token, client_id", len(request.fields), request.fields)
	}
	if request.fields["grant_type"] != "refresh_token" || request.fields["refresh_token"] != "rt-1" || request.fields["client_id"] == "" {
		t.Fatalf("refresh request = %v", request.fields)
	}
}

func TestRefresh_RefusesAnEmptyToken(t *testing.T) {
	client := NewClient()
	_, err := client.Refresh(context.Background(), "  ")
	if err == nil || !strings.Contains(err.Error(), "log in again") {
		t.Fatalf("Refresh = %v, want a 'log in again' the user can act on", err)
	}
}

func TestToken_DefaultsAnAbsentExpiry(t *testing.T) {
	endpoint := &tokenEndpoint{body: `{"access_token":"at","refresh_token":"rt"}`}
	issuer := httptest.NewServer(endpoint.handler())
	defer issuer.Close()
	client := NewClient(WithIssuer(issuer.URL))
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	client.now = func() time.Time { return now }

	tokens, err := client.Refresh(context.Background(), "rt-1")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if want := now.Add(time.Hour - expirySkew); !tokens.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt = %v, want the one-hour default minus the skew (%v)", tokens.ExpiresAt, want)
	}
}

func TestToken_FailuresAreLegibleAndRedacted(t *testing.T) {
	cases := []struct {
		name  string
		stub  *tokenEndpoint
		wants string
	}{
		{"server failure", &tokenEndpoint{status: http.StatusBadRequest, body: `{"error":"invalid_grant"}`}, "invalid_grant"},
		{"unreadable body", &tokenEndpoint{body: `not json`}, "cannot read"},
		{"no access token", &tokenEndpoint{body: `{"refresh_token":"rt"}`}, "no access token"},
		{"no refresh token", &tokenEndpoint{body: `{"access_token":"at"}`}, "no refresh token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issuer := httptest.NewServer(tc.stub.handler())
			defer issuer.Close()
			client := NewClient(WithIssuer(issuer.URL))
			_, err := client.Refresh(context.Background(), "rt-secret")
			if err == nil || !strings.Contains(err.Error(), tc.wants) {
				t.Fatalf("Refresh = %v, want it to mention %q", err, tc.wants)
			}
			if strings.Contains(err.Error(), "rt-secret") || strings.Contains(err.Error(), "at\"") {
				t.Fatalf("error %q quotes a credential", err)
			}
		})
	}
}

func TestClientID_FollowsTheIssuer(t *testing.T) {
	cases := []struct {
		issuer string
		want   string
	}{
		{"", usClientID},
		{"https://us.posthog.com", usClientID},
		{"https://eu.posthog.com", euClientID},
		{"https://eu.posthog.com/", euClientID},
		{"http://localhost:8010", usClientID},
	}
	for _, tc := range cases {
		client := NewClient(WithIssuer(tc.issuer))
		if got := client.clientID(); got != tc.want {
			t.Errorf("clientID(%q) = %q, want %q", tc.issuer, got, tc.want)
		}
	}
}

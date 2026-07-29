package openaiauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// jwtWith builds a token whose claims segment carries claims. The signature is
// not verified anywhere — see AccountID — so it is a literal.
func jwtWith(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

// issuer stands in for auth.openai.com. answer is what every request goes to, and
// the client wired to it does not wait between polls, so a flow that takes a human
// half a minute takes a test a millisecond.
type issuer struct {
	server *httptest.Server
	answer http.HandlerFunc
	mu     sync.Mutex
	// waits records every interval the flow slept, which is the only observable
	// difference a slow_down makes.
	waits []time.Duration
	polls int
}

// newIssuer stands the stub up before its handler exists, because a handler that
// counts polls has to close over the stub counting them.
func newIssuer(t *testing.T) (*issuer, *Client) {
	t.Helper()
	stub := &issuer{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.answer(w, r)
	}))
	t.Cleanup(stub.server.Close)
	client := NewClient(WithIssuer(stub.server.URL))
	client.sleep = func(ctx context.Context, d time.Duration) error {
		stub.mu.Lock()
		stub.waits = append(stub.waits, d)
		stub.mu.Unlock()
		return ctx.Err()
	}
	return stub, client
}

func (s *issuer) poll() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.polls++
	return s.polls
}

func (s *issuer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.polls
}

func (s *issuer) slept() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.waits...)
}

// tokenResponse is the successful body of /oauth/token, with an access token that
// names an account.
func tokenResponse(t *testing.T, accountID, refresh string) string {
	t.Helper()
	access := jwtWith(t, map[string]any{"chatgpt_account_id": accountID})
	body, err := json.Marshal(map[string]any{
		"access_token":  access,
		"refresh_token": refresh,
		"id_token":      jwtWith(t, map[string]any{}),
		"expires_in":    3600,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// TestClient_RequestDeviceReadsAnIntervalSentAsAString: this server sends
// interval as a JSON string, so a client that insists on a number polls at its
// own default and earns a slow_down. Both spellings have to land.
func TestClient_RequestDeviceReadsAnIntervalSentAsAString(t *testing.T) {
	expires := time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)
	stub, client := newIssuer(t)
	stub.answer = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/accounts/deviceauth/usercode" {
			t.Errorf("usercode request went to %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), ClientID) {
			t.Errorf("usercode body = %s, want the Codex client id", body)
		}
		io.WriteString(w, `{"device_auth_id":"deviceauth_1","user_code":"V3H5-1MW96","interval":"7","expires_at":"`+expires+`"}`)
	}

	device, err := client.RequestDevice(context.Background())
	if err != nil {
		t.Fatalf("RequestDevice: %v", err)
	}
	if device.UserCode != "V3H5-1MW96" || device.ID != "deviceauth_1" {
		t.Fatalf("RequestDevice() = %#v, want the code and the login handle", device)
	}
	if device.Interval != 7*time.Second {
		t.Errorf("Interval = %s, want 7s parsed out of the JSON string", device.Interval)
	}
	if device.ExpiresAt.IsZero() {
		t.Error("ExpiresAt is zero: the server's own expiry is what bounds the wait, and dropping it means polling a dead code")
	}
	// Derived from the issuer, so a client pointed elsewhere does not send the user
	// to a page that knows nothing about the code it just minted.
	if want := stub.server.URL + verificationPath; device.VerificationURI != want {
		t.Errorf("VerificationURI = %q, want %q", device.VerificationURI, want)
	}
}

// TestClient_RequestDeviceFallsBackWhenTheServerNamesNoIntervalOrExpiry: absence
// is not a failure — the flow has its own floor and its own cap.
func TestClient_RequestDeviceFallsBackWhenTheServerNamesNoIntervalOrExpiry(t *testing.T) {
	stub, client := newIssuer(t)
	stub.answer = func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"device_auth_id":"d","user_code":"AAAA-BBBB","interval":"nonsense"}`)
	}

	device, err := client.RequestDevice(context.Background())
	if err != nil {
		t.Fatalf("RequestDevice: %v", err)
	}
	if device.Interval != defaultPollInterval {
		t.Errorf("Interval = %s, want the default %s", device.Interval, defaultPollInterval)
	}
	if !device.ExpiresAt.IsZero() {
		t.Errorf("ExpiresAt = %s, want zero so Await applies its own cap", device.ExpiresAt)
	}
}

// TestClient_AwaitPollsUntilApprovedThenExchangesTheCode: a login nobody has
// approved yet answers 403 and 200-with-a-pending-code interchangeably, and
// neither is a failure. The exchange carries the verifier the SERVER minted —
// there is no local PKCE in this flow.
func TestClient_AwaitPollsUntilApprovedThenExchangesTheCode(t *testing.T) {
	var exchanged url.Values
	stub, client := newIssuer(t)
	stub.answer = func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/accounts/deviceauth/token":
			switch stub.poll() {
			case 1:
				w.WriteHeader(http.StatusForbidden)
				io.WriteString(w, `{"detail":"not yet"}`)
			case 2:
				io.WriteString(w, `{"error":"deviceauth_authorization_pending"}`)
			default:
				io.WriteString(w, `{"authorization_code":"ac_123","code_verifier":"cv_456"}`)
			}
		case "/oauth/token":
			body, _ := io.ReadAll(r.Body)
			exchanged, _ = url.ParseQuery(string(body))
			io.WriteString(w, tokenResponse(t, "acct_9", "rt_new"))
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
		}
	}

	tokens, err := client.Await(context.Background(), Device{ID: "d", UserCode: "AAAA-BBBB", Interval: time.Second, ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if tokens.AccountID != "acct_9" || tokens.RefreshToken != "rt_new" || tokens.AccessToken == "" {
		t.Fatalf("Await() = %#v, want the issued credential with its account", tokens)
	}
	if tokens.ExpiresAt.Before(time.Now().Add(59 * time.Minute)) {
		t.Errorf("ExpiresAt = %s, want expires_in applied to now", tokens.ExpiresAt)
	}
	if exchanged.Get("grant_type") != "authorization_code" || exchanged.Get("code") != "ac_123" ||
		exchanged.Get("code_verifier") != "cv_456" || exchanged.Get("redirect_uri") != redirectURI ||
		exchanged.Get("client_id") != ClientID {
		t.Fatalf("exchange form = %v, want the server's own code and verifier", exchanged)
	}
}

// TestClient_AwaitBacksOffWhenTheServerSaysSlowDown: the interval is a floor, and
// a server that says it was crossed is asking for a wider one — polling at the
// same rate afterwards is how a client gets throttled out of a flow it could have
// finished.
func TestClient_AwaitBacksOffWhenTheServerSaysSlowDown(t *testing.T) {
	stub, client := newIssuer(t)
	stub.answer = func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/accounts/deviceauth/token":
			if stub.poll() <= 2 {
				io.WriteString(w, `{"error":{"code":"slow_down"}}`)
				return
			}
			io.WriteString(w, `{"authorization_code":"ac","code_verifier":"cv"}`)
		case "/oauth/token":
			io.WriteString(w, tokenResponse(t, "acct_1", "rt"))
		}
	}

	if _, err := client.Await(context.Background(), Device{ID: "d", UserCode: "c", Interval: time.Second, ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatalf("Await: %v", err)
	}
	waits := stub.slept()
	if len(waits) != 2 {
		t.Fatalf("slept %v, want one wait after each unapproved poll", waits)
	}
	if waits[1] <= waits[0] {
		t.Fatalf("slept %v, want the second wait wider after the second slow_down", waits)
	}
}

// TestClient_AwaitTreatsASlowDownCarriedOnA429AsABackoffNotAsAnOutage: the
// authorization server is free to send its "poll less often" instruction on a 429,
// which is also the status a throttling edge answers with. The two readings are
// opposite — one says the login is fine and asks for patience, the other counts
// against the outage budget — so the error code has to be read before the status.
//
// Nothing else pins that order: the slow_down test answers 200 and the outage test
// answers a 429 with no code in it, so both stay green with the branches swapped
// while ten throttled polls kill a login the server was only pacing.
func TestClient_AwaitTreatsASlowDownCarriedOnA429AsABackoffNotAsAnOutage(t *testing.T) {
	const throttled = maxTransientFailures + 2
	stub, client := newIssuer(t)
	stub.answer = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			io.WriteString(w, tokenResponse(t, "acct_after_the_throttling", "rt"))
			return
		}
		if stub.poll() <= throttled {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"code":"slow_down"}}`)
			return
		}
		io.WriteString(w, `{"authorization_code":"ac","code_verifier":"cv"}`)
	}

	tokens, err := client.Await(context.Background(), Device{ID: "d", UserCode: "c", Interval: time.Second, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if tokens.AccountID != "acct_after_the_throttling" {
		t.Fatalf("Await() = %#v, want the login that completed once the throttling let up", tokens)
	}
	waits := stub.slept()
	if len(waits) != throttled {
		t.Fatalf("slept %v, want one wait after each of the %d throttled polls", waits, throttled)
	}
	for i := 1; i < len(waits); i++ {
		if waits[i] <= waits[i-1] {
			t.Fatalf("slept %v, want every slow_down to widen the interval", waits)
		}
	}
}

// TestClient_AwaitPollsBeforeItWaits: a code the user already approved must not
// cost a whole interval of waiting first. It is one request against a blind wait
// on every login there will ever be, and it is what makes the flow drivable end to
// end from another package's tests.
func TestClient_AwaitPollsBeforeItWaits(t *testing.T) {
	stub, client := newIssuer(t)
	stub.answer = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			io.WriteString(w, tokenResponse(t, "acct", "rt"))
			return
		}
		stub.poll()
		io.WriteString(w, `{"authorization_code":"ac","code_verifier":"cv"}`)
	}
	client.sleep = func(context.Context, time.Duration) error {
		t.Error("Await waited before its first poll")
		return nil
	}

	if _, err := client.Await(context.Background(), Device{ID: "d", UserCode: "c", Interval: time.Hour, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("Await: %v", err)
	}
	if got := stub.slept(); len(got) != 0 {
		t.Fatalf("slept %v on an already-approved code, want no waiting at all", got)
	}
}

// TestClient_AwaitStopsWhenTheCodeExpires: the server's expires_at is the
// authority on when the code dies, and the user is told to start over rather than
// reading a context deadline.
func TestClient_AwaitStopsWhenTheCodeExpires(t *testing.T) {
	stub, client := newIssuer(t)
	stub.answer = func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"error":"deviceauth_authorization_pending"}`)
	}
	// Real waiting, so the deadline is what ends the loop rather than the stub.
	client.sleep = sleep

	start := time.Now()
	_, err := client.Await(context.Background(), Device{ID: "d", UserCode: "c", Interval: time.Second, ExpiresAt: time.Now().Add(30 * time.Millisecond)})
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("Await() error = %v, want one naming the expired code", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Await waited %s past an expiry 30ms out: the server's expiry has to bound the loop", elapsed)
	}
}

// TestClient_AwaitReportsAnAnswerTheNextPollWouldRepeat: a 400 is not a pending
// login and not a blip either — an unknown device id, a bad request or a revoked
// client reads the same on every poll after it. Treating those as "keep waiting"
// is how a broken request looks like a user who never approves, so the first one
// ends the wait.
func TestClient_AwaitReportsAnAnswerTheNextPollWouldRepeat(t *testing.T) {
	stub, client := newIssuer(t)
	stub.answer = func(w http.ResponseWriter, _ *http.Request) {
		stub.poll()
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"detail":"unknown device_auth_id"}`)
	}

	_, err := client.Await(context.Background(), Device{ID: "d", UserCode: "c", ExpiresAt: time.Now().Add(time.Minute)})
	if err == nil {
		t.Fatal("Await: expected the rejected poll to surface")
	}
	if !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "unknown device_auth_id") {
		t.Fatalf("Await() error = %v, want the status and the reason", err)
	}
	if polls := stub.count(); polls != 1 {
		t.Fatalf("polled %d times, want the first answer to end a wait nothing about it would change", polls)
	}
}

// TestClient_AwaitSurvivesAServerFailureBetweenPolls: the user is in their browser
// approving the code and the issuer's edge answers one poll with a 503. The code is
// as approvable as it was a second earlier — exactly as approvable as it is through
// a DNS blip — so ending the login there paints a gateway's HTML at a user who was
// one poll away from being logged in.
func TestClient_AwaitSurvivesAServerFailureBetweenPolls(t *testing.T) {
	for name, status := range map[string]int{
		"a gateway that is down":     http.StatusServiceUnavailable,
		"an edge that is throttling": http.StatusTooManyRequests,
		"a backend that broke":       http.StatusInternalServerError,
	} {
		t.Run(name, func(t *testing.T) {
			stub, client := newIssuer(t)
			stub.answer = func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/oauth/token" {
					io.WriteString(w, tokenResponse(t, "acct_after_the_outage", "rt"))
					return
				}
				if stub.poll() == 1 {
					w.WriteHeader(status)
					io.WriteString(w, "<html><body>Service Unavailable</body></html>")
					return
				}
				io.WriteString(w, `{"authorization_code":"ac","code_verifier":"cv"}`)
			}

			tokens, err := client.Await(context.Background(), Device{ID: "d", UserCode: "c", Interval: time.Second, ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatalf("Await: %v", err)
			}
			if tokens.AccountID != "acct_after_the_outage" {
				t.Fatalf("Await() = %#v, want the login the next poll completed", tokens)
			}
			if waits := stub.slept(); len(waits) != 1 {
				t.Fatalf("slept %v, want one wait between the failed poll and the one that answered", waits)
			}
		})
	}
}

// TestClient_AwaitGivesUpOnAServerThatKeepsFailing: tolerance is for a blink. A run
// of failures is the issuer being down, and it spends the same budget an
// unreachable network does — but says which of the two it was, because "check the
// connection" is the wrong thing to tell someone whose connection is fine.
func TestClient_AwaitGivesUpOnAServerThatKeepsFailing(t *testing.T) {
	stub, client := newIssuer(t)
	stub.answer = func(w http.ResponseWriter, _ *http.Request) {
		stub.poll()
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, `{"detail":"deviceauth is down"}`)
	}

	_, err := client.Await(context.Background(), Device{ID: "d", UserCode: "c", Interval: time.Second, ExpiresAt: time.Now().Add(time.Minute)})
	if err == nil {
		t.Fatal("Await: expected an authorization server that never answers to surface")
	}
	if !strings.Contains(err.Error(), "await a ChatGPT login") || !strings.Contains(err.Error(), "kept failing") {
		t.Fatalf("Await() error = %v, want the package's own prefix and what happened", err)
	}
	if !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "deviceauth is down") {
		t.Fatalf("Await() error = %v, want the last answer the server gave", err)
	}
	if strings.Contains(err.Error(), "check the connection") {
		t.Fatalf("Await() error = %v, blames a connection that answered every request", err)
	}
	if polls := stub.count(); polls != maxTransientFailures {
		t.Fatalf("polled %d times, want the shared budget of %d spent", polls, maxTransientFailures)
	}
}

// TestClient_AwaitNeverQuotesABodyThatCouldHaveCarriedAGrant: a 200 is the shape
// that carries the authorization code and the verifier minted for it. When this
// build cannot read one, the failure still travels to a TUI panel, a Wails event
// and a log file — so it names the status and nothing else. This is the contract
// at the top of the package, and the only path that could break it.
func TestClient_AwaitNeverQuotesABodyThatCouldHaveCarriedAGrant(t *testing.T) {
	cases := map[string]string{
		"a grant missing its verifier":    `{"authorization_code":"ac-super-secret"}`,
		"a shape this build cannot parse": `{"authorization_code":"ac-super-secret","code_verifier":`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			stub, client := newIssuer(t)
			stub.answer = func(w http.ResponseWriter, _ *http.Request) {
				io.WriteString(w, body)
			}

			_, err := client.Await(context.Background(), Device{ID: "d", UserCode: "c", ExpiresAt: time.Now().Add(time.Minute)})
			if err == nil {
				t.Fatal("Await: expected an answer this build cannot read to surface")
			}
			if strings.Contains(err.Error(), "ac-super-secret") {
				t.Fatalf("Await() error quotes the authorization code: %v", err)
			}
			if !strings.Contains(err.Error(), "await a ChatGPT login") {
				t.Fatalf("Await() error = %v, want the package's own prefix", err)
			}
		})
	}
}

// TestClient_AwaitSurvivesATransportFailureBetweenPolls: a DNS blip, a connection
// reset or a wifi switch forty seconds into a ten-minute window leaves the code
// approvable server-side. This is the one flow designed for the user to walk away
// from the machine, so a poll that never arrived must not end a login that was
// going to succeed.
func TestClient_AwaitSurvivesATransportFailureBetweenPolls(t *testing.T) {
	stub, client := newIssuer(t)
	stub.answer = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			io.WriteString(w, tokenResponse(t, "acct_after_the_blip", "rt"))
			return
		}
		io.WriteString(w, `{"authorization_code":"ac","code_verifier":"cv"}`)
	}
	client.http = &http.Client{Transport: &flakyTransport{inner: http.DefaultTransport, failures: 1}}

	tokens, err := client.Await(context.Background(), Device{ID: "d", UserCode: "c", Interval: time.Second, ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if tokens.AccountID != "acct_after_the_blip" {
		t.Fatalf("Await() = %#v, want the login the retry completed", tokens)
	}
	if waits := stub.slept(); len(waits) != 1 {
		t.Fatalf("slept %v, want one wait between the failed poll and the one that answered", waits)
	}
}

// TestClient_AwaitGivesUpOnANetworkThatIsGoneRatherThanBlinking: tolerance is for
// a blip. A run of unreachable polls is the connection being down, and polling
// silently until the code dies would tell the user nothing about why.
func TestClient_AwaitGivesUpOnANetworkThatIsGoneRatherThanBlinking(t *testing.T) {
	stub, client := newIssuer(t)
	stub.answer = func(http.ResponseWriter, *http.Request) {
		t.Error("no poll can reach the server while the network is gone")
	}
	client.http = &http.Client{Transport: &flakyTransport{inner: http.DefaultTransport, failures: maxTransientFailures}}

	_, err := client.Await(context.Background(), Device{ID: "d", UserCode: "c", Interval: time.Second, ExpiresAt: time.Now().Add(time.Minute)})
	if err == nil {
		t.Fatal("Await: expected an unreachable authorization server to surface")
	}
	if !strings.Contains(err.Error(), "await a ChatGPT login") || !strings.Contains(err.Error(), "could not be reached") {
		t.Fatalf("Await() error = %v, want the package's own prefix and what to do", err)
	}
}

// flakyTransport fails the first `failures` requests through it at the transport,
// the way a reconnecting interface does, and lets everything after them through.
type flakyTransport struct {
	inner    http.RoundTripper
	mu       sync.Mutex
	failures int
}

func (f *flakyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	f.mu.Lock()
	fail := f.failures > 0
	if fail {
		f.failures--
	}
	f.mu.Unlock()
	if fail {
		return nil, errors.New("dial tcp: lookup auth.openai.com: no such host")
	}
	return f.inner.RoundTrip(request)
}

// TestClient_AwaitStopsWhenTheCallerCancels: closing the login panel cancels the
// wait, and it has to come back promptly and unambiguously.
func TestClient_AwaitStopsWhenTheCallerCancels(t *testing.T) {
	stub, client := newIssuer(t)
	stub.answer = func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"error":"deviceauth_authorization_pending"}`)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.Await(ctx, Device{ID: "d", UserCode: "c", ExpiresAt: time.Now().Add(time.Minute)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Await() error = %v, want context.Canceled", err)
	}
}

// TestClient_AwaitRefusesACredentialThatNamesNoAccount: every request to the
// codex backend carries the account as a header, so a credential without one is
// unusable. Failing the login is the legible version of the opaque rejection the
// first turn would otherwise get.
func TestClient_AwaitRefusesACredentialThatNamesNoAccount(t *testing.T) {
	stub, client := newIssuer(t)
	stub.answer = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			io.WriteString(w, `{"access_token":"`+jwtWith(t, map[string]any{"sub": "user_1"})+`","refresh_token":"rt","expires_in":3600}`)
			return
		}
		io.WriteString(w, `{"authorization_code":"ac","code_verifier":"cv"}`)
	}

	_, err := client.Await(context.Background(), Device{ID: "d", UserCode: "c", ExpiresAt: time.Now().Add(time.Minute)})
	if err == nil || !strings.Contains(err.Error(), "ChatGPT account") {
		t.Fatalf("Await() error = %v, want one naming the missing account", err)
	}
}

// TestClient_AwaitRefusesACredentialWithNoRefreshToken: without one the
// credential dies in an hour with no way back, which is worse than not storing
// it.
func TestClient_AwaitRefusesACredentialWithNoRefreshToken(t *testing.T) {
	stub, client := newIssuer(t)
	stub.answer = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			io.WriteString(w, `{"access_token":"`+jwtWith(t, map[string]any{"chatgpt_account_id": "a"})+`","expires_in":3600}`)
			return
		}
		io.WriteString(w, `{"authorization_code":"ac","code_verifier":"cv"}`)
	}

	_, err := client.Await(context.Background(), Device{ID: "d", UserCode: "c", ExpiresAt: time.Now().Add(time.Minute)})
	if err == nil || !strings.Contains(err.Error(), "refresh token") {
		t.Fatalf("Await() error = %v, want one naming the missing refresh token", err)
	}
}

// TestClient_RefreshRotatesTheRefreshToken: the token the server returns replaces
// the one sent. A caller that keeps the old one is logged out at the next renewal,
// so this is the fact the storage layer has to honor.
func TestClient_RefreshRotatesTheRefreshToken(t *testing.T) {
	var sent url.Values
	stub, client := newIssuer(t)
	stub.answer = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Errorf("refresh went to %s", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q, want the form encoding the token endpoint takes", got)
		}
		body, _ := io.ReadAll(r.Body)
		sent, _ = url.ParseQuery(string(body))
		io.WriteString(w, tokenResponse(t, "acct_2", "rt_rotated"))
	}

	tokens, err := client.Refresh(context.Background(), "rt_old")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tokens.RefreshToken != "rt_rotated" {
		t.Fatalf("RefreshToken = %q, want the rotated one", tokens.RefreshToken)
	}
	if sent.Get("grant_type") != "refresh_token" || sent.Get("refresh_token") != "rt_old" || sent.Get("client_id") != ClientID {
		t.Fatalf("refresh form = %v, want the stored token and the Codex client id", sent)
	}
}

// TestClient_RefreshReportsARejectedTokenWithoutQuotingIt: a revoked credential
// has to be legible, and the error travels to a log file, so the token itself
// must not be in it.
func TestClient_RefreshReportsARejectedTokenWithoutQuotingIt(t *testing.T) {
	stub, client := newIssuer(t)
	stub.answer = func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"invalid_grant"}`)
	}

	_, err := client.Refresh(context.Background(), "rt-secret-value")
	if err == nil {
		t.Fatal("Refresh: expected the rejection to surface")
	}
	if strings.Contains(err.Error(), "rt-secret-value") {
		t.Fatalf("Refresh() error quotes the refresh token: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("Refresh() error = %v, want the reason the server gave", err)
	}
}

func TestClient_RefreshRefusesAnEmptyToken(t *testing.T) {
	stub, client := newIssuer(t)
	stub.answer = func(http.ResponseWriter, *http.Request) {
		t.Error("Refresh with no token must not reach the network")
	}
	if _, err := client.Refresh(context.Background(), "  "); err == nil {
		t.Fatal("Refresh(\"\"): expected an error")
	}
}

// TestAccountID_AcceptsEveryPlaceTheClaimIsWritten: three implementations read
// this claim from three places, and a credential is unusable without it.
func TestAccountID_AcceptsEveryPlaceTheClaimIsWritten(t *testing.T) {
	cases := []struct {
		name   string
		claims map[string]any
		want   string
	}{
		{"top level", map[string]any{"chatgpt_account_id": "acct_top"}, "acct_top"},
		{"namespaced auth claim", map[string]any{
			"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct_nested"},
		}, "acct_nested"},
		{"first organization", map[string]any{
			"organizations": []any{map[string]any{"id": "org_1"}},
		}, "org_1"},
		{"nothing to read", map[string]any{"sub": "user"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := AccountID(jwtWith(t, c.claims)); got != c.want {
				t.Fatalf("AccountID() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestAccountID_FallsThroughToTheNextToken: the access token is the one to read,
// but opencode reads the id token, so a credential that only names the account
// there still has to work.
func TestAccountID_FallsThroughToTheNextToken(t *testing.T) {
	access := jwtWith(t, map[string]any{"sub": "user"})
	id := jwtWith(t, map[string]any{"chatgpt_account_id": "acct_from_id_token"})
	if got := AccountID(access, id); got != "acct_from_id_token" {
		t.Fatalf("AccountID() = %q, want the claim from the id token", got)
	}
	if got := AccountID("not-a-jwt", "also.not!base64.x"); got != "" {
		t.Fatalf("AccountID() = %q on garbage, want empty", got)
	}
}

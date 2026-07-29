// Package openaiauth speaks the OAuth flow that turns a ChatGPT subscription
// into a credential atenea can send.
//
// It is the device-code flow and only that. The alternative — a loopback
// redirect on a fixed local port — needs a browser on the same machine as the
// process, which the terminal host cannot promise: atenea runs over SSH, in a
// container, on a machine with no desktop session. A code the user types
// somewhere else works everywhere the other one does and in the places it does
// not.
//
// The endpoints below are OpenAI's own and are NOT RFC 8628. They are
// undocumented; what is known about them is written down in
// .okf/research/2026-07-27-openai-chatgpt-oauth-device-code.md, which is where a
// reader should go before changing anything here.
//
// Nothing this package returns as an error carries a token, an authorization
// code or a code verifier. Errors from here reach a TUI panel, a Wails event and
// a log file.
package openaiauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultIssuer is the authorization server that issues subscription
	// credentials.
	DefaultIssuer = "https://auth.openai.com"
	// ClientID is the official Codex CLI client. OpenAI offers no client
	// registration for this flow, so every third-party implementation — opencode,
	// pi — uses this same id; the ToS consideration that follows is recorded in
	// .okf/specs/2026-07-27-openai-subscription-oauth.md rather than here, because
	// it is a product decision and not a fact about the protocol.
	ClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	// verificationPath is the page where the user enters the code, relative to the
	// issuer. It is derived from the issuer rather than hardcoded whole, so a client
	// pointed at another authorization server sends the user to that server's page
	// instead of to a page that knows nothing about the code it just minted.
	verificationPath = "/codex/device"
	// redirectURI is not navigated to by anyone: the device flow has no browser
	// redirect. The token endpoint still demands the value, because the code being
	// exchanged was minted for it.
	redirectURI = "https://auth.openai.com/deviceauth/callback"

	// defaultPollInterval is how often to ask whether the code was approved when
	// the server names no interval of its own.
	defaultPollInterval = 5 * time.Second
	// pollMargin is added to whatever interval the server asked for. Both
	// reference implementations do it, and the reason is that the interval is a
	// floor: polling exactly on it is how a client earns a slow_down.
	pollMargin = 3 * time.Second
	// maxDeviceLifetime caps how long a login may stay pending when the server
	// sends no expiry, or one this build cannot parse. The server's own
	// expires_at wins whenever there is one — it is the authority on when the
	// code dies, and hardcoding a timeout instead is how a client keeps polling a
	// code that is already dead.
	maxDeviceLifetime = 15 * time.Minute
	// maxQuotedBody caps how much of a failed response is quoted back, so a
	// failure reads as a sentence in a panel instead of pasting a gateway's HTML.
	maxQuotedBody = 256
	// requestTimeout bounds one HTTP call. Polling happens in a loop, so a single
	// hung request must not eat the whole login's deadline.
	requestTimeout = 30 * time.Second
	// maxTransientFailures is how many polls in a row may fail before the trouble
	// counts as an outage rather than as a blink. This is the one flow designed for
	// the user to walk away from the machine, so neither a wifi switch nor a 503
	// out of the issuer's edge — the code survives both — may end a login the next
	// poll would have completed; a run of them is no longer a blink, and polling
	// silently until the code dies would tell the user nothing.
	maxTransientFailures = 10
)

// Device is one pending device-code login: what to show the user and how long
// they have to act on it.
type Device struct {
	// ID is the server's handle for this login (device_auth_id). It is not a
	// secret and not shown: it is what every poll is about.
	ID string
	// UserCode is what the user types at VerificationURI.
	UserCode string
	// VerificationURI is the page to type it into.
	VerificationURI string
	// Interval is the floor between two polls, as the server asked for it.
	Interval time.Duration
	// ExpiresAt is when the code stops being approvable. Zero means the server
	// did not say, and then maxDeviceLifetime applies.
	ExpiresAt time.Time
}

// Tokens is one issued credential set, already resolved down to what a request
// needs: the bearer token, the refresh token that renews it, when it stops being
// accepted, and the account the endpoint routes on.
type Tokens struct {
	AccessToken string
	// RefreshToken ROTATES on every refresh. A caller that does not persist the
	// new one logs the user out at the following renewal.
	RefreshToken string
	ExpiresAt    time.Time
	AccountID    string
}

// Client talks to one issuer as one client id.
//
// now and sleep are the seams this package's own tests replace; a caller gets
// the real clock and the real waiting, which is the only sane default for
// something whose whole job is to wait for a human.
type Client struct {
	issuer   string
	clientID string
	http     *http.Client
	now      func() time.Time
	sleep    func(ctx context.Context, d time.Duration) error
}

// Option adjusts a Client at construction, keeping the constructor stable.
type Option func(*Client)

// WithIssuer points the client at another authorization server. It is what makes
// the flow testable against a stub, and what lets the issuer be configuration
// rather than a constant compiled into an adapter.
func WithIssuer(issuer string) Option {
	return func(c *Client) {
		if issuer = strings.TrimRight(strings.TrimSpace(issuer), "/"); issuer != "" {
			c.issuer = issuer
		}
	}
}

// WithHTTPClient replaces the transport.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) {
		if client != nil {
			c.http = client
		}
	}
}

func NewClient(opts ...Option) *Client {
	c := &Client{
		issuer:   DefaultIssuer,
		clientID: ClientID,
		http:     &http.Client{Timeout: requestTimeout},
		now:      time.Now,
		sleep:    sleep,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// RequestDevice starts a login and returns the code to show the user.
//
// It is deliberately separate from Await: a host has to paint the code before
// anything waits on it, and the two have different lifetimes — the request is
// bounded by the UI action that triggered it, the wait by the user.
func (c *Client) RequestDevice(ctx context.Context) (Device, error) {
	var body struct {
		DeviceAuthID string        `json:"device_auth_id"`
		UserCode     string        `json:"user_code"`
		Interval     flexSeconds   `json:"interval"`
		ExpiresAt    flexTimestamp `json:"expires_at"`
	}
	if err := c.postJSON(ctx, "/api/accounts/deviceauth/usercode", map[string]string{"client_id": c.clientID}, &body); err != nil {
		return Device{}, err
	}
	if body.DeviceAuthID == "" || body.UserCode == "" {
		return Device{}, errors.New("start a ChatGPT login: the authorization server issued no code")
	}
	device := Device{
		ID:              body.DeviceAuthID,
		UserCode:        body.UserCode,
		VerificationURI: c.issuer + verificationPath,
		Interval:        body.Interval.Duration(defaultPollInterval),
		ExpiresAt:       body.ExpiresAt.Time(),
	}
	return device, nil
}

// Await polls until the user approves the code, then exchanges it for tokens.
//
// It returns when the login completes, when ctx is cancelled (the shape closing
// the panel takes) or when the code expires — whichever comes first. The deadline
// is the server's own expires_at, so a client never keeps polling a code that is
// already dead.
//
// A poll that says nothing about the code is not one of those endings — neither
// one that never reached the server nor one it answered with a failure of its
// own. This is the flow a user walks away from the machine for, and trouble in the
// middle of it leaves the code approvable, so the loop keeps going until the
// deadline and only [maxTransientFailures] in a row end it.
func (c *Client) Await(ctx context.Context, device Device) (Tokens, error) {
	if device.ID == "" || device.UserCode == "" {
		return Tokens{}, errors.New("await a ChatGPT login: no pending login to wait for")
	}
	deadline := device.ExpiresAt
	if deadline.IsZero() || !deadline.After(c.now()) {
		deadline = c.now().Add(maxDeviceLifetime)
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	interval := device.Interval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	failures := 0
	// The first poll goes out immediately. The code may already have been
	// approved — the user can have had the page open, or a host can be resuming a
	// login it started before — and asking costs one request against waiting the
	// whole interval on every login that ever happens.
	for {
		grant, outcome, err := c.poll(ctx, device)
		switch summary, transient := transientFailure(err); {
		case transient:
			// The poll said nothing about the login: the code is still approvable and
			// the user is still in their browser. Only a run of them ends the wait, and
			// the summary names which kind of trouble it turned out to be.
			if failures++; failures >= maxTransientFailures {
				return Tokens{}, awaitInterrupted(ctx, fmt.Errorf("await a ChatGPT login: %s: %w", summary, err))
			}
		case err != nil:
			return Tokens{}, awaitInterrupted(ctx, err)
		case outcome == pollAuthorized:
			return c.exchange(ctx, grant)
		case outcome == pollSlowDown:
			failures = 0
			interval += pollMargin
		default:
			failures = 0
		}
		if err := c.sleep(ctx, interval+pollMargin); err != nil {
			return Tokens{}, awaitInterrupted(ctx, err)
		}
	}
}

// transientError marks a failure that says nothing about whether the code was
// approved: a request that never got an answer out of the authorization server —
// DNS, the dial, the read — and one the server answered with a failure of its own,
// a 5xx or a bare 429. The code outlives all of them, so they share one budget
// instead of ending a login the next poll would have finished.
//
// summary is what a run of them is called once that budget is gone. The two have
// different remedies, and "check the connection" is the wrong thing to tell
// someone whose connection is fine and whose issuer is down.
type transientError struct {
	summary string
	err     error
}

func (e transientError) Error() string { return e.err.Error() }
func (e transientError) Unwrap() error { return e.err }

// unreachable marks a request that never got an answer. Every transport failure
// carries it, not only a poll's, but only [Await] acts on the distinction — the
// requests that are not polls have a caller waiting on them and report what
// happened as it is.
func unreachable(err error) error {
	return transientError{summary: "the authorization server could not be reached; check the connection and start the login again", err: err}
}

// answeredWithFailure marks a status the authorization server itself could not
// serve. Only a poll produces one: the other requests have no code to protect and
// nothing to gain from outliving a refusal.
func answeredWithFailure(err error) error {
	return transientError{summary: "the authorization server kept failing; start the login again", err: err}
}

func transientFailure(err error) (string, bool) {
	var transient transientError
	if errors.As(err, &transient) {
		return transient.summary, true
	}
	return "", false
}

// awaitInterrupted names the two ways waiting ends without an answer, so the
// user reads why instead of reading "context deadline exceeded".
func awaitInterrupted(ctx context.Context, err error) error {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return errors.New("the ChatGPT login code expired before it was approved; start the login again")
	case errors.Is(ctx.Err(), context.Canceled):
		return context.Canceled
	default:
		return err
	}
}

type pollOutcome int

const (
	pollPending pollOutcome = iota
	pollSlowDown
	pollAuthorized
)

// grant is the authorized login's proof, both halves of it secret: OpenAI mints
// the PKCE verifier server-side and hands it back here, which is why this flow
// has no local PKCE at all.
type grant struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
}

// poll asks once whether the code was approved.
//
// The status codes are the surprising part and they are not ours to fix: a login
// nobody has approved yet answers 403 or 404 as readily as it answers 200 with a
// pending error code. Treating either as a failure is how a client gives up three
// seconds into a flow that takes a human thirty.
func (c *Client) poll(ctx context.Context, device Device) (grant, pollOutcome, error) {
	status, body, err := c.post(ctx, "/api/accounts/deviceauth/token", jsonBody(map[string]string{
		"device_auth_id": device.ID,
		"user_code":      device.UserCode,
	}))
	if err != nil {
		return grant{}, pollPending, err
	}
	var payload struct {
		grant
		Error errorCode `json:"error"`
	}
	// A body this build cannot parse is only fatal on a status that was not going
	// to carry a grant anyway.
	decodeErr := json.Unmarshal(body, &payload)
	switch payload.Error.code {
	case "deviceauth_authorization_pending", "authorization_pending":
		return grant{}, pollPending, nil
	case "slow_down":
		return grant{}, pollSlowDown, nil
	}
	if status == http.StatusOK && decodeErr == nil && payload.AuthorizationCode != "" && payload.CodeVerifier != "" {
		return payload.grant, pollAuthorized, nil
	}
	if status == http.StatusForbidden || status == http.StatusNotFound {
		return grant{}, pollPending, nil
	}
	// A 5xx is the issuer's edge failing and a 429 that is not a slow_down is it
	// throttling; neither is an answer about the code, which stays approvable
	// through both. They are as transient as a DNS blip, so they spend the same
	// budget rather than ending a login while the user is mid-approval. Everything
	// else — a bad request, a revoked client, an unknown device id — will read the
	// same on the next poll and ends the wait now.
	if status >= http.StatusInternalServerError || status == http.StatusTooManyRequests {
		return grant{}, pollPending, answeredWithFailure(fmt.Errorf("%s: %s", statusText(status), quote(body)))
	}
	if payload.Error.code != "" {
		return grant{}, pollPending, fmt.Errorf("await a ChatGPT login: the authorization server refused the code (%s)", payload.Error.code)
	}
	// A 200 is the shape that carries the grant, and reaching here means only that
	// this build could not read it — half of one, or a body json rejected. Quoting
	// it would paste an authorization code and the verifier minted for it into a
	// panel, a log file and a Wails event, which is exactly what this package
	// promises never to do. The status alone is what is safe to say.
	if status == http.StatusOK {
		return grant{}, pollPending, errors.New("await a ChatGPT login: the authorization server approved the code but answered with a response this build cannot read")
	}
	return grant{}, pollPending, fmt.Errorf("await a ChatGPT login: %s: %s", statusText(status), quote(body))
}

// exchange trades an approved login for tokens. It is the only place the
// authorization code and the code verifier are used, and neither leaves this
// function.
func (c *Client) exchange(ctx context.Context, approved grant) (Tokens, error) {
	return c.token(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {approved.AuthorizationCode},
		"code_verifier": {approved.CodeVerifier},
		"redirect_uri":  {redirectURI},
		"client_id":     {c.clientID},
	}, "complete a ChatGPT login")
}

// Refresh renews an access token. The refresh token it returns REPLACES the one
// passed in — OpenAI rotates it — and a caller that keeps the old one has logged
// the user out at the next renewal.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (Tokens, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return Tokens{}, errors.New("refresh the ChatGPT credential: no refresh token stored; log in again")
	}
	return c.token(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {c.clientID},
	}, "refresh the ChatGPT credential")
}

// token posts the form-encoded token request both grants share and resolves the
// account id out of the returned JWTs.
func (c *Client) token(ctx context.Context, form url.Values, what string) (Tokens, error) {
	status, body, err := c.post(ctx, "/oauth/token", formBody(form))
	if err != nil {
		return Tokens{}, fmt.Errorf("%s: %w", what, err)
	}
	if status != http.StatusOK {
		return Tokens{}, fmt.Errorf("%s: %s: %s", what, statusText(status), quote(body))
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Tokens{}, fmt.Errorf("%s: the authorization server sent a response this build cannot read", what)
	}
	if payload.AccessToken == "" {
		return Tokens{}, fmt.Errorf("%s: the authorization server issued no access token", what)
	}
	if payload.RefreshToken == "" {
		return Tokens{}, fmt.Errorf("%s: the authorization server issued no refresh token, so the credential could not be renewed", what)
	}
	// No account id means no usable credential: every request to the codex
	// backend carries it as a header. Storing one without it would trade this
	// error for an opaque rejection on the first turn.
	accountID := AccountID(payload.AccessToken, payload.IDToken)
	if accountID == "" {
		return Tokens{}, fmt.Errorf("%s: the credential names no ChatGPT account, which a subscription request cannot be routed without", what)
	}
	expiresIn := time.Duration(payload.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = time.Hour
	}
	return Tokens{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		ExpiresAt:    c.now().Add(expiresIn),
		AccountID:    accountID,
	}, nil
}

// AccountID reads the ChatGPT account out of the first JWT that names one.
//
// The signature is not verified, on purpose: we are not the audience of these
// tokens and hold none of the keys that would let us. The claim is not a
// permission we are granting ourselves — it is a routing header the issuer told
// us to send back, and a forged one buys an attacker a rejected request.
//
// Three spellings are accepted because the ecosystem has three: the claim at the
// top level, the same claim nested under OpenAI's namespaced auth claim, and the
// first organization as a last resort.
func AccountID(jwts ...string) string {
	for _, token := range jwts {
		var claims struct {
			ChatGPTAccountID string `json:"chatgpt_account_id"`
			Organizations    []struct {
				ID string `json:"id"`
			} `json:"organizations"`
			Auth struct {
				ChatGPTAccountID string `json:"chatgpt_account_id"`
				Organizations    []struct {
					ID string `json:"id"`
				} `json:"organizations"`
			} `json:"https://api.openai.com/auth"`
		}
		payload, err := jwtPayload(token)
		if err != nil || json.Unmarshal(payload, &claims) != nil {
			continue
		}
		candidates := []string{claims.ChatGPTAccountID, claims.Auth.ChatGPTAccountID}
		if len(claims.Auth.Organizations) > 0 {
			candidates = append(candidates, claims.Auth.Organizations[0].ID)
		}
		if len(claims.Organizations) > 0 {
			candidates = append(candidates, claims.Organizations[0].ID)
		}
		for _, candidate := range candidates {
			if candidate = strings.TrimSpace(candidate); candidate != "" {
				return candidate
			}
		}
	}
	return ""
}

// jwtPayload decodes the claims segment of a JWT. Base64url without padding is
// what the spec mandates, and padded is what some issuers emit anyway.
func jwtPayload(token string) ([]byte, error) {
	segments := strings.Split(token, ".")
	if len(segments) < 2 {
		return nil, errors.New("not a JWT")
	}
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(segments[1], "="))
}

type requestBody struct {
	contentType string
	payload     []byte
}

func jsonBody(value any) requestBody {
	data, _ := json.Marshal(value)
	return requestBody{contentType: "application/json", payload: data}
}

func formBody(form url.Values) requestBody {
	return requestBody{contentType: "application/x-www-form-urlencoded", payload: []byte(form.Encode())}
}

// postJSON posts a JSON body and decodes a 200 response into out. Anything else
// is an error naming the status, because a caller of this cannot act on a body it
// did not get.
func (c *Client) postJSON(ctx context.Context, path string, body any, out any) error {
	status, data, err := c.post(ctx, path, jsonBody(body))
	if err != nil {
		return fmt.Errorf("start a ChatGPT login: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("start a ChatGPT login: %s: %s", statusText(status), quote(data))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return errors.New("start a ChatGPT login: the authorization server sent a response this build cannot read")
	}
	return nil
}

func (c *Client) post(ctx context.Context, path string, body requestBody) (int, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.issuer+path, strings.NewReader(string(body.payload)))
	if err != nil {
		return 0, nil, unreachable(err)
	}
	request.Header.Set("Content-Type", body.contentType)
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return 0, nil, unreachable(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return response.StatusCode, nil, unreachable(err)
	}
	return response.StatusCode, data, nil
}

func statusText(status int) string {
	if text := http.StatusText(status); text != "" {
		return strconv.Itoa(status) + " " + text
	}
	return "HTTP " + strconv.Itoa(status)
}

// quote flattens a failed response into one truncated line. Only non-200 bodies
// reach it, and those carry no credential.
func quote(body []byte) string {
	text := strings.Join(strings.Fields(string(body)), " ")
	if text == "" {
		return "(no details)"
	}
	if runes := []rune(text); len(runes) > maxQuotedBody {
		return string(runes[:maxQuotedBody]) + "…"
	}
	return text
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// flexSeconds is a duration in seconds that arrives as a JSON string ("5") from
// this server and as a number from every other one. Being lenient here is
// cheaper than being right about which it will be next month.
type flexSeconds struct{ seconds float64 }

func (f *flexSeconds) UnmarshalJSON(data []byte) error {
	text := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if text == "" || text == "null" {
		return nil
	}
	seconds, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return nil
	}
	f.seconds = seconds
	return nil
}

func (f flexSeconds) Duration(fallback time.Duration) time.Duration {
	if f.seconds <= 0 {
		return fallback
	}
	return time.Duration(f.seconds * float64(time.Second))
}

// flexTimestamp is an RFC3339 instant that may be absent or unparseable, in
// which case the caller's own cap applies rather than the zero instant leaking
// out as "already expired".
type flexTimestamp struct{ at time.Time }

func (f *flexTimestamp) UnmarshalJSON(data []byte) error {
	var text string
	if json.Unmarshal(data, &text) != nil || strings.TrimSpace(text) == "" {
		return nil
	}
	if at, err := time.Parse(time.RFC3339, text); err == nil {
		f.at = at
	}
	return nil
}

func (f flexTimestamp) Time() time.Time { return f.at }

// errorCode reads the error of a response whichever way it is spelled: a bare
// string, or an object with a code or a message. The distinction the flow turns
// on is which code it is, so both shapes have to reduce to one.
type errorCode struct{ code string }

func (e *errorCode) UnmarshalJSON(data []byte) error {
	var text string
	if json.Unmarshal(data, &text) == nil {
		e.code = strings.TrimSpace(text)
		return nil
	}
	var object struct {
		Code    string `json:"code"`
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &object) != nil {
		return nil
	}
	for _, candidate := range []string{object.Code, object.Type, object.Message} {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			e.code = candidate
			return nil
		}
	}
	return nil
}

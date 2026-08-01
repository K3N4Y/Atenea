// Package posthogauth speaks the OAuth flow that turns a PostHog account into
// a credential atenea can send to PostHog's LLM gateway.
//
// It is the authorization-code flow with PKCE and a loopback redirect, and only
// that: PostHog offers no device-code flow. The redirect lands on a fixed local
// port — the registered redirect URI names it, so it cannot be dynamic — which
// means this login needs a browser on the same machine as the process (or the
// port forwarded over SSH). That limitation is the flow's, not this package's,
// and it is recorded in .okf/specs/2026-07-31-posthog-oauth-provider.md.
//
// The endpoints are PostHog's standard OAuth pair (/oauth/authorize,
// /oauth/token) with two quirks worth naming: the token endpoint takes a JSON
// body rather than the form encoding RFC 6749 prescribes, and the only scope
// the production OAuth apps accept is the wildcard.
//
// Nothing this package returns as an error carries a token, an authorization
// code or a code verifier. Errors from here reach a TUI panel, a Wails event
// and a log file.
package posthogauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultIssuer is the PostHog cloud that mints credentials when a provider
	// declares no issuer of its own. EU users point oauth_issuer at
	// https://eu.posthog.com instead; the client id follows the issuer.
	DefaultIssuer = "https://us.posthog.com"
	// The registered OAuth clients, one per PostHog cloud. They are public
	// identifiers, not secrets: PKCE is what proves the exchange belongs to the
	// process that started it.
	usClientID = "HCWoE0aRFMYxIxFNTTwkOORn5LBjOt2GVDzwSw5W"
	euClientID = "AIvijgMS0dxKEmr5z6odvRd8Pkh5vts3nPTzgzU9"

	// callbackPort is where the browser redirect lands. It is part of the
	// registered redirect URI, so it is a constant of the flow rather than a
	// choice this build gets to make — a login cannot fall back to a free port.
	callbackPort = 8237
	callbackPath = "/callback"
	// redirectURI is the exact string the OAuth clients are registered with. It
	// says localhost while the listener binds 127.0.0.1; both spellings are kept
	// as registered, because the token endpoint compares the string.
	redirectURI = "http://localhost:8237/callback"

	// scope is the literal wildcard, not an explicit list: the production OAuth
	// apps have no seeded scope ceiling, so /oauth/authorize rejects a named
	// privileged scope with invalid_scope while "*" is grandfathered.
	scope = "*"
	// accessLevel asks for a project-scoped grant, which is what the gateway
	// routes on.
	accessLevel = "project"

	// loginTimeout is how long the callback may take to arrive. It bounds a
	// human walking through a browser consent page, not a request.
	loginTimeout = 3 * time.Minute
	// requestTimeout bounds one HTTP call to the token endpoint.
	requestTimeout = 30 * time.Second
	// expirySkew is subtracted from the advertised token lifetime, so a token is
	// renewed while every clock involved still agrees it is valid.
	expirySkew = time.Minute
	// maxQuotedBody caps how much of a failed response is quoted back, so a
	// failure reads as a sentence in a panel instead of pasting a gateway's HTML.
	maxQuotedBody = 256
	// shutdownGrace is how long the callback server gets to flush the page the
	// browser is reading before the listener dies under it.
	shutdownGrace = 2 * time.Second
)

// successPage and errorPage are what the browser shows once the redirect lands.
// The decision between them is made before either is written, so the browser
// never reads "authentication complete" on a callback this package rejected.
const (
	successPage = `<!doctype html><html><head><meta charset="utf-8"><title>PostHog</title></head><body style="font-family:system-ui;text-align:center;padding-top:20vh"><h1>Authentication complete</h1><p>You can close this window and return to atenea.</p></body></html>`
	errorPage   = `<!doctype html><html><head><meta charset="utf-8"><title>PostHog</title></head><body style="font-family:system-ui;text-align:center;padding-top:20vh"><h1>Authentication failed</h1><p>Please return to atenea and try again.</p></body></html>`
)

// Tokens is one issued credential set: the bearer the gateway accepts, the
// refresh token that renews it, and when it stops being accepted.
type Tokens struct {
	AccessToken string
	// RefreshToken ROTATES on every refresh. A caller that does not persist the
	// new one logs the user out at the following renewal.
	RefreshToken string
	ExpiresAt    time.Time
}

// Login is one pending browser login: the page to send the user to and the
// wait that turns their consent into a credential.
type Login struct {
	// AuthorizeURL is where the user consents. It carries the code challenge and
	// the state, neither of which is a secret.
	AuthorizeURL string
	// ExpiresAt is when waiting gives up, so a host can say how long is left.
	ExpiresAt time.Time
	// Await blocks until the browser redirect lands, ctx is cancelled, or the
	// login times out — whichever comes first. It owns the callback listener and
	// releases it on every path.
	Await func(ctx context.Context) (Tokens, error)
}

// ListenFunc binds the loopback listener the redirect lands on. It is a seam so
// tests can bind port 0 instead of colliding on the registered port.
type ListenFunc func() (net.Listener, error)

// Client talks to one issuer as the OAuth client registered for it.
type Client struct {
	issuer string
	http   *http.Client
	now    func() time.Time
	listen ListenFunc
}

// Option adjusts a Client at construction, keeping the constructor stable.
type Option func(*Client)

// WithIssuer points the client at another PostHog cloud. Empty keeps the US
// default; the client id is derived from whatever host the issuer names.
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

// WithListener replaces how the callback listener is bound.
func WithListener(listen ListenFunc) Option {
	return func(c *Client) {
		if listen != nil {
			c.listen = listen
		}
	}
}

func NewClient(opts ...Option) *Client {
	c := &Client{
		issuer: DefaultIssuer,
		http:   &http.Client{Timeout: requestTimeout},
		now:    time.Now,
		listen: listenCallback,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// clientID is the registered client for this issuer. The id follows the issuer
// host, which is what lets an EU login be one config edit (the issuer) rather
// than two facts the user has to keep consistent.
func (c *Client) clientID() string {
	if parsed, err := url.Parse(c.issuer); err == nil && strings.EqualFold(parsed.Hostname(), "eu.posthog.com") {
		return euClientID
	}
	return usClientID
}

func listenCallback() (net.Listener, error) {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", callbackPort))
	if err != nil {
		return nil, fmt.Errorf("start a PostHog login: port %d is already in use or cannot be bound (another login in progress?): %w", callbackPort, err)
	}
	return listener, nil
}

// callbackResult is what one browser redirect resolved to: a code, or the
// reason there is none.
type callbackResult struct {
	code string
	err  error
}

// BeginLogin binds the callback listener, builds the authorize URL, and returns
// the pending login.
//
// The listener is bound before the URL exists, on purpose: a URL handed to a
// browser with nothing listening behind it is a consent the user grants into
// the void. A busy port fails here, legibly, before anything was shown.
//
// It is deliberately separate from the waiting: a host has to paint the URL
// before anything blocks, and the two have different lifetimes — starting is
// bounded by the click that triggered it, waiting by a human in a browser.
func (c *Client) BeginLogin(ctx context.Context) (Login, error) {
	if err := ctx.Err(); err != nil {
		return Login{}, err
	}
	verifier, err := randomToken(32)
	if err != nil {
		return Login{}, fmt.Errorf("start a PostHog login: %w", err)
	}
	state, err := randomToken(16)
	if err != nil {
		return Login{}, fmt.Errorf("start a PostHog login: %w", err)
	}
	listener, err := c.listen()
	if err != nil {
		return Login{}, err
	}

	results := make(chan callbackResult, 1)
	server := &http.Server{Handler: callbackHandler(state, results)}
	go func() { _ = server.Serve(listener) }()

	expiresAt := c.now().Add(loginTimeout)
	return Login{
		AuthorizeURL: c.authorizeURL(challengeS256(verifier), state),
		ExpiresAt:    expiresAt,
		Await: func(ctx context.Context) (Tokens, error) {
			return c.await(ctx, server, results, verifier, expiresAt)
		},
	}, nil
}

// authorizeURL is the consent page for this login. Everything on it is public:
// the challenge commits to the verifier without revealing it, and the state is
// only meaningful to the listener that minted it.
func (c *Client) authorizeURL(challenge, state string) string {
	query := url.Values{
		"client_id":             {c.clientID()},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"scope":                 {scope},
		"required_access_level": {accessLevel},
		"state":                 {state},
	}
	return c.issuer + "/oauth/authorize?" + query.Encode()
}

// callbackHandler answers the browser redirect. Success or failure is decided
// BEFORE the response is written — an OAuth error, a missing code, a state
// mismatch each get the error page — so the browser never says the login
// completed on a path this package is about to reject. Only the first result
// counts; the channel is buffered and later redirects change nothing.
func callbackHandler(expectedState string, results chan<- callbackResult) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != callbackPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		query := r.URL.Query()
		result := callbackResult{code: query.Get("code")}
		switch {
		case query.Get("error") != "":
			// The error parameter is one of RFC 6749's registered codes
			// (access_denied, ...), which is safe to quote and the only thing the
			// user can act on.
			result = callbackResult{err: fmt.Errorf("the PostHog login was refused (%s)", query.Get("error"))}
		case result.code == "":
			result = callbackResult{err: errors.New("the PostHog login callback carried no code; start the login again")}
		case query.Get("state") != expectedState:
			result = callbackResult{err: errors.New("the PostHog login callback did not match this login attempt; start the login again")}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if result.err != nil {
			_, _ = io.WriteString(w, errorPage)
		} else {
			_, _ = io.WriteString(w, successPage)
		}
		select {
		case results <- result:
		default:
		}
	})
}

// await waits for the redirect and trades its code for tokens. The listener is
// released on every path — success, refusal, cancellation, timeout.
func (c *Client) await(ctx context.Context, server *http.Server, results <-chan callbackResult, verifier string, expiresAt time.Time) (Tokens, error) {
	defer func() {
		// Shutdown, not Close: the success page is being flushed to the browser at
		// exactly this moment, and cutting the connection under it shows the user
		// a browser error about a login that worked.
		grace, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
		defer cancel()
		_ = server.Shutdown(grace)
	}()

	waitCtx, cancel := context.WithDeadline(ctx, expiresAt)
	defer cancel()
	select {
	case result := <-results:
		if result.err != nil {
			return Tokens{}, result.err
		}
		// The exchange runs on the caller's ctx, not the deadline-wrapped one: the
		// timeout bounds the human, and a consent granted at the last second
		// deserves its 30-second request.
		return c.exchange(ctx, result.code, verifier)
	case <-waitCtx.Done():
		return Tokens{}, awaitInterrupted(waitCtx)
	}
}

// awaitInterrupted names the two ways waiting ends without a redirect, so the
// user reads why instead of reading "context deadline exceeded".
func awaitInterrupted(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errors.New("the PostHog login timed out before it was approved; start the login again")
	}
	return ctx.Err()
}

// exchange trades an authorization code for tokens. It is the only place the
// code and the verifier are used, and neither leaves this function.
func (c *Client) exchange(ctx context.Context, code, verifier string) (Tokens, error) {
	return c.token(ctx, map[string]string{
		"grant_type":    "authorization_code",
		"code":          code,
		"redirect_uri":  redirectURI,
		"client_id":     c.clientID(),
		"code_verifier": verifier,
	}, "complete a PostHog login")
}

// Refresh renews an access token. The refresh token it returns REPLACES the one
// passed in — PostHog rotates it — and a caller that keeps the old one has
// logged the user out at the next renewal.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (Tokens, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return Tokens{}, errors.New("refresh the PostHog credential: no refresh token stored; log in again")
	}
	return c.token(ctx, map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     c.clientID(),
	}, "refresh the PostHog credential")
}

// token posts the JSON-encoded token request both grants share. JSON, not the
// form encoding RFC 6749 prescribes: PostHog's token endpoint reads the body as
// JSON, and the form spelling is answered with a 400 about missing fields.
func (c *Client) token(ctx context.Context, body map[string]string, what string) (Tokens, error) {
	payload, _ := json.Marshal(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.issuer+"/oauth/token", strings.NewReader(string(payload)))
	if err != nil {
		return Tokens{}, fmt.Errorf("%s: %w", what, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return Tokens{}, fmt.Errorf("%s: the authorization server could not be reached; check the connection: %w", what, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return Tokens{}, fmt.Errorf("%s: the authorization server could not be reached; check the connection: %w", what, err)
	}
	if response.StatusCode != http.StatusOK {
		return Tokens{}, fmt.Errorf("%s: %s: %s", what, statusText(response.StatusCode), quote(data))
	}
	var parsed struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Tokens{}, fmt.Errorf("%s: the authorization server sent a response this build cannot read", what)
	}
	if parsed.AccessToken == "" {
		return Tokens{}, fmt.Errorf("%s: the authorization server issued no access token", what)
	}
	if parsed.RefreshToken == "" {
		return Tokens{}, fmt.Errorf("%s: the authorization server issued no refresh token, so the credential could not be renewed", what)
	}
	expiresIn := time.Duration(parsed.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = time.Hour
	}
	return Tokens{
		AccessToken:  parsed.AccessToken,
		RefreshToken: parsed.RefreshToken,
		ExpiresAt:    c.now().Add(expiresIn - expirySkew),
	}, nil
}

// randomToken is base64url of n random bytes: the spelling PKCE wants for the
// verifier, and a fine one for the state.
func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", errors.New("no randomness available to protect the login")
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// challengeS256 commits to the verifier the way RFC 7636 spells it.
func challengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
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

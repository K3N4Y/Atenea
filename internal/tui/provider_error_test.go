package tui

import (
	"strings"
	"testing"
)

// TestSanitizeProviderDetails_RedactsEverySecretAProviderCanEcho: the details
// pane is the last stop before a secret is drawn on a screen that gets scrolled
// back, screenshotted and pasted into an issue. A provider error can quote the
// request that caused it, so every shape a credential arrives in has to be caught
// here — including the OAuth ones, whose tokens are JWTs with no `sk-` prefix and
// no api_key field name around them.
func TestSanitizeProviderDetails_RedactsEverySecretAProviderCanEcho(t *testing.T) {
	// A JWT-shaped token, which is what a ChatGPT subscription's access and id
	// tokens are.
	const jwt = "eyJhbGciOiJub25lIn0.eyJjaGF0Z3B0X2FjY291bnRfaWQiOiJhY2N0XzEifQ.sig"
	cases := map[string]string{
		"api key in a query string":  `POST "https://gw.test/v1?api_key=super-secret": 401`,
		"bearer in a header dump":    `authorization: Bearer super-secret`,
		"an OpenAI key on its own":   `rejected key sk-proj-super-secret-value`,
		"an access token in a body":  `{"access_token":"super-secret","expires_in":3600}`,
		"a refresh token in a body":  `{"refresh_token": "super-secret"}`,
		"an id token in a body":      `{"id_token":"super-secret"}`,
		"a bare JWT with no field":   "Bearer token rejected: " + jwt,
		"a JWT inside a JSON string": `{"detail":"token ` + jwt + ` is expired"}`,
		// Both halves of a device-code grant. Together they are a login an attacker
		// can complete, and the word boundary in the `authorization` pattern is
		// exactly what would let the first one through.
		"an authorization code in a body": `{"authorization_code":"super-secret","status":"ok"}`,
		"a code verifier in a body":       `{"code_verifier": "super-secret"}`,
		"a verifier in a query string":    `POST "https://auth.test/oauth/token?code-verifier=super-secret": 400`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			got := sanitizeProviderDetails(raw)
			if strings.Contains(got, "super-secret") {
				t.Fatalf("sanitizeProviderDetails(%q) = %q, want the secret redacted", raw, got)
			}
			if strings.Contains(got, jwt) {
				t.Fatalf("sanitizeProviderDetails(%q) = %q, want the JWT redacted", raw, got)
			}
			if !strings.Contains(got, "[redacted]") {
				t.Fatalf("sanitizeProviderDetails(%q) = %q, want a visible redaction marker", raw, got)
			}
		})
	}
}

// TestSanitizeProviderDetails_KeepsWhatMakesTheErrorUseful: redaction that eats the
// reason turns a debuggable failure into a blank line, which is why the patterns
// are anchored on credential shapes rather than on anything that looks opaque.
func TestSanitizeProviderDetails_KeepsWhatMakesTheErrorUseful(t *testing.T) {
	raw := `POST "https://chatgpt.com/backend-api/codex/responses": 429 {"error":{"message":"You've hit your usage limit."}}`
	got := sanitizeProviderDetails(raw)
	for _, want := range []string{"429", "usage limit", "backend-api/codex/responses"} {
		if !strings.Contains(got, want) {
			t.Fatalf("sanitizeProviderDetails(%q) = %q, want it to keep %q", raw, got, want)
		}
	}
}

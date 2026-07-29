package providerconfig

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	// DefaultExecTimeout bounds one run of an exec credential's command. It is
	// generous because the commands this exists for talk to the network on a cold
	// token cache, and an ExecCredential can shorten it.
	DefaultExecTimeout = 30 * time.Second
	// DefaultExecTokenTTL is how long a token read from a command is reused for
	// model listing. Short enough that a rotated or revoked token is not served
	// for long, long enough that opening the model picker twice does not run
	// everyone's `gcloud` twice.
	DefaultExecTokenTTL = 5 * time.Minute
	// maxStderrQuoted caps how much of a failed command's standard error is
	// quoted back. Enough to see the reason, not enough to paste a whole stack
	// trace into a TUI toast.
	maxStderrQuoted = 512
	// killGrace is how long a timed-out command's output pipes may stay open
	// after it is killed, so a grandchild holding them cannot make resolution
	// outlive its own deadline.
	killGrace = time.Second
)

// CommandRunner runs an exec credential's command and returns its standard
// output. It is the seam a test replaces so that wiring can be exercised
// without spawning a process; the shipped one is [runCommand].
type CommandRunner func(ctx context.Context, command []string) ([]byte, error)

// CredentialResolver turns a stored credential into the bearer token an adapter
// authenticates with, dispatching on the credential's declared type: an api_key
// resolves to itself, an exec credential runs its command.
//
// It is deliberately not the [CredentialStore]. A store persists, and persisting
// must not mean executing — the split is what lets a keyring backend exist
// without knowing what a subprocess is, and what keeps the code that runs
// commands in one place with its timeout, its guardrails and its cache.
type CredentialResolver struct {
	store CredentialStore
	run   CommandRunner
	now   func() time.Time
	// oauthMargin is how long before expiry an OAuth credential is renewed. Zero
	// means [DefaultOAuthRefreshMargin]; a test shortens it to make the renewal
	// happen on demand.
	oauthMargin time.Duration

	mu     sync.Mutex
	tokens map[string]cachedToken
	// gates serialize the OAuth renewals of one provider, so several turns
	// noticing the same expiry produce one refresh and one rotation rather than
	// one each. See [CredentialResolver.oauthToken].
	gates map[string]*sync.Mutex
}

// cachedToken is one reusable exec result. fingerprint is the command it came
// from, so editing credentials.json invalidates the entry instead of serving a
// token the user just replaced.
type cachedToken struct {
	token       string
	fingerprint string
	expiresAt   time.Time
}

// NewCredentialResolver resolves credentials out of store. A nil store is
// legitimate — a host may have no credential storage at all — and resolves
// everything to "no credential".
func NewCredentialResolver(store CredentialStore) *CredentialResolver {
	return &CredentialResolver{store: store, run: runCommand, now: time.Now, tokens: map[string]cachedToken{}, gates: map[string]*sync.Mutex{}}
}

// Token resolves the credential stored for providerID, running an exec
// credential's command every time. It is what a *selection* uses: the string it
// returns is baked into the adapter that will carry a whole conversation, so it
// must be as fresh as this process can make it.
//
// Absence is not an error — no store, or no entry, is ("", nil), which is how a
// keyless endpoint and an unconnected provider both read. So is an OAuth login,
// which has no static token at all: see the arm in resolve. A credential that is
// stored and cannot be honored is an error, because the alternative is an
// unauthenticated request failing later with a worse message.
//
// What it resolves also refreshes what [CredentialResolver.CachedToken] serves,
// so the catalog refresh that follows a selection reuses this run.
func (r *CredentialResolver) Token(ctx context.Context, providerID string) (string, error) {
	credential, ok := r.stored(providerID)
	if !ok {
		return "", nil
	}
	token, err := r.resolve(ctx, providerID, credential)
	if err != nil {
		return "", err
	}
	r.remember(providerID, credential, token)
	return token, nil
}

// CachedToken is [Token] with reuse: a token read from a command is served again
// until its TTL expires. Model listing walks every configured provider, so
// without this one catalog refresh would spawn one subprocess per
// exec-credentialed provider — and the picker refreshes every time it opens.
//
// A cached token is good enough to list models; it is not good enough to build
// the adapter a conversation runs on. That asymmetry is the whole reason there
// are two entry points instead of one.
func (r *CredentialResolver) CachedToken(ctx context.Context, providerID string) (string, error) {
	credential, ok := r.stored(providerID)
	if !ok {
		return "", nil
	}
	if token, ok := r.cached(providerID, credential); ok {
		return token, nil
	}
	token, err := r.resolve(ctx, providerID, credential)
	if err != nil {
		return "", err
	}
	r.remember(providerID, credential, token)
	return token, nil
}

func (r *CredentialResolver) stored(providerID string) (Credential, bool) {
	if r == nil || r.store == nil {
		return Credential{}, false
	}
	return r.store.Get(providerID)
}

func (r *CredentialResolver) resolve(ctx context.Context, providerID string, credential Credential) (string, error) {
	if err := credential.Validate(); err != nil {
		return "", fmt.Errorf("credential for provider %q: %w", providerID, err)
	}
	switch credential.Type {
	case CredentialTypeAPIKey:
		return credential.APIKey, nil
	case CredentialTypeExec:
		return r.runExec(ctx, providerID, credential.Exec)
	case CredentialTypeOAuth:
		// An OAuth login has no static token to hand out. Its bearer expires within
		// the hour and travels with an account id, so it is resolved per request
		// through [CredentialResolver.OAuthTokenSource] instead — and answering
		// "nothing static" here is what makes a provider authenticated that way
		// build with the keyless placeholder rather than with a secret it ignores.
		return "", nil
	default:
		// Unreachable while Validate and this switch agree; a guard rather than
		// dead code, because the failure it prevents is a new arm being run as if
		// it were the previous one.
		return "", fmt.Errorf("credential for provider %q: type %q has no resolution", providerID, credential.Type)
	}
}

// runExec reads a token from a command's standard output. Nothing it returns
// contains the token: an error from here is shown in a UI and written to logs.
func (r *CredentialResolver) runExec(ctx context.Context, providerID string, def *ExecCredential) (string, error) {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("credential for provider %q: "+format, append([]any{providerID}, args...)...)
	}
	if err := r.checkOrigin(); err != nil {
		return "", fail("%w", err)
	}
	timeout := execTimeout(def)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, err := r.run(ctx, append([]string(nil), def.Command...))
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fail("%q did not produce a token within %s", def.Command[0], timeout)
		}
		return "", fail("%q failed: %w", def.Command[0], err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", fail("%q printed no token", def.Command[0])
	}
	// A bearer token is one opaque word. Anything else is a command that also
	// printed a banner or a warning, and splicing that into an Authorization
	// header produces an unreadable transport error instead of this one.
	if strings.ContainsFunc(token, func(c rune) bool { return unicode.IsSpace(c) || unicode.IsControl(c) }) {
		return "", fail("%q printed more than a bare token", def.Command[0])
	}
	return token, nil
}

// checkOrigin refuses to run an exec credential that came from a file anyone but
// its owner can write, and from a directory anyone but its owner can write —
// which is the same hole, since a writable directory means the file can be
// replaced wholesale. An exec credential turns the credentials file into code
// this user runs; group-writable is a local privilege escalation waiting for a
// shared machine.
//
// Windows is exempt: Go reports a synthetic mode there, so every file would look
// world-writable and the check would refuse everything. A store that is not
// file-backed has nothing to check.
func (r *CredentialResolver) checkOrigin() error {
	file, ok := r.store.(CredentialFile)
	if !ok || runtime.GOOS == "windows" {
		return nil
	}
	path := file.CredentialPath()
	for _, target := range []string{path, filepath.Dir(path)} {
		info, err := os.Stat(target)
		if err != nil {
			return fmt.Errorf("refusing to run an exec credential: cannot check the permissions of %s: %w", target, err)
		}
		if mode := info.Mode().Perm(); mode&0o022 != 0 {
			return fmt.Errorf("refusing to run an exec credential: %s is writable by group or others (%#o); run chmod go-w on it", target, mode)
		}
	}
	return nil
}

func (r *CredentialResolver) cached(providerID string, credential Credential) (string, bool) {
	if credential.Type != CredentialTypeExec {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.tokens[providerID]
	if !ok || entry.fingerprint != execFingerprint(credential.Exec) || !r.now().Before(entry.expiresAt) {
		return "", false
	}
	return entry.token, true
}

// remember caches an exec result. An api_key credential is not cached: reading
// it is a file read the store already does on every call, on purpose, so that
// two processes observe each other's writes.
func (r *CredentialResolver) remember(providerID string, credential Credential, token string) {
	if credential.Type != CredentialTypeExec {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens[providerID] = cachedToken{
		token:       token,
		fingerprint: execFingerprint(credential.Exec),
		expiresAt:   r.now().Add(execTTL(credential.Exec)),
	}
}

func execFingerprint(def *ExecCredential) string {
	return strings.Join(def.Command, "\x00")
}

func execTimeout(def *ExecCredential) time.Duration {
	if def.TimeoutSeconds > 0 {
		return time.Duration(def.TimeoutSeconds) * time.Second
	}
	return DefaultExecTimeout
}

func execTTL(def *ExecCredential) time.Duration {
	if def.TTLSeconds > 0 {
		return time.Duration(def.TTLSeconds) * time.Second
	}
	return DefaultExecTokenTTL
}

// runCommand executes an argv list and returns its standard output.
//
// The environment is inherited, because the tools this exists for are useless
// without it: `gcloud` needs HOME and PATH, `aws` needs its profile variables.
// Standard input is not: a command that decides to prompt must fail on EOF
// rather than hang until the deadline.
//
// A failure quotes standard error and never standard output, which is where the
// token is.
func runCommand(ctx context.Context, command []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Without this, killing the process still leaves Wait blocked on output pipes
	// a surviving grandchild holds open, and the timeout stops bounding anything.
	cmd.WaitDelay = killGrace
	if err := cmd.Run(); err != nil {
		if detail := quoteStderr(stderr.Bytes()); detail != "" {
			return nil, fmt.Errorf("%w: %s", err, detail)
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

// quoteStderr flattens a command's standard error into one truncated line, so a
// resolution failure reads as a sentence wherever errors are shown.
func quoteStderr(stderr []byte) string {
	text := strings.Join(strings.Fields(string(stderr)), " ")
	if runes := []rune(text); len(runes) > maxStderrQuoted {
		return string(runes[:maxStderrQuoted]) + "…"
	}
	return text
}

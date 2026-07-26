package providerconfig

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// helperEnv names the behavior TestCredentialHelperProcess should act out. The
// exec tests re-execute the test binary the way os/exec's own tests do, so the
// happy path, a failure and a timeout are measured against a real process rather
// than a stub — and passing the behavior through the environment doubles as
// proof that a command inherits it, which is what makes `gcloud` and `aws`
// usable at all.
const helperEnv = "ATENEA_CREDENTIAL_HELPER"

// TestCredentialHelperProcess is not a test. It is the program the exec tests
// run; without the environment variable it does nothing.
func TestCredentialHelperProcess(t *testing.T) {
	switch os.Getenv(helperEnv) {
	case "":
		return
	case "token":
		os.Stdout.WriteString("exec-token\n")
	case "noisy":
		os.Stdout.WriteString("exec-token\nWarning: using application default credentials\n")
	case "empty":
	case "fail":
		os.Stderr.WriteString("helper: no active account; run `helper login`\n")
		os.Exit(3)
	case "slow":
		time.Sleep(10 * time.Second)
		os.Stdout.WriteString("late-token\n")
	}
	os.Exit(0)
}

func helperCommand(t *testing.T, behavior string) []string {
	t.Helper()
	t.Setenv(helperEnv, behavior)
	return []string{os.Args[0], "-test.run=TestCredentialHelperProcess"}
}

// memoryCredentials is a store with no file behind it, which is also what makes
// it the case that exercises CredentialFile's absence: there is nothing whose
// permissions could be checked.
type memoryCredentials map[string]Credential

func (m memoryCredentials) Get(providerID string) (Credential, bool) {
	credential, ok := m[providerID]
	return credential, ok
}

func (m memoryCredentials) Put(providerID string, credential Credential) error {
	if err := credential.Validate(); err != nil {
		return err
	}
	m[providerID] = credential
	return nil
}

func execCredential(command []string) Credential {
	return Credential{Type: CredentialTypeExec, Exec: &ExecCredential{Command: command}}
}

func TestCredentialResolver_APIKeyCredentialResolvesToItself(t *testing.T) {
	resolver := NewCredentialResolver(memoryCredentials{"p": {Type: CredentialTypeAPIKey, APIKey: "sk-stored"}})
	for _, resolve := range map[string]func(context.Context, string) (string, error){
		"Token":       resolver.Token,
		"CachedToken": resolver.CachedToken,
	} {
		token, err := resolve(context.Background(), "p")
		if err != nil || token != "sk-stored" {
			t.Fatalf("token = %q err = %v, want the stored key unchanged", token, err)
		}
	}
}

func TestCredentialResolver_AbsentCredentialIsNotAnError(t *testing.T) {
	for name, resolver := range map[string]*CredentialResolver{
		"no store": NewCredentialResolver(nil),
		"no entry": NewCredentialResolver(memoryCredentials{}),
	} {
		token, err := resolver.Token(context.Background(), "p")
		if err != nil || token != "" {
			t.Fatalf("%s: token = %q err = %v, want absence to read as no credential", name, token, err)
		}
	}
}

func TestCredentialResolver_ExecCredentialReadsTheTokenFromStdout(t *testing.T) {
	command := helperCommand(t, "token")
	store := NewFileCredentialStore(filepath.Join(t.TempDir(), "credentials.json"))
	if err := store.Put("p", execCredential(command)); err != nil {
		t.Fatal(err)
	}
	token, err := NewCredentialResolver(store).Token(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	if token != "exec-token" {
		t.Fatalf("token = %q, want the command's trimmed stdout", token)
	}
}

func TestCredentialResolver_FailingCommandSurfacesStderrWithoutLeakingStdout(t *testing.T) {
	resolver := NewCredentialResolver(memoryCredentials{"p": execCredential(helperCommand(t, "fail"))})
	_, err := resolver.Token(context.Background(), "p")
	if err == nil {
		t.Fatal("a command that exits non-zero must fail resolution")
	}
	if !strings.Contains(err.Error(), "no active account") {
		t.Fatalf("error = %v, want the command's stderr in it", err)
	}
	if strings.Contains(err.Error(), "exec-token") {
		t.Fatalf("error = %v, must never carry the token", err)
	}
}

func TestCredentialResolver_EmptyStdoutIsRefused(t *testing.T) {
	resolver := NewCredentialResolver(memoryCredentials{"p": execCredential(helperCommand(t, "empty"))})
	if _, err := resolver.Token(context.Background(), "p"); err == nil || !strings.Contains(err.Error(), "no token") {
		t.Fatalf("err = %v, want a command that prints nothing to be refused", err)
	}
}

func TestCredentialResolver_StdoutWithMoreThanATokenIsRefused(t *testing.T) {
	resolver := NewCredentialResolver(memoryCredentials{"p": execCredential(helperCommand(t, "noisy"))})
	if _, err := resolver.Token(context.Background(), "p"); err == nil || !strings.Contains(err.Error(), "more than a bare token") {
		t.Fatalf("err = %v, want a banner alongside the token to be refused", err)
	}
}

// TestCredentialResolver_TimeoutKillsTheCommand pins the guardrail, not the
// message: the helper sleeps far longer than the deadline, so a resolution that
// returned only after it exited would take ten seconds instead of one.
func TestCredentialResolver_TimeoutKillsTheCommand(t *testing.T) {
	credential := execCredential(helperCommand(t, "slow"))
	credential.Exec.TimeoutSeconds = 1
	resolver := NewCredentialResolver(memoryCredentials{"p": credential})

	start := time.Now()
	_, err := resolver.Token(context.Background(), "p")
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "did not produce a token within 1s") {
		t.Fatalf("err = %v, want the deadline reported", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("resolution took %s, want the command killed at its deadline", elapsed)
	}
}

func TestCredentialResolver_UnknownTypeIsRefusedByName(t *testing.T) {
	resolver := NewCredentialResolver(memoryCredentials{
		"unknown": {Type: "oauth"},
		"untyped": {APIKey: "sk-stored"},
	})
	_, err := resolver.Token(context.Background(), "unknown")
	if err == nil || !strings.Contains(err.Error(), `unknown credential type "oauth"`) || !strings.Contains(err.Error(), "api_key, exec") {
		t.Fatalf("err = %v, want the unknown type named alongside the known ones", err)
	}
	// A credential that declares nothing is its own answer: it is not an api_key
	// credential that forgot to say so, and resolving it as one is how a bag of
	// optional fields gets reintroduced.
	_, err = resolver.Token(context.Background(), "untyped")
	if err == nil || !strings.Contains(err.Error(), "no type") {
		t.Fatalf("err = %v, want a credential with no type refused rather than guessed", err)
	}
}

func TestCredentialResolver_RefusesAnExecCredentialFromAGroupWritableFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root writes anywhere; the check is about other users")
	}
	path := filepath.Join(t.TempDir(), "credentials.json")
	store := NewFileCredentialStore(path)
	if err := store.Put("p", execCredential(helperCommand(t, "token"))); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}
	resolver := NewCredentialResolver(store)
	_, err := resolver.Token(context.Background(), "p")
	if err == nil || !strings.Contains(err.Error(), "writable by group or others") {
		t.Fatalf("err = %v, want a loosely permissioned file refused", err)
	}

	// The api_key arm is untouched: reading a string out of a file nobody should
	// have widened is a confidentiality problem, not a code-execution one, and
	// locking a user out of their own key would be a worse trade.
	if err := store.Put("k", Credential{Type: CredentialTypeAPIKey, APIKey: "sk-stored"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}
	if token, err := resolver.Token(context.Background(), "k"); err != nil || token != "sk-stored" {
		t.Fatalf("token = %q err = %v, want the api_key arm unaffected", token, err)
	}
}

// countingResolver is the wiring-level fixture: a resolver whose command never
// runs, so a test can count resolutions and move the clock without owning a
// process.
func countingResolver(t *testing.T, credentials memoryCredentials) (*CredentialResolver, *int, *time.Time) {
	t.Helper()
	runs := 0
	clock := time.Now()
	resolver := NewCredentialResolver(credentials)
	resolver.run = func(context.Context, []string) ([]byte, error) {
		runs++
		return []byte("exec-token\n"), nil
	}
	resolver.now = func() time.Time { return clock }
	return resolver, &runs, &clock
}

func TestCredentialResolver_CachedTokenRunsTheCommandOncePerTTL(t *testing.T) {
	credential := execCredential([]string{"print-token"})
	credential.Exec.TTLSeconds = 60
	resolver, runs, clock := countingResolver(t, memoryCredentials{"p": credential})

	for i := 0; i < 3; i++ {
		if token, err := resolver.CachedToken(context.Background(), "p"); err != nil || token != "exec-token" {
			t.Fatalf("token = %q err = %v", token, err)
		}
	}
	if *runs != 1 {
		t.Fatalf("runs = %d, want one command for three listings inside the TTL", *runs)
	}

	*clock = clock.Add(61 * time.Second)
	if _, err := resolver.CachedToken(context.Background(), "p"); err != nil {
		t.Fatal(err)
	}
	if *runs != 2 {
		t.Fatalf("runs = %d, want the command to run again once the TTL expired", *runs)
	}
}

// TestCredentialResolver_TokenIgnoresTheCache is the other half of the split:
// a token that will be baked into an adapter for a whole conversation is
// resolved fresh, which is what makes re-selecting a model recover from an
// expired token.
func TestCredentialResolver_TokenIgnoresTheCache(t *testing.T) {
	resolver, runs, _ := countingResolver(t, memoryCredentials{"p": execCredential([]string{"print-token"})})
	for i := 0; i < 3; i++ {
		if _, err := resolver.Token(context.Background(), "p"); err != nil {
			t.Fatal(err)
		}
	}
	if *runs != 3 {
		t.Fatalf("runs = %d, want every selection to resolve fresh", *runs)
	}
}

func TestCredentialResolver_CachedTokenReresolvesWhenTheCommandChanges(t *testing.T) {
	credentials := memoryCredentials{"p": execCredential([]string{"print-token"})}
	resolver, runs, _ := countingResolver(t, credentials)
	if _, err := resolver.CachedToken(context.Background(), "p"); err != nil {
		t.Fatal(err)
	}
	credentials["p"] = execCredential([]string{"print-token", "--other-account"})
	if _, err := resolver.CachedToken(context.Background(), "p"); err != nil {
		t.Fatal(err)
	}
	if *runs != 2 {
		t.Fatalf("runs = %d, want an edited command to invalidate the cached token", *runs)
	}
}

func TestCredentialResolver_ConcurrentResolutionsAreSafe(t *testing.T) {
	resolver := NewCredentialResolver(memoryCredentials{"p": execCredential([]string{"print-token"})})
	var mu sync.Mutex
	runs := 0
	resolver.run = func(context.Context, []string) ([]byte, error) {
		mu.Lock()
		runs++
		mu.Unlock()
		return []byte("exec-token\n"), nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resolve := resolver.CachedToken
			if i%2 == 0 {
				resolve = resolver.Token
			}
			if token, err := resolve(context.Background(), "p"); err != nil || token != "exec-token" {
				t.Errorf("token = %q err = %v", token, err)
			}
		}(i)
	}
	wg.Wait()
}

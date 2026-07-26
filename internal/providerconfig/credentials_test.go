package providerconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCredential_ValidateRefusesABagOfArms is the property that keeps Credential
// a tagged variant: exactly the arm Type names is populated. Without it the type
// degenerates into optional fields and every reader has to guess which one the
// writer meant.
func TestCredential_ValidateRefusesABagOfArms(t *testing.T) {
	for name, credential := range map[string]Credential{
		"api_key with a command":    {Type: CredentialTypeAPIKey, APIKey: "sk", Exec: &ExecCredential{Command: []string{"print-token"}}},
		"api_key with no key":       {Type: CredentialTypeAPIKey},
		"exec with an api_key":      {Type: CredentialTypeExec, APIKey: "sk", Exec: &ExecCredential{Command: []string{"print-token"}}},
		"exec with no command":      {Type: CredentialTypeExec, Exec: &ExecCredential{}},
		"exec with no arm":          {Type: CredentialTypeExec},
		"exec with a blank program": {Type: CredentialTypeExec, Exec: &ExecCredential{Command: []string{"  "}}},
		"exec with a negative timeout": {Type: CredentialTypeExec, Exec: &ExecCredential{
			Command: []string{"print-token"}, TimeoutSeconds: -1,
		}},
	} {
		if err := credential.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want a refusal", name)
		}
	}
}

func TestCredential_ValidateAcceptsEachArmOnItsOwn(t *testing.T) {
	for name, credential := range map[string]Credential{
		"api_key": {Type: CredentialTypeAPIKey, APIKey: "sk"},
		"exec":    {Type: CredentialTypeExec, Exec: &ExecCredential{Command: []string{"print-token", "--quiet"}}},
		"exec with declared bounds": {Type: CredentialTypeExec, Exec: &ExecCredential{
			Command: []string{"print-token"}, TimeoutSeconds: 5, TTLSeconds: 300,
		}},
	} {
		if err := credential.Validate(); err != nil {
			t.Errorf("%s: Validate() = %v, want nil", name, err)
		}
	}
}

func TestFileCredentialStore_PutRefusesAMalformedCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	store := NewFileCredentialStore(path)
	err := store.Put("p", Credential{Type: CredentialTypeExec, APIKey: "sk"})
	if err == nil || !strings.Contains(err.Error(), `provider "p"`) {
		t.Fatalf("Put() = %v, want a refusal naming the provider", err)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Fatal("a rejected credential must not create the file")
	}
}

func TestFileCredentialStore_ExecCredentialRoundTrips(t *testing.T) {
	store := NewFileCredentialStore(filepath.Join(t.TempDir(), "credentials.json"))
	stored := Credential{Type: CredentialTypeExec, Exec: &ExecCredential{
		Command: []string{"gcloud", "auth", "print-access-token"}, TTLSeconds: 1800,
	}}
	if err := store.Put("vertex", stored); err != nil {
		t.Fatal(err)
	}
	credential, ok := store.Get("vertex")
	if !ok || credential.Type != CredentialTypeExec || credential.Exec == nil {
		t.Fatalf("credential = %#v, ok = %v", credential, ok)
	}
	if strings.Join(credential.Exec.Command, " ") != "gcloud auth print-access-token" || credential.Exec.TTLSeconds != 1800 {
		t.Fatalf("exec arm = %#v, want the command and TTL round-tripped", credential.Exec)
	}
}

func TestFileCredentialStore_MissingFileMeansNotConnected(t *testing.T) {
	store := NewFileCredentialStore(filepath.Join(t.TempDir(), "credentials.json"))
	if _, ok := store.Get("openrouter"); ok {
		t.Fatal("expected no credential when the file does not exist")
	}
}

func TestFileCredentialStore_PutThenGetRoundTripsWithPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atenea", "credentials.json")
	store := NewFileCredentialStore(path)
	if err := store.Put("openrouter", Credential{Type: CredentialTypeAPIKey, APIKey: "sk-or-secret"}); err != nil {
		t.Fatal(err)
	}
	credential, ok := store.Get("openrouter")
	if !ok || credential.Type != CredentialTypeAPIKey || credential.APIKey != "sk-or-secret" {
		t.Fatalf("credential = %#v, ok = %v", credential, ok)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("credentials file permissions = %o, want 600", got)
	}
}

func TestFileCredentialStore_PutKeepsOtherProviders(t *testing.T) {
	store := NewFileCredentialStore(filepath.Join(t.TempDir(), "credentials.json"))
	if err := store.Put("openrouter", Credential{Type: CredentialTypeAPIKey, APIKey: "sk-or"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Put("openai", Credential{Type: CredentialTypeAPIKey, APIKey: "sk-oai"}); err != nil {
		t.Fatal(err)
	}
	credential, ok := store.Get("openrouter")
	if !ok || credential.APIKey != "sk-or" {
		t.Fatalf("openrouter credential lost after writing another provider: %#v, ok = %v", credential, ok)
	}
}

func TestFileCredentialStore_GetToleratesUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	future := `{"credentials":{"openrouter":{"type":"api_key","api_key":"sk-or","refresh_token":"future"}}}`
	if err := os.WriteFile(path, []byte(future), 0o600); err != nil {
		t.Fatal(err)
	}
	credential, ok := NewFileCredentialStore(path).Get("openrouter")
	if !ok || credential.APIKey != "sk-or" {
		t.Fatalf("a file written by a newer binary must still resolve: %#v, ok = %v", credential, ok)
	}
}

func TestFileCredentialStore_PutRefusesToReplaceCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewFileCredentialStore(path)
	if _, ok := store.Get("openrouter"); ok {
		t.Fatal("corrupt file must not resolve credentials")
	}
	if err := store.Put("openrouter", Credential{Type: CredentialTypeAPIKey, APIKey: "sk-or"}); err == nil {
		t.Fatal("Put must refuse to overwrite a corrupt file instead of destroying it")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "{not json" {
		t.Fatalf("corrupt file must stay untouched, got %q err=%v", data, err)
	}
}

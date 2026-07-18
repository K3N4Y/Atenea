package providerconfig

import (
	"os"
	"path/filepath"
	"testing"
)

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

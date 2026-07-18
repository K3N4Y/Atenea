package providerconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// CredentialTypeAPIKey marks a credential that carries a plain API key. New
// credential kinds (OAuth tokens) add their own type value and fields instead
// of migrating the file.
const CredentialTypeAPIKey = "api_key"

// Credential is one stored provider secret. Type discriminates the shape so
// the same file can hold API keys today and OAuth grants later.
type Credential struct {
	Type   string `json:"type"`
	APIKey string `json:"api_key,omitempty"`
}

// CredentialStore is the surface the provider service needs to resolve and
// persist per-provider secrets. The file-backed implementation is the default;
// an OS-keyring implementation can slot in without touching callers.
type CredentialStore interface {
	// Get returns the stored credential for a provider, if any.
	Get(providerID string) (Credential, bool)
	// Put stores (or replaces) the credential for a provider.
	Put(providerID string, credential Credential) error
}

// credentialsFile is the on-disk shape: credentials keyed by provider ID.
// Decoding is deliberately lenient (no DisallowUnknownFields): a newer binary
// may add fields, and an older one must still read everyone else's entries.
type credentialsFile struct {
	Credentials map[string]Credential `json:"credentials"`
}

// FileCredentialStore persists credentials as JSON next to providers.json.
// The file is read on every call so several processes (TUI and Wails app)
// observe each other's writes without coordination.
type FileCredentialStore struct {
	path string
}

func NewFileCredentialStore(path string) *FileCredentialStore {
	return &FileCredentialStore{path: path}
}

// DefaultCredentialsPath stores credentials next to the provider config.
func DefaultCredentialsPath() string {
	return filepath.Join(filepath.Dir(DefaultPath()), "credentials.json")
}

func (s *FileCredentialStore) Get(providerID string) (Credential, bool) {
	file, err := s.load()
	if err != nil {
		return Credential{}, false
	}
	credential, ok := file.Credentials[providerID]
	return credential, ok
}

func (s *FileCredentialStore) Put(providerID string, credential Credential) error {
	file, err := s.load()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load credentials: %w", err)
	}
	if file.Credentials == nil {
		file.Credentials = map[string]Credential{}
	}
	file.Credentials[providerID] = credential
	return s.save(file)
}

// save writes the whole file atomically with private permissions so a crash
// never leaves a half-written secrets file (see writeFileAtomic).
func (s *FileCredentialStore) save(file credentialsFile) error {
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("encode credentials: %w", err)
	}
	data = append(data, '\n')
	if err := writeFileAtomic(s.path, data); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	return nil
}

func (s *FileCredentialStore) load() (credentialsFile, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return credentialsFile{}, err
	}
	var file credentialsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return credentialsFile{}, err
	}
	return file, nil
}

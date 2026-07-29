package providerconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/K3N4Y/atenea/internal/paths"
)

// The credential types this build honors. Type names which arm of [Credential]
// carries the secret; a kind of auth that is neither a string nor a command
// (OAuth, SigV4) adds its own value and its own arm rather than widening one of
// these.
const (
	CredentialTypeAPIKey = "api_key"
	CredentialTypeExec   = "exec"
	CredentialTypeOAuth  = "oauth"
)

// credentialTypes is what an error naming the alternatives prints. Same reason
// the registry names its known wire formats: the answer is data, and an error
// that does not say which data this build was given sends the reader to source
// that no longer holds it.
var credentialTypes = []string{CredentialTypeAPIKey, CredentialTypeExec, CredentialTypeOAuth}

// Credential is one stored provider secret, as a tagged variant: Type names an
// arm, and exactly that arm is populated. [Credential.Validate] is what keeps
// that true, so the type never degenerates into a bag where an exec credential
// also carries an api_key and the reader has to guess which one was meant.
type Credential struct {
	Type   string           `json:"type"`
	APIKey string           `json:"api_key,omitempty"`
	Exec   *ExecCredential  `json:"exec,omitempty"`
	OAuth  *OAuthCredential `json:"oauth,omitempty"`
}

// ExecCredential reads a bearer token from a command's standard output —
// `gcloud auth print-access-token`, an `aws` helper, a corporate token broker.
// It is how a provider whose auth is not a static string gets a home without
// atenea implementing that provider's protocol.
type ExecCredential struct {
	// Command is an argv list, run directly with no shell: there are no quoting
	// rules to learn and no injection semantics to reason about. A pipeline is
	// still expressible as ["sh", "-lc", "..."], which puts the shell in the
	// file where a reader can see it instead of in the reader's assumptions.
	Command []string `json:"command"`
	// TimeoutSeconds bounds one run. Zero means [DefaultExecTimeout].
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// TTLSeconds is how long a token may be reused for model listing (see
	// [CredentialResolver.CachedToken]). Zero means [DefaultExecTokenTTL].
	// There is no way to disable the reuse: what it protects against — one
	// subprocess per provider on every catalog refresh — is not optional.
	TTLSeconds int `json:"ttl_seconds,omitempty"`
}

// OAuthCredential is a subscription the user logged into rather than a key they
// pasted: a short-lived bearer, the token that renews it, and the account the
// endpoint routes on.
//
// It is its own arm, and not an api_key with extra fields, because every field
// here answers a question an api_key never raises — when does this stop working,
// what replaces it, whose account is it. A single string could carry the first
// field and nothing else, and the reader of a flat shape would have to decide for
// itself which fields the type makes meaningful.
type OAuthCredential struct {
	// AccessToken is what a request carries. It lives about an hour, which is why
	// nothing may treat it as a stored constant; resolution renews it.
	AccessToken string `json:"access_token"`
	// RefreshToken renews AccessToken and ROTATES when it does. The renewed one
	// must be written back here, or the user is logged out at the following
	// renewal — which is the one failure mode of this arm that is silent.
	RefreshToken string `json:"refresh_token"`
	// ExpiresAt is when AccessToken stops being accepted. Zero reads as "already
	// expired": renewing a token that was still good costs one request, and
	// sending one that is not costs a failed turn.
	ExpiresAt time.Time `json:"expires_at,omitzero"`
	// AccountID is a header on every request, not a claim about identity. A
	// credential without one cannot be routed, so login refuses to store one.
	AccountID string `json:"account_id"`
}

// Validate reports whether this credential is a well-formed instance of the
// variant it declares. It runs on the way in ([CredentialStore.Put]) and on the
// way out (resolution), because credentials.json is user-editable by design: a
// hand-written entry must be refused with a reason rather than half-honored.
//
// An absent type is refused separately from an unknown one. "Said nothing" and
// "declared something this build does not know" are different facts, and the
// second one deserves the list of what this build does know.
func (c Credential) Validate() error {
	switch c.Type {
	case CredentialTypeAPIKey:
		if c.APIKey == "" {
			return errors.New("an api_key credential needs a non-empty api_key")
		}
		if c.Exec != nil || c.OAuth != nil {
			return errors.New("an api_key credential must not declare exec or oauth")
		}
		return nil
	case CredentialTypeExec:
		if c.APIKey != "" {
			return errors.New("an exec credential must not carry an api_key")
		}
		if c.OAuth != nil {
			return errors.New("an exec credential must not declare oauth")
		}
		if c.Exec == nil || len(c.Exec.Command) == 0 {
			return errors.New("an exec credential needs a non-empty command")
		}
		if strings.TrimSpace(c.Exec.Command[0]) == "" {
			return errors.New("an exec credential needs a program to run as the first element of command")
		}
		if c.Exec.TimeoutSeconds < 0 || c.Exec.TTLSeconds < 0 {
			return errors.New("an exec credential cannot declare a negative timeout_seconds or ttl_seconds")
		}
		return nil
	case CredentialTypeOAuth:
		if c.APIKey != "" || c.Exec != nil {
			return errors.New("an oauth credential must not carry an api_key or declare exec")
		}
		if c.OAuth == nil {
			return errors.New("an oauth credential needs an oauth object")
		}
		if strings.TrimSpace(c.OAuth.AccessToken) == "" {
			return errors.New("an oauth credential needs a non-empty access_token")
		}
		// Without a refresh token the credential dies within the hour and there is
		// no way back except logging in again — which is exactly what a stored
		// credential is supposed to save the user from.
		if strings.TrimSpace(c.OAuth.RefreshToken) == "" {
			return errors.New("an oauth credential needs a non-empty refresh_token, or it cannot be renewed")
		}
		if strings.TrimSpace(c.OAuth.AccountID) == "" {
			return errors.New("an oauth credential needs a non-empty account_id, which requests cannot be routed without")
		}
		return nil
	case "":
		return fmt.Errorf("credential declares no type (known types: %s)", strings.Join(credentialTypes, ", "))
	default:
		return fmt.Errorf("unknown credential type %q (known types: %s)", c.Type, strings.Join(credentialTypes, ", "))
	}
}

// CredentialStore is the surface the provider service needs to read and persist
// per-provider secrets. The file-backed implementation is the default; an
// OS-keyring implementation can slot in without touching callers.
//
// A store persists; it does not resolve. Turning a stored credential into a
// bearer token can mean running a command, which blocks, fails and needs a
// deadline — that is [CredentialResolver]'s job, and keeping it out of here is
// what stops a keyring backend from having to know about subprocesses.
type CredentialStore interface {
	// Get returns the stored credential for a provider, if any. It reports what
	// is stored, including a credential this build cannot honor: refusing one is
	// resolution's answer to give, with a reason.
	Get(providerID string) (Credential, bool)
	// Put stores (or replaces) the credential for a provider. It rejects one
	// that fails Credential.Validate, so a malformed variant never reaches disk.
	Put(providerID string, credential Credential) error
}

// CredentialFile is the optional capability of a [CredentialStore] whose
// credentials come from a file on disk, discovered by type assertion the way
// the provider and tool capabilities are.
//
// Resolution asks for it before running an exec credential: a file anyone can
// write is a file anyone can turn into arbitrary code running as this user. A
// store with no file — a keyring — does not implement it and has nothing to
// check.
type CredentialFile interface {
	CredentialPath() string
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
	path, err := paths.Credentials()
	if err != nil {
		return filepath.Join(".", "atenea", "credentials.json")
	}
	return path
}

// CredentialPath implements [CredentialFile]: it is the file whose permissions
// decide whether an exec credential stored in it is honored.
func (s *FileCredentialStore) CredentialPath() string { return s.path }

func (s *FileCredentialStore) Get(providerID string) (Credential, bool) {
	file, err := s.load()
	if err != nil {
		return Credential{}, false
	}
	credential, ok := file.Credentials[providerID]
	return credential, ok
}

func (s *FileCredentialStore) Put(providerID string, credential Credential) error {
	if err := credential.Validate(); err != nil {
		return fmt.Errorf("credential for provider %q: %w", providerID, err)
	}
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

package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/K3N4Y/atenea/internal/paths"
	"github.com/K3N4Y/atenea/internal/providerconfig"
	"github.com/K3N4Y/atenea/internal/session"
)

// injected builds the Config a caller uses when it wants the host's assembly but
// none of its I/O: with both resources given, New opens no file of its own.
func injected(t *testing.T) Config {
	t.Helper()
	providers, err := providerconfig.Open(context.Background(), "", "", offlineSnapshot(),
		func(string) string { return "" }, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("open provider service: %v", err)
	}
	return Config{Store: session.NewMemoryStore(), Providers: providers}
}

func TestNew_PreservesInjectedIdentityAndDefaultsDevelopment(t *testing.T) {
	releaseConfig := injected(t)
	releaseConfig.Identity = paths.NewIdentity("v1.2.3")
	if got := New(context.Background(), releaseConfig).Identity; got != releaseConfig.Identity {
		t.Fatalf("release identity = %+v, want %+v", got, releaseConfig.Identity)
	}

	if got := New(context.Background(), injected(t)).Identity; got != paths.NewIdentity(paths.DevelopmentVersion) {
		t.Fatalf("development identity = %+v", got)
	}
}

func TestNewSitting_AutoAcceptStartsDisabledAndDoesNotPersist(t *testing.T) {
	first := NewSitting()
	first.AutoAccept.Set("resumed-session", true)
	second := NewSitting()
	if second.AutoAccept.Enabled("resumed-session") {
		t.Fatal("new process sitting inherited auto-accept")
	}
	if second.AutoAccept.Enabled("new-session") {
		t.Fatal("new session did not default to ask")
	}
}

// Which environment key selects which provider is providerconfig's answer and its
// tests pin it. What is the host's own is the last resort: with no key anywhere
// the app still has to be usable, so the host supplies the offline demo and the
// shared fallback has to hand it straight back.
func TestOfflineSnapshot_IsTheFallbackWithoutAnyKey(t *testing.T) {
	for _, provider := range providerconfig.DefaultCatalog().Providers {
		if provider.APIKeyEnv != "" {
			t.Setenv(provider.APIKeyEnv, "")
		}
	}
	got, fromEnvironment := providerconfig.DefaultFallback(offlineSnapshot())
	if fromEnvironment || got.ProviderID != OfflineProviderID || got.Provider == nil {
		t.Fatalf("fallback = %#v (fromEnvironment=%v), want the offline demo provider", got, fromEnvironment)
	}
}

// With dotenv loading disabled, an injected host writes nothing.
func TestNew_ExtractsNothingUnlessAsked(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	New(context.Background(), injected(t))

	if _, err := os.Stat(filepath.Join(home, ".atenea")); !os.IsNotExist(err) {
		t.Fatalf("stat ~/.atenea = %v, want it never created", err)
	}
}

// An empty Root anchors the agent at the process working directory, which is what
// both entrypoints relied on before the host existed.
func TestNew_AnchorsAtTheWorkingDirectoryWhenRootIsEmpty(t *testing.T) {
	want, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	if got := New(context.Background(), injected(t)).Root; got != want {
		t.Fatalf("Root = %q, want the working directory %q", got, want)
	}
}

// A given Root is taken as it is: the desktop app moves its workspace live and a
// headless run will name one on the command line.
func TestNew_KeepsTheConfiguredRoot(t *testing.T) {
	want := t.TempDir()
	cfg := injected(t)
	cfg.Root = want

	if got := New(context.Background(), cfg).Root; got != want {
		t.Fatalf("Root = %q, want %q", got, want)
	}
}

// Every host gets the same sitting, and the sitting is what a rewire must not
// drop: the manager that rebuilds the wiring reads these off the host instead of
// making its own.
func TestNew_PublishesOneSitting(t *testing.T) {
	h := New(context.Background(), injected(t))

	if h.Sitting == nil {
		t.Fatal("Sitting is nil")
	}
	if h.Gate == nil || h.Grants == nil || h.Inbox == nil || h.Agent == nil || h.Snapshots == nil {
		t.Fatalf("sitting is incomplete: %#v", h.Sitting)
	}
}

// Close is what the terminal app calls on the way out. It has to answer for a
// store that cannot be closed as well as for the shared SQLite that can, because
// which one is in use is the caller's choice.
func TestClose_ToleratesAStoreThatIsNotCloseable(t *testing.T) {
	if err := New(context.Background(), injected(t)).Close(); err != nil {
		t.Fatalf("Close() = %v, want nil for a memory store", err)
	}
}

func TestClose_ClosesACloseableStore(t *testing.T) {
	store, err := session.NewSQLiteStore(filepath.Join(t.TempDir(), "atenea.db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := injected(t)
	cfg.Store = store

	if err := New(context.Background(), cfg).Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}
	if _, err := store.Sessions(context.Background()); err == nil {
		t.Fatal("the store still answers after Close()")
	}
}

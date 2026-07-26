package host

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/K3N4Y/atenea/internal/dotenv"
	"github.com/K3N4Y/atenea/internal/providerconfig"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/skill"
)

// Config is what a caller varies. Every field is optional, and the zero value is
// the production assembly minus the two startup side effects, which a test wants
// off and both entrypoints turn on.
type Config struct {
	// Root anchors the agent: the file and exec tools, skill and subagent
	// discovery, and the system prompt. Empty resolves the process working
	// directory, falling back to "." when even that fails.
	Root string
	// Dotenv is a KEY=VALUE file loaded into the environment before anything reads
	// it, so ATENEA_DB, ATENEA_CHECKPOINTS or a provider key can come from the
	// working directory during development. Empty loads nothing, and a release
	// build compiles dotenv.Load to a no-op regardless of this field.
	Dotenv string
	// ExtractBuiltinSkills materializes the skills embedded in the binary into the
	// global skills directory, so a fresh install has them without the user
	// copying anything. Extraction never overwrites a file that already exists, so
	// it is idempotent and it respects local edits.
	ExtractBuiltinSkills bool
	// Store replaces the durable session store. nil opens the SQLite file both
	// hosts share; a test passes session.NewMemoryStore() and touches nothing.
	Store session.Store
	// Providers replaces the provider service. nil opens it on the default paths
	// over the built-in catalog, with the environment and then the offline demo as
	// the fallback until a selection exists.
	Providers *providerconfig.Service
}

// Host is the assembled outer layer. Sitting is embedded, so h.Gate and h.Agent
// read directly while h.Sitting is what a manager receives whole.
type Host struct {
	*Sitting
	// Root is the workspace the agent starts anchored to. The desktop app moves it
	// live through App.SetWorkspace; the terminal app keeps the one it launched in.
	Root string
	// Store is the durable session log. It is the *undecorated* store: bridging it
	// to a UI (event.NewEmittingStore over that UI's bus) is the one part of the
	// assembly that cannot be shared, so each host does it.
	Store session.Store
	// Providers is the catalog, the credentials and the active selection. Both
	// hosts hold the same service on the same files, which is what makes choosing
	// a model in either one change it in both.
	Providers *providerconfig.Service
}

// New assembles the host. The order is the contract: the .env lands before
// anything reads the environment, because the store path, the checkpoint path and
// every provider key can come from it.
//
// ctx bounds the provider service's startup work, which is not all local:
// activating a persisted selection whose credential is an exec command runs that
// command, and the caller has to be able to put a limit on it.
func New(ctx context.Context, cfg Config) *Host {
	if cfg.Dotenv != "" {
		dotenv.Load(cfg.Dotenv)
	}
	if cfg.ExtractBuiltinSkills {
		ExtractBuiltinSkills()
	}
	h := &Host{
		Sitting:   NewSitting(),
		Root:      cfg.Root,
		Store:     cfg.Store,
		Providers: cfg.Providers,
	}
	if h.Root == "" {
		root, err := os.Getwd()
		if err != nil {
			log.Printf("atenea: could not resolve the working directory (%v); anchoring the workspace at \".\"", err)
			root = "."
		}
		h.Root = root
	}
	if h.Store == nil {
		h.Store = openStore()
	}
	if h.Providers == nil {
		h.Providers = openProviders(ctx)
	}
	return h
}

// Close releases what New opened. Today that is the store, and only when the one
// in use is closeable: the shared SQLite is, an injected memory store is not.
func (h *Host) Close() error {
	if closer, ok := h.Store.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// openStore opens the SQLite file both hosts share, so the sessions of one show
// up in the other. A failure is a warning, not a stop: OpenDefault has already
// returned a usable in-memory store, and the app runs without persisting.
func openStore() session.Store {
	store, err := session.OpenDefault()
	if err != nil {
		log.Printf("atenea: could not open SQLite (%v); sessions will NOT persist (in-memory store)", err)
	}
	return store
}

// openProviders opens the provider service on the default paths: the
// providers.json, the model cache and the credentials both hosts read. No failure
// is fatal — the service comes back usable, serving the fallback, and only a
// selection fails, with the reason in the log.
func openProviders(ctx context.Context) *providerconfig.Service {
	fallback, fromEnvironment := providerconfig.DefaultFallback(offlineSnapshot())
	if !fromEnvironment {
		log.Print("atenea: no provider API key in the environment; falling back to the stored selection or the offline demo")
	}
	providers, err := providerconfig.OpenDefault(ctx, fallback)
	if err != nil {
		log.Printf("atenea: provider config: %v", err)
	}
	return providers
}

// ExtractBuiltinSkills writes the embedded skills into ~/.atenea/skills, one of
// the global directories wiring already scans, so they are discovered exactly
// like a skill the user wrote. Neither failure is fatal: the host starts with
// whatever skills are on disk.
//
// It is exported for the one caller that needs the skills a run would see without
// being a run: `atenea skill list` opens no store and no provider, and listing
// fewer skills than the agent has would make the command a second answer to the
// question it exists to answer. Extraction never overwrites, so calling it costs
// nothing and cannot hide a local edit.
func ExtractBuiltinSkills() {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("atenea: could not resolve the home directory to extract the built-in skills: %v", err)
		return
	}
	if err := skill.ExtractBuiltins(filepath.Join(home, ".atenea", "skills")); err != nil {
		log.Printf("atenea: could not extract the built-in skills: %v", err)
	}
}

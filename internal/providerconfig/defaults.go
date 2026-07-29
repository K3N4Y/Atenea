package providerconfig

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"

	"github.com/K3N4Y/atenea/internal/llm"
)

// defaultCatalogJSON is the catalog atenea ships with: the providers a fresh
// install offers before the user has configured anything. It is data rather than
// code, so adding a provider or a model is a reviewable change to one JSON file
// instead of an edit inside a main package nobody can import — and the file has
// exactly the shape of a user's providers.json, so it can be copied as a
// starting point.
//
// Why the entries look the way they do:
//
//   - Order is precedence. It is the order the model picker lists and the order
//     [EnvironmentFallback] walks, so the provider shown first is the one an
//     unconfigured environment lands on. Anthropic leads because Opus 4.8 is the
//     recommended starting point for complex agentic coding; the moving alias is
//     deliberate so the built-in default tracks compatible model improvements,
//     and ANTHROPIC_MODEL pins a snapshot for anyone who wants one.
//   - OpenAI and OpenRouter speak the same protocol under different types,
//     because the dialects differ: OpenAI answers to prompt_cache_key and
//     rejects OpenRouter's top-level `reasoning`. The declared type — never the
//     id — is what decides how a request is shaped.
//   - `openai-codex` is the same vendor again and a third type, because a ChatGPT
//     subscription is not an API key: its tokens are refused by api.openai.com
//     and by chat completions, and the endpoint that accepts them takes a
//     different request. It declares no api_key_env for the same reason — there
//     is no variable that could hold a login — so it is connected, never
//     inherited from the environment.
//   - Model discovery is off everywhere but OpenRouter. OpenAI's GET /models
//     lists embedding, audio and image models the agent loop cannot drive, the
//     codex backend publishes no /models at all, and the OpenCode endpoints
//     publish a catalog that ignores plan entitlement. A curated list is more
//     honest than a filtered one.
//   - A provider's first model is its default: /connect activates it when
//     nothing is selected yet. OpenRouter leads with openrouter/free, which
//     routes to a free model, so a fresh connection always works.
//
//go:embed providers.default.json
var defaultCatalogJSON []byte

// DefaultCatalog decodes the built-in catalog. Every call returns an independent
// Config — the same guarantee [DefaultRegistry] gives — because callers
// normalize it and merge it into whatever the user already has.
//
// It panics instead of returning an error: the file is compiled into the binary,
// so a malformed one is a build defect rather than a runtime condition, and this
// package's own test catches it long before a binary ships.
func DefaultCatalog() Config {
	cfg, err := decodeConfig(bytes.NewReader(defaultCatalogJSON))
	if err != nil {
		panic(fmt.Sprintf("providerconfig: embedded providers.default.json is invalid: %v", err))
	}
	return cfg
}

// OpenDefault opens the provider service the way atenea ships it: the config,
// model cache and credentials at their default paths — the ones every host
// shares, which is what makes a selection in one visible to the other — over the
// built-in registry and catalog.
//
// fallback is what the host chats with until a selection exists; see
// DefaultFallback. A host with a reason to differ still has Open.
func OpenDefault(ctx context.Context, fallback llm.ProviderSnapshot) (*Service, error) {
	credentials := NewFileCredentialStore(DefaultCredentialsPath())
	return Open(ctx, DefaultPath(), DefaultCachePath(), fallback, os.Getenv, nil, nil, nil, credentials, DefaultCatalog())
}

// DefaultFallback is the provider a bare environment can speak, taken from the
// built-in catalog, and false with offline returned unchanged when no key in the
// environment names one. The offline provider is the host's to supply: it is the
// only part of this that is not a fact about the environment.
func DefaultFallback(offline llm.ProviderSnapshot) (llm.ProviderSnapshot, bool) {
	if snapshot, ok := EnvironmentFallback(DefaultCatalog(), os.Getenv, nil); ok {
		return snapshot, true
	}
	return offline, false
}

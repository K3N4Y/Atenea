package providerconfig

import (
	"bytes"
	_ "embed"
	"fmt"
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
//   - Model discovery is off everywhere but OpenRouter. OpenAI's GET /models
//     lists embedding, audio and image models the agent loop cannot drive, and
//     the OpenCode endpoints publish a catalog that ignores plan entitlement. A
//     curated list is more honest than a filtered one.
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

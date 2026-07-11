---
updated_at: 2026-07-10
summary: TDD implementation plan for global provider configuration, hybrid model discovery, runtime switching, and the `/model` TUI selector.
---

# TUI Provider and Model Selector Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a global, persistent `/model` selector that atomically switches the OpenAI-compatible provider, endpoint, and model for every future LLM call without interrupting calls already using an acquired provider snapshot.

**Architecture:** A new `internal/providerconfig` package owns validated JSON configuration, cache persistence, hybrid catalog refresh, provider construction, and selection transactions. `internal/llm.SwitchableProvider` exposes one stable provider reference to wiring while each LLM call acquires an immutable provider/model snapshot; the runner uses that same snapshot for system-prompt selection and streaming. The Bubble Tea model treats `/model` as a local modal command and receives catalog refreshes through the existing engine event channel.

**Tech Stack:** Go 1.23+, Bubble Tea, Bubbles, Lip Gloss, OpenAI-compatible HTTP APIs, `os.UserConfigDir`, JSON, `httptest`, and PTY E2E tests.

---

## File map

### New files

- `internal/llm/switchable.go` — immutable active-provider snapshots and atomic switching.
- `internal/llm/switchable_test.go` — concurrency and model-forcing tests.
- `internal/providerconfig/config.go` — config schema, validation, default path, load, and atomic save.
- `internal/providerconfig/config_test.go` — parsing, validation, path, and atomic-write tests.
- `internal/providerconfig/catalog.go` — configured/cache/remote model merge and refresh coordination.
- `internal/providerconfig/catalog_test.go` — ordering, deduplication, cache, and failure tests.
- `internal/providerconfig/service.go` — startup resolution, provider factory, selection transaction, and public API.
- `internal/providerconfig/service_test.go` — fallback, missing-key, persistence, and switch tests.
- `internal/tui/model_selector.go` — modal state, grouping, filtering, navigation, and rendering.
- `internal/tui/model_selector_test.go` — focused selector behavior and view tests.
- `cmd/atenea-tui/testdata/model-selector/project/.gitkeep` — stable PTY working directory.

### Existing files modified

- `internal/llm/models.go` and `internal/llm/models_test.go` — authenticated OpenAI-compatible discovery remains provider-neutral.
- `internal/session/runner/turn.go` — acquire one provider snapshot before request and prompt construction.
- `internal/session/runner/turn_test.go` — snapshot consistency and legacy-provider behavior.
- `internal/tui/engine.go` and `internal/tui/engine_test.go` — catalog, refresh, current selection, and select operations.
- `internal/tui/model.go`, `internal/tui/view.go`, and tests — local command, modal input, dynamic footer, and overlay.
- `cmd/atenea-tui/main.go` and tests — global config startup and environment fallback.
- `.okf/architecture/tui.md`, `.okf/architecture/llm-opencode-openai.md`, `README.md`, and `.okf/README.md` — shipped behavior and configuration.

## Implementation invariants

- Persist only API-key environment-variable names, never secret values.
- A switch becomes visible only after the new config is atomically renamed into place.
- One logical LLM call uses one provider/model snapshot for the system prompt, compaction decision, request, and stream.
- Plain providers keep the existing epoch-model behavior so fakes and Wails remain compatible.
- `SwitchableProvider.Stream` forces its snapshot model for provider-backed tools such as `web_fetch`.
- `/model` never enters composer history, durable session events, or slash-command prompt expansion.
- Discovery is non-blocking when the modal opens and cannot erase configured or cached models after failure.

## Safety net before Task 1

- [ ] Run the current focused suites before modifying production code:

```bash
go test ./internal/llm ./internal/session/runner ./internal/tui ./cmd/atenea-tui
```

Expected: PASS. If any test fails, record it as preexisting and resolve it before continuing, per repository policy.

- [ ] Run the broad baseline:

```bash
go test ./...
```

Expected: PASS. Preserve the raw relevant output for the final TDD Cycle Evidence table.

## Task 1: Global provider configuration

**Files:**
- Create: `internal/providerconfig/config.go`
- Create: `internal/providerconfig/config_test.go`

- [ ] **Step 1: Write the failing path and valid-load tests**

Add tests for these contracts:

```go
func TestDefaultPath_UsesUserConfigDir(t *testing.T) {
    if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
        t.Skipf("XDG_CONFIG_HOME is not the UserConfigDir override on %s", runtime.GOOS)
    }
    root := t.TempDir()
    t.Setenv("XDG_CONFIG_HOME", root)
    want := filepath.Join(root, "atenea", "providers.json")
    if got := DefaultPath(); got != want {
        t.Fatalf("DefaultPath() = %q, want %q", got, want)
    }
}

func TestLoad_ParsesValidatedProviderSelection(t *testing.T) {
    path := writeConfigFile(t, `{
      "providers":[{
        "id":"openrouter","name":"OpenRouter",
        "type":"openai-compatible",
        "base_url":"https://openrouter.ai/api/v1/",
        "api_key_env":"OPENROUTER_API_KEY",
        "openrouter_reasoning":true,
        "models":["openai/gpt-5","openai/gpt-5"]
      }],
      "selected":{"provider":"openrouter","model":"openai/gpt-5"}
    }`)
    cfg, err := Load(path)
    if err != nil { t.Fatal(err) }
    if got := cfg.Providers[0].BaseURL; got != "https://openrouter.ai/api/v1" {
        t.Fatalf("BaseURL = %q", got)
    }
    if got := cfg.Providers[0].Models; !reflect.DeepEqual(got, []string{"openai/gpt-5"}) {
        t.Fatalf("Models = %#v", got)
    }
    if !cfg.Providers[0].OpenRouterReasoning { t.Fatal("reasoning flag lost") }
}
```

- [ ] **Step 2: Run the tests and verify RED**

Run:

```bash
go test -run 'Test(DefaultPath|Load_)' -v ./internal/providerconfig
```

Expected: FAIL because the package API does not exist. This package-creation compile failure is acceptable for this first RED gate only.

- [ ] **Step 3: Implement schema, normalization, validation, and load**

Create these exact public types and functions:

```go
const ProviderTypeOpenAICompatible = "openai-compatible"

type Provider struct {
    ID                  string   `json:"id"`
    Name                string   `json:"name"`
    Type                string   `json:"type"`
    BaseURL             string   `json:"base_url"`
    APIKeyEnv           string   `json:"api_key_env,omitempty"`
    OpenRouterReasoning bool     `json:"openrouter_reasoning,omitempty"`
    Models              []string `json:"models,omitempty"`
}
type Selection struct {
    Provider string `json:"provider"`
    Model    string `json:"model"`
}
type Config struct {
    Providers []Provider `json:"providers"`
    Selected  Selection  `json:"selected"`
}

func DefaultPath() string
func DefaultCachePath() string
func Load(path string) (Config, error)
func Validate(cfg Config) (Config, error)
```

`DefaultCachePath` resolves `<UserConfigDir>/atenea/models-cache.json` with the same fallback policy as `DefaultPath`. `Validate` normalizes trailing slashes and rejects duplicate IDs, unknown types, missing fields, non-HTTP(S) URLs, blank model IDs, and a missing selected provider. A selected model absent from `models` remains valid because it may be discovered or cached.

- [ ] **Step 4: Verify GREEN and triangulate invalid cases**

Add a table test covering every validation error plus a valid discovered-only selected model. Run:

```bash
go test -run 'Test(DefaultPath|Load_|Validate_)' -v ./internal/providerconfig
```

Expected: PASS.

- [ ] **Step 5: Write RED tests for atomic save**

Test that `Save(path, cfg)` creates the parent directory, writes valid indented JSON, and preserves an existing file when an injected rename fails. Use an unexported package variable `renameFile = os.Rename` for deterministic failure injection.

Run:

```bash
go test -run 'TestSave_' -v ./internal/providerconfig
```

Expected: FAIL because `Save` does not exist.

- [ ] **Step 6: Implement and verify atomic save**

Implement `func Save(path string, cfg Config) error` with `MkdirAll`, same-directory `CreateTemp`, JSON encoding, `Sync`, `Close`, and `Rename`; remove the temp file on every failure.

Run:

```bash
go test -run 'Test(Save_|Validate_|DefaultPath|Load_)' -v ./internal/providerconfig
```

Expected: PASS.

- [ ] **Step 7: Commit Task 1**

```bash
git add internal/providerconfig/config.go internal/providerconfig/config_test.go
git commit -m "$(cat <<'EOF'
feat(config): add global provider configuration

Generated-By: PostHog Code
Task-Id: ef9d64f8-cc97-450e-bfb6-2f212e94a4a4
EOF
)"
```

## Task 2: Hybrid model catalog and cache

**Files:**
- Create: `internal/providerconfig/catalog.go`
- Create: `internal/providerconfig/catalog_test.go`
- Modify: `internal/llm/models.go`
- Modify: `internal/llm/models_test.go`

- [ ] **Step 1: Add a safety test for authenticated discovery**

Verify the existing API `ListModels(ctx, baseURL, apiKey)` sends `Authorization: Bearer secret` to `GET /models`, while an empty key omits the header.

Run:

```bash
go test -run 'TestListModels_.*APIKey' -v ./internal/llm
```

Expected: RED if authentication is missing; otherwise record a preexisting GREEN safety result and do not alter production code unnecessarily.

- [ ] **Step 2: Write RED tests for deterministic source merging**

Define:

```go
type ProviderModels struct {
    ID     string
    Name   string
    Models []string
}
type CachedProvider struct {
    ID        string    `json:"id"`
    BaseURL   string    `json:"base_url"`
    Models    []string  `json:"models"`
    FetchedAt time.Time `json:"fetched_at"`
}
type Cache struct {
    Providers []CachedProvider `json:"providers"`
}
```

Test provider declaration order and, within each provider: selected model first, configured order next, lexicographically sorted remote-only models next, cache-only models last, exact case-sensitive deduplication, and ignoring cache after a base URL change.

Run:

```bash
go test -run 'TestCatalog_Merge' -v ./internal/providerconfig
```

Expected: FAIL because `Catalog` does not exist.

- [ ] **Step 3: Implement the network-free snapshot and cache loader**

Create:

```go
type ModelLister func(context.Context, string, string) ([]string, error)
type Catalog struct {
    mu         sync.RWMutex
    config     Config
    cachePath  string
    cache      Cache
    remote     map[string][]string
    getenv     func(string) string
    list       ModelLister
    refreshing map[string]*providerRefresh
}

func NewCatalog(cfg Config, cachePath string, getenv func(string) string, list ModelLister) *Catalog
func (c *Catalog) Snapshot() []ProviderModels
func (c *Catalog) Refresh(ctx context.Context) ([]ProviderModels, error)
```

`Snapshot` must never perform I/O. Cache corruption is non-fatal and produces configured/selected models.

- [ ] **Step 4: Verify merge GREEN and triangulate refresh**

Add tests for configured-only, cache-only, selected-only, authenticated remote discovery, one provider failing while another succeeds, corrupt cache, and concurrent refreshes sharing one in-flight request per provider.

Run:

```bash
go test -race -run 'TestCatalog_' -v ./internal/providerconfig
```

Expected: PASS with no race report. Successful refreshes atomically save cache; failed refreshes retain old usable data and return it with a joined warning.

- [ ] **Step 5: Commit Task 2**

```bash
git add internal/llm/models.go internal/llm/models_test.go internal/providerconfig/catalog.go internal/providerconfig/catalog_test.go
git commit -m "$(cat <<'EOF'
feat(models): add hybrid provider model catalog

Generated-By: PostHog Code
Task-Id: ef9d64f8-cc97-450e-bfb6-2f212e94a4a4
EOF
)"
```

## Task 3: Snapshot-safe switchable provider

**Files:**
- Create: `internal/llm/switchable.go`
- Create: `internal/llm/switchable_test.go`
- Modify: `internal/session/runner/turn.go`
- Modify: `internal/session/runner/turn_test.go`

- [ ] **Step 1: Write RED tests for immutable snapshots**

Define the intended API in tests:

```go
type ProviderSnapshot struct {
    ProviderID   string
    ProviderName string
    BaseURL      string
    Model        string
    Provider     Provider
}
func NewSwitchableProvider(initial ProviderSnapshot) (*SwitchableProvider, error)
func (p *SwitchableProvider) Acquire() ProviderSnapshot
func (p *SwitchableProvider) Swap(next ProviderSnapshot)
```

Prove a snapshot acquired from provider A continues through A after a swap, while `switcher.Stream(ctx, Request{Model: "stale"})` reaches B with B's selected model.

Run:

```bash
go test -run 'TestSwitchableProvider_' -v ./internal/llm
```

Expected: FAIL because the type does not exist.

- [ ] **Step 2: Implement atomic snapshots and forced models**

Use `atomic.Pointer[ProviderSnapshot]`. `NewSwitchableProvider` validates non-empty identifiers/model and a non-nil delegate; service construction validates every later snapshot before calling the non-failing `Swap`. `Stream` acquires once, overwrites `req.Model`, and delegates.

Run:

```bash
go test -race -run 'TestSwitchableProvider_' -v ./internal/llm
```

Expected: PASS with no race report.

- [ ] **Step 3: Write RED runner consistency tests**

Add tests proving a selected snapshot model `gpt-5` is passed both to the normal/plan system-prompt builder and `Request.Model`, even when `ContextEpoch.Model` is `old-model`. Add a second test proving a plain fake provider still receives the epoch model.

Run:

```bash
go test -run 'TestRunner_(UsesProviderSnapshotModel|PlainProviderKeepsEpochModel)' -v ./internal/session/runner
```

Expected: FAIL because the runner builds from the epoch before choosing the delegate.

- [ ] **Step 4: Acquire one logical-call snapshot in the runner**

Add `func Acquire(provider Provider) ProviderSnapshot` in `internal/llm/switchable.go`. It returns the active snapshot for snapshot-capable providers and `{Provider: provider}` for plain providers.

In `runTurnAttempt`, acquire before request construction:

```go
providerSnapshot := llm.Acquire(r.provider)
model := before.Model
if providerSnapshot.Model != "" { model = providerSnapshot.Model }
req := llm.Request{Model: model, Messages: toLLMMessages(msgs), Tools: mat.Definitions}
// Build normal or plan system prompt with model.
in, err := providerSnapshot.Provider.Stream(ctx, req)
```

Do not call `r.provider.Stream` after acquisition, because that could acquire a second snapshot.

- [ ] **Step 5: Verify GREEN and concurrent packages**

Run:

```bash
go test -race -run 'TestRunner_(UsesProviderSnapshotModel|PlainProviderKeepsEpochModel)' -v ./internal/session/runner
go test -race ./internal/llm ./internal/session/runner
```

Expected: PASS.

- [ ] **Step 6: Commit Task 3**

```bash
git add internal/llm/switchable.go internal/llm/switchable_test.go internal/session/runner/turn.go internal/session/runner/turn_test.go
git commit -m "$(cat <<'EOF'
feat(llm): switch providers with immutable call snapshots

Generated-By: PostHog Code
Task-Id: ef9d64f8-cc97-450e-bfb6-2f212e94a4a4
EOF
)"
```

## Task 4: Provider service and startup transaction

**Files:**
- Create: `internal/providerconfig/service.go`
- Create: `internal/providerconfig/service_test.go`
- Modify: `cmd/atenea-tui/main.go`
- Modify: `cmd/atenea-tui/main_test.go`

- [ ] **Step 1: Write RED service tests for startup resolution**

Define:

```go
type Active struct {
    ProviderID   string
    ProviderName string
    BaseURL      string
    Model        string
}
type ProviderFactory func(def Provider, model, apiKey string) (llm.Provider, error)
type SaveConfig func(path string, cfg Config) error
type Service struct {
    mu         sync.RWMutex
    path       string
    config     Config
    catalog    *Catalog
    switcher   *llm.SwitchableProvider
    getenv     func(string) string
    factory    ProviderFactory
    save       SaveConfig
}

func Open(path, cachePath string, fallback llm.ProviderSnapshot, getenv func(string) string, factory ProviderFactory, save SaveConfig, list ModelLister) (*Service, error)
func (s *Service) Provider() *llm.SwitchableProvider
func (s *Service) Active() Active
func (s *Service) Catalog() []ProviderModels
func (s *Service) Refresh(context.Context) ([]ProviderModels, error)
func (s *Service) Select(context.Context, string, string) (Active, error)
```

Test valid persisted selection, absent config using fallback without creating a file, invalid config returning a usable fallback plus a warning, and missing required key returning fallback plus an error that names the variable.

Run:

```bash
go test -run 'TestService_Open' -v ./internal/providerconfig
```

Expected: FAIL because `Service` does not exist.

- [ ] **Step 2: Implement default provider construction and fallback-aware `Open`**

The default factory must explicitly control the OpenRouter extension:

```go
opts := []llm.Option{llm.WithoutOpenRouterReasoning()}
if def.OpenRouterReasoning { opts = nil }
return llm.NewOpenAIProvider(apiKeyOrPlaceholder, def.BaseURL, model, opts...), nil
```

Use a non-empty placeholder key for keyless local servers. The returned service is always usable when the supplied fallback is valid; the error is a startup warning for `main` to log.

Run:

```bash
go test -run 'TestService_Open' -v ./internal/providerconfig
```

Expected: PASS.

- [ ] **Step 3: Write RED all-or-nothing selection tests**

Add:

```go
func TestService_SelectPersistsBeforeSwapping(t *testing.T)
func TestService_SelectMissingKeyKeepsPreviousSelection(t *testing.T)
func TestService_SelectFactoryFailureKeepsPreviousSelection(t *testing.T)
func TestService_SelectSaveFailureKeepsPreviousSelection(t *testing.T)
```

Record factory, save, and swap ordering with injected functions. `Open` uses `Save` when `save` is nil; tests inject a failing `SaveConfig`. Every failure must preserve both `Active()` and `Provider().Acquire()`.

Run:

```bash
go test -run 'TestService_Select' -v ./internal/providerconfig
```

Expected: FAIL because the transaction is not implemented.

- [ ] **Step 4: Implement the selection transaction and verify GREEN**

Resolve the provider, validate a non-empty model, read the environment key, construct the candidate delegate, clone config with the new selection, call `Save`, then call `Swap`. Update in-memory config only after the swap.

Run:

```bash
go test -race -run 'TestService_(Open|Select)' -v ./internal/providerconfig
```

Expected: PASS.

- [ ] **Step 5: Write RED startup-boundary tests**

Extract testable helpers:

```go
func openProviderService() (*providerconfig.Service, error)
func environmentFallbackSnapshot() llm.ProviderSnapshot
```

Test `XDG_CONFIG_HOME` selection, absent-file environment fallback, and invalid-file fallback warning.

Run:

```bash
go test -run 'Test(OpenProviderService|EnvironmentFallback)' -v ./cmd/atenea-tui
```

Expected: FAIL until `main` uses the service.

- [ ] **Step 6: Wire service startup and verify GREEN**

Pass the stable `Service.Provider()` into `tui.EngineConfig`, pass the service as the model-control dependency, and initialize footer state from `Service.Active()`. Preserve the existing demo fallback when `OPENROUTER_API_KEY` is absent.

Run:

```bash
go test -run 'Test(OpenProviderService|EnvironmentFallback)' -v ./cmd/atenea-tui
go test ./cmd/atenea-tui ./internal/providerconfig
```

Expected: PASS.

- [ ] **Step 7: Commit Task 4**

```bash
git add internal/providerconfig/service.go internal/providerconfig/service_test.go cmd/atenea-tui/main.go cmd/atenea-tui/main_test.go
git commit -m "$(cat <<'EOF'
feat(tui): load persistent provider selection at startup

Generated-By: PostHog Code
Task-Id: ef9d64f8-cc97-450e-bfb6-2f212e94a4a4
EOF
)"
```

## Task 5: Engine model-control boundary

**Files:**
- Modify: `internal/tui/engine.go`
- Modify: `internal/tui/engine_test.go`

- [ ] **Step 1: Write RED delegation tests**

Add:

```go
type ModelService interface {
    Active() providerconfig.Active
    Catalog() []providerconfig.ProviderModels
    Refresh(context.Context) ([]providerconfig.ProviderModels, error)
    Select(context.Context, string, string) (providerconfig.Active, error)
}
```

Extend `EngineConfig` with `Models ModelService`. Test `ModelCatalog` returns a defensive copy, `CurrentModel` reflects service state, and `SelectModel` delegates without rebuilding runner/wiring.

Run:

```bash
go test -run 'TestEngine_(ModelCatalog|CurrentModel|SelectModel)' -v ./internal/tui
```

Expected: FAIL because these methods do not exist.

- [ ] **Step 2: Implement synchronous operations and verify GREEN**

Add:

```go
func (e *Engine) ModelCatalog() []providerconfig.ProviderModels
func (e *Engine) CurrentModel() providerconfig.Active
func (e *Engine) SelectModel(providerID, model string) (providerconfig.Active, error)
```

When `Models` is nil, return an empty catalog and startup status so existing tests can continue using simple fake engines.

Run:

```bash
go test -run 'TestEngine_(ModelCatalog|CurrentModel|SelectModel)' -v ./internal/tui
```

Expected: PASS.

- [ ] **Step 3: Write RED non-blocking refresh tests**

Define:

```go
type ModelsRefreshedMsg struct {
    Providers []providerconfig.ProviderModels
    Err       string
}
func (e *Engine) RefreshModels()
```

Prove `RefreshModels` returns immediately, emits one message through `e.events`, includes usable providers with a warning, and suppresses duplicate engine refreshes while one is blocked.

Run:

```bash
go test -race -run 'TestEngine_RefreshModels' -v ./internal/tui
```

Expected: FAIL.

- [ ] **Step 4: Implement refresh coordination and verify GREEN**

Guard a refresh-in-flight boolean. Clear it before publishing the final message so a later selector opening may refresh again.

Run:

```bash
go test -race -run 'TestEngine_RefreshModels' -v ./internal/tui
go test -race ./internal/tui
```

Expected: PASS.

- [ ] **Step 5: Commit Task 5**

```bash
git add internal/tui/engine.go internal/tui/engine_test.go
git commit -m "$(cat <<'EOF'
feat(tui): expose runtime model controls from engine

Generated-By: PostHog Code
Task-Id: ef9d64f8-cc97-450e-bfb6-2f212e94a4a4
EOF
)"
```

## Task 6: Pure selector state and rendering

**Files:**
- Create: `internal/tui/model_selector.go`
- Create: `internal/tui/model_selector_test.go`

- [ ] **Step 1: Write RED grouping and filter tests**

Define state by logical identity:

```go
type modelChoice struct {
    providerID   string
    providerName string
    model        string
}
type modelSelector struct {
    open      bool
    filter    []rune
    providers []providerconfig.ProviderModels
    active    modelChoice
    selected  modelChoice
    offset    int
    err       string
}
```

Test that provider-name or provider-ID matches show every model for that provider, model-only matches show only matching models, headings are not selectable, empty providers show `No models available`, no matches show `No matches`, and the active pair is selected when visible.

Run:

```bash
go test -run 'TestModelSelector_(Filter|InitialSelection|EmptyStates)' -v ./internal/tui
```

Expected: FAIL because selector state does not exist.

- [ ] **Step 2: Implement pure projection and navigation**

Add:

```go
type modelSelectorRow struct {
    heading bool
    empty   bool
    choice  modelChoice
    text    string
}
func newModelSelector([]providerconfig.ProviderModels, providerconfig.Active, string) modelSelector
func (s modelSelector) rows() []modelSelectorRow
func (s modelSelector) move(delta int) modelSelector
func (s modelSelector) resize(height int) modelSelector
```

Preserve selection by provider/model identity across filter, refresh, and resize.

- [ ] **Step 3: Verify GREEN and triangulate editing/refresh**

Add rune-aware Backspace, Up/Down skipping headings, selection clamping, active marker, refreshed lists preserving a valid selection, and first-choice fallback after disappearance.

Run:

```bash
go test -run 'TestModelSelector_' -v ./internal/tui
```

Expected: PASS.

- [ ] **Step 4: Add compact renderer tests**

Implement `func (s modelSelector) view(width, height int) string`. Assert stripped output contains one heading per provider, only model identifiers underneath, `●` only on the active pair, no endpoints/source metadata, clipped long identifiers, and scroll movement in short terminals.

Run:

```bash
go test -run 'TestModelSelector_View' -v ./internal/tui
```

Expected: PASS.

- [ ] **Step 5: Commit Task 6**

```bash
git add internal/tui/model_selector.go internal/tui/model_selector_test.go
git commit -m "$(cat <<'EOF'
feat(tui): add compact grouped model selector

Generated-By: PostHog Code
Task-Id: ef9d64f8-cc97-450e-bfb6-2f212e94a4a4
EOF
)"
```

## Task 7: `/model` modal integration

**Files:**
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/view.go`
- Modify: `internal/tui/model_test.go`

- [ ] **Step 1: Extend the fake agent and write RED local-command tests**

Extend `Agent` with:

```go
ModelCatalog() []providerconfig.ProviderModels
CurrentModel() providerconfig.Active
RefreshModels()
SelectModel(providerID, model string) (providerconfig.Active, error)
```

Update `fakeAgent` with deterministic data and call recording. Prove exact `/model` and `/model qwen` open the modal, clear the composer, seed the filter, call `RefreshModels`, and do not call `SendPrompt` or alter history. `/models` and `/modelish` must keep existing prompt behavior.

Run:

```bash
go test -run 'TestModel_ModelCommand' -v ./internal/tui
```

Expected: FAIL because `/model` is not intercepted.

- [ ] **Step 2: Intercept before history and slash expansion**

Add `func parseModelCommand(input string) (query string, ok bool)`. Call it in the Enter path after rejecting empty input but before history append, `commands.Resolve`, or agent send. While the selector is open, route all keyboard input to selector handling.

Run:

```bash
go test -run 'TestModel_ModelCommand' -v ./internal/tui
```

Expected: PASS.

- [ ] **Step 3: Write RED modal integration tests**

Cover Up/Down, typing, rune-aware Backspace, Esc, Enter with no match, successful selection, failed selection, and `ModelsRefreshedMsg`. Successful selection closes the modal, updates the footer, and sets `Model changed to <provider> · <model>`. Failure keeps the modal open and previous footer unchanged.

Run:

```bash
go test -run 'TestModel_ModelSelector' -v ./internal/tui
```

Expected: FAIL.

- [ ] **Step 4: Implement modal state and dynamic status**

Replace the fixed model field with:

```go
activeProvider string
activeModel    string
modelSelector  modelSelector
modelNotice    string
```

Keep `WithStatus(agentName, model)` for compatibility and add `WithModelStatus(providerName, model string)`. The normal footer retains agent/mode, model, and usage layout; the success notice includes provider and model.

- [ ] **Step 5: Render the modal overlay and verify GREEN**

While open, render selector content as the top-level modal and preserve transcript/composer state underneath without accepting their input. Handle zero-size terminals and resize without panic.

Run:

```bash
go test -run 'TestModel_(ModelSelector|ModelCommand|.*Footer.*)' -v ./internal/tui
go test ./internal/tui
```

Expected: PASS.

- [ ] **Step 6: Commit Task 7**

```bash
git add internal/tui/model.go internal/tui/view.go internal/tui/model_test.go
git commit -m "$(cat <<'EOF'
feat(tui): connect slash model command to selector

Generated-By: PostHog Code
Task-Id: ef9d64f8-cc97-450e-bfb6-2f212e94a4a4
EOF
)"
```

## Task 8: PTY end-to-end provider switching

**Files:**
- Modify: `cmd/atenea-tui/main_test.go`
- Create: `cmd/atenea-tui/testdata/model-selector/project/.gitkeep`

- [ ] **Step 1: Write the failing primary E2E test**

Create two `httptest.Server` providers with `/models` and streaming `/chat/completions`; record requested model IDs and return distinct assistant text.

Add:

```go
func TestTUI_ModelSelectorSwitchesProviderAndPersistsUnderPTY(t *testing.T)
```

The test writes global config with A selected, starts the PTY, checks model A in the footer, opens `/model`, verifies grouped headings/models without endpoints, selects B, checks confirmation/footer, sends a prompt only to B with model B, restarts with the same config directory, and verifies B remains selected.

Run:

```bash
go test -run TestTUI_ModelSelectorSwitchesProviderAndPersistsUnderPTY -v ./cmd/atenea-tui
```

Expected: FAIL until all layers are integrated.

- [ ] **Step 2: Fix only integration gaps and verify GREEN**

Keep fixes inside Tasks 1-7 files. Inspect ANSI-stripped output for provider grouping, active marker, focus, clipping, and confirmation placement.

Run:

```bash
go test -run TestTUI_ModelSelectorSwitchesProviderAndPersistsUnderPTY -v ./cmd/atenea-tui
```

Expected: PASS.

- [ ] **Step 3: Add concurrent-stream E2E triangulation**

Add:

```go
func TestTUI_ModelSelectorLetsActiveStreamFinishBeforeNextProviderCall(t *testing.T)
```

Provider A holds a stream open; open `/model` and select B while it is active; release A and assert its response completes; then trigger the next LLM call and assert it reaches B with model B. If current key routing blocks modal interaction while working, change that routing as part of Task 7 because the approved behavior requires selection during an active response.

Run:

```bash
go test -run 'TestTUI_ModelSelector(SwitchesProviderAndPersists|LetsActiveStreamFinish)' -v ./cmd/atenea-tui
```

Expected: PASS.

- [ ] **Step 4: Run the full PTY package without cache**

Run:

```bash
go test -count=1 ./cmd/atenea-tui
```

Expected: PASS without layout regressions or flakes.

- [ ] **Step 5: Commit Task 8**

```bash
git add cmd/atenea-tui/main_test.go cmd/atenea-tui/testdata/model-selector/project/.gitkeep
git commit -m "$(cat <<'EOF'
test(tui): cover provider switching under PTY

Generated-By: PostHog Code
Task-Id: ef9d64f8-cc97-450e-bfb6-2f212e94a4a4
EOF
)"
```

## Task 9: Documentation, refactor, and closing evidence

**Files:**
- Modify: `.okf/architecture/tui.md`
- Modify: `.okf/architecture/llm-opencode-openai.md`
- Modify: `README.md`
- Modify: `.okf/README.md`
- Modify as needed: Task 1-8 files for behavior-preserving cleanup only

- [ ] **Step 1: Update shipped architecture and configuration docs**

Document local-command precedence, modal ownership, global config path/schema, `api_key_env`, explicit `openrouter_reasoning`, snapshot semantics, environment fallback, cache separation, refresh failures, and whether selection is allowed during active work.

- [ ] **Step 2: Run focused suites before refactor**

Run:

```bash
go test -race ./internal/providerconfig ./internal/llm ./internal/session/runner ./internal/tui
go test -count=1 ./cmd/atenea-tui
```

Expected: PASS. Record raw relevant output in TDD evidence.

- [ ] **Step 3: Perform behavior-preserving cleanup only**

Allowed cleanup: extract repeated test server/config helpers, centralize provider/model identity comparison, split a file only when responsibilities became mixed, and remove compatibility fields made unnecessary by completed wiring. Rerun the closest package after every cleanup. Do not add provider editing, metadata, new provider types, or per-session selection.

- [ ] **Step 4: Run formatting and static-analysis gates**

Run:

```bash
gofmt -w internal/llm/switchable.go internal/llm/switchable_test.go internal/providerconfig/*.go internal/session/runner/turn.go internal/session/runner/turn_test.go internal/tui/*.go cmd/atenea-tui/*.go
gofmt -l .
go vet ./...
```

Expected: `gofmt -l .` prints nothing and `go vet ./...` exits 0.

- [ ] **Step 5: Run race and whole-suite gates**

Run:

```bash
go test -race ./...
go test -count=1 ./...
```

Expected: both exit 0. Any failure or flake is blocking, including outside directly modified packages.

- [ ] **Step 6: Consolidate TDD Cycle Evidence**

Use this table in progress and final reporting with real commands/results:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Existing focused and broad suites run before changes | focused commands and `go test ./...` | pass/fail/preexisting |
| Understand | Approved spec and provider/TUI/runner boundaries inspected | `.okf/specs/2026-07-10-tui-model-selector.md` and source files | behavior identified |
| RED | Failing behavior tests written before each slice | exact Task 1-8 test names and commands | expected failures, gates ok |
| GREEN | Minimum production changes passed focused tests | exact files and commands | focused tests passed |
| TRIANGULATE | Invalid config, cache, concurrency, modal, restart, and PTY variants passed | race and PTY commands | cases passed |
| REFACTOR | Cleanup retained behavior and all gates passed | `gofmt -l .`, `go vet ./...`, `go test -race ./...`, `go test -count=1 ./...` | clean |

- [ ] **Step 7: Commit Task 9**

```bash
git add .okf/README.md .okf/architecture/tui.md .okf/architecture/llm-opencode-openai.md README.md internal cmd
git commit -m "$(cat <<'EOF'
docs: document TUI provider selection

Generated-By: PostHog Code
Task-Id: ef9d64f8-cc97-450e-bfb6-2f212e94a4a4
EOF
)"
```

## Final acceptance checklist

- [ ] Global config validates and writes atomically without secrets.
- [ ] Absent or invalid config preserves environment startup behavior.
- [ ] Configured, remote, selected, and cached models merge deterministically.
- [ ] Remote discovery authenticates through the configured environment key.
- [ ] Each LLM call acquires one immutable provider/model snapshot.
- [ ] System prompt, compaction, request model, and delegate use that snapshot.
- [ ] An active stream finishes on the old provider; later calls use the new one.
- [ ] `/model` is local, grouped, filterable, compact, and absent from history/logs.
- [ ] Failed selection changes neither persisted nor runtime state.
- [ ] Successful selection updates status and survives restart.
- [ ] PTY tests inspect behavior and rendered layout.
- [ ] Documentation matches shipped schema and behavior.
- [ ] Formatting, vet, race, PTY, and whole-suite gates pass.

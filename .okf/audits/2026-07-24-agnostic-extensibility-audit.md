---
updated_at: 2026-07-26
summary: Audit of how agnostic atenea's seams are, and what to change to enable third-party integrations, contributions, and a plugin system.
---

# Agnosticism and extensibility audit — atenea

> Date: 2026-07-24
> Method: 5 read-only agents in parallel (LLM layer, tool + permission layer,
> config-driven surfaces, session core, entrypoints/wiring), findings verified
> against the code.
> Scope: Go module (`main.go`, `app.go`, `cmd/atenea`, `internal/...`),
> `.goreleaser.yml`, `install.sh`, repo conventions.
> Reference frame: a coding-agent CLI in the Claude Code / OpenCode class.

## 1. Verdict

The **core is better factored than the shell around it**. The agent loop already
depends on interfaces (`llm.Provider`, `session.Store`, `tool.Tool`,
`permission.Gate`, `permission.Policy`, `runner.Compactor`), the TUI talks to the
engine through a one-way protocol, and MCP tools already prove `tool.Tool` is
implementable by a third party at runtime. Those are real assets.

What blocks integrations is not the loop, it is the perimeter:

1. **Nothing is importable.** `go.mod:1` declares `module atenea` — a non-fetchable
   module path — and all 26 non-`main` packages live under `internal/`. No third
   party can consume any of this code without forking.
2. **There is no LICENSE file.** Contributions and any extension ecosystem are
   legally undefined today.
3. **There is no programmatic surface.** `cmd/atenea/main.go:53-62` is the whole
   CLI: one string comparison for `--version`, otherwise the TUI. No headless
   mode, no JSON event stream, no stdin. Every integrator's first ask is missing.
4. ~~**Extension identity is spread across name-keyed switches.**~~
   `[done 2026-07-24]` A tool's permission class, session-grant shape, UI
   rendering and workspace-mutation flag used to be decided by `switch`/lookup on
   the tool's *name* in 6+ files. R2 replaced all six with optional capability
   interfaces the tool implements, so a plugin-contributed tool is first-class in
   everything except a rich presentation, which MCP has no way to carry yet.

The good news: the fixes are mostly *consolidations*, not new machinery. Several
of them delete code.

## 2. Seam scorecard

| Seam | State | Evidence |
|---|---|---|
| Agent loop deps | **Good** — all interfaces, injected | `internal/session/runner/runner.go:20-98` |
| Store | **Good** — interface + shared contract kit reused by both impls and by the two decorators (R1.4) | `internal/session/store.go:50-93`, `internal/session/sessiontest`, `internal/event/contract_test.go` |
| Optional store capabilities | **Good** — runtime type assertion, degrades | `session.CompactionStore`, `session.UndoStore` |
| Compaction strategy | **Good** — `Compactor` interface, injected | `runner/runner.go:67-75` |
| TUI ↔ engine | **Good** — one-way protocol + compile-time assertions | `internal/tui/engine/protocol.go`, `internal/tui/engine_protocol.go:33-38` |
| Shared turn lifecycle | **Good** — UI-independent `agent.Service` already extracted | `internal/agent/service.go`, `.okf/specs/2026-07-13-headless-agent-service-design.md` |
| Permission policy | **Good** — derived from what each tool declares, not from a name list; decorated by session grants (R2) | `internal/permission/policy.go`, `internal/wiring/wiring.go` |
| Tool interface | **Good** — 4-method contract plus optional capabilities for effects, grants and presentation (R2) | `agentcore/tool`, `agentcore/permission/grantable.go` |
| Tool registration | **Partial** — fixed constructor list; `cfg.MCPTools` is the only open slot | `wiring.go:138-142,167-175` |
| LLM `Provider` | **Good** — neutral domain model, an open factory registry (R3.1), an optional capability declaration the host derives windows and reserves from (R3.2), a shipped catalog that is embedded data rather than code (R3.3), and one provider system for both hosts, extensible by the user (R3.4) | `agentcore/llm/capabilities.go`, `internal/providerconfig/registry.go`, `internal/providerconfig/providers.default.json` |
| Provider identity | **Good** — the declared type is the wire format, resolved through a registry that builds *and* describes it; a provider's id decides nothing (R3.1, R3.2) | `internal/providerconfig/registry.go` |
| Provider auth | **Good** — a tagged `Credential` variant with an `api_key` and an `exec` arm, resolved by a `CredentialResolver` separate from the store that persists it (R3.5); a token is still resolved once per selection, not per request | `internal/providerconfig/credentials.go`, `internal/providerconfig/credentialresolver.go` |
| Event taxonomy | **Weak** — open string set, two hand-maintained non-exhaustive switches that already disagree | `internal/tui/transcript.go:59-151`, `frontend/src/features/chat/chat.ts:215-341` |
| Event payload evolution | **Weak** — no version, 4 hand-synced sites per new field | `internal/session/sqlitestore.go:22-57,134-149,279-369,510-601` |
| Tool-use hooks | **Missing** — no pre/post seam; gate + repair + capping hardcoded in the loop | `runner/turn.go:214-235` |
| Prompt assembly | **Missing** — fixed string concatenation | `internal/session/prompt/prompt.go:68-81` |
| Composition root | **Weak** — two outer roots, duplicated provider bootstrap and demo provider | `app.go:76-152` vs `cmd/atenea/main.go:64-130` |
| CLI / headless | **Missing** | `cmd/atenea/main.go:53-62` |
| Public API | **Partial** — 5 importable contract packages under `agentcore/` plus 2 contract test kits, boundary enforced by test; the tool capability interfaces landed with R2; implementations still private, no stability promise yet (R1.3, R1.4, R2 done) | `agentcore/`, `agentcore/boundary_test.go`, `agentcore/{tool/tooltest,llm/llmtest}` |
| Branding/paths | **Weak** — `atenea` literal in 6+ path builders, no XDG | §3.6 |
| MCP as plugin substrate | **Partial** — first-class in the registry and now gated ask-by-default with a session grant (R2), but stdio-only and invisible to subagents | §3.8 |
| Contributor docs | **Weak** — no CONTRIBUTING; `AGENTS.md` points at two files that do not exist; LICENSE now present (R1.2 done) | §3.9 |

## 3. Findings

### 3.1 The module cannot be consumed — at all

> `[done 2026-07-24]` The module path is now `github.com/K3N4Y/atenea` and the
> dead `replace` comment is gone (R1.1). The contract types moved to
> `agentcore/{tool,llm,session,permission}` (R1.3), so the importable surface is
> no longer zero: a third party can implement a tool, a provider, a policy or a
> gate, and read the durable event stream. Everything that runs the agent stays
> under `internal/` on purpose — see
> [Published contracts](../architecture/public-contracts.md). The contracts are
> now checkable as well as importable: `tooltest.Contract` and `llmtest.Contract`
> ship next to them (R1.4), so a tool or an adapter written elsewhere can be
> accepted on evidence rather than on a reading. What remains of this finding is
> the absence of any stability promise on the new packages.

`go.mod:1` is `module atenea`. Even if packages were moved out of `internal/`,
`go get`/import is impossible: there is no resolvable path. Combined with 100% of
logic under `internal/` (`go list ./...` returns two `main` packages plus 26
`internal/...` packages), the current importable API surface is exactly zero.

`go.mod` also carries a commented-out `replace` pointing at a local module cache
path — dead and misleading for anyone cloning the repo.

### 3.2 The provider layer is neutral in its types, closed in its wiring

The domain model is a genuine abstraction, not a leaked wire format: `Request`,
`Message`, `ToolCallPart`, `Event`/`EventKind`, `Usage`
(`internal/llm/provider.go:21-109`), with `ToolDef.Schema` kept as raw JSON
Schema end-to-end (`internal/llm/tool.go:14`) and each SDK type confined to its
adapter. That is the right shape and worth preserving.

The wiring around it is closed:

- ~~**Closed type enum.**~~ ~~**Switch, not registry.**~~ `[done 2026-07-25]` R3.1
  replaced both. A `providerconfig.Registry` (`map[string]Factory`) resolves the
  declared type to the adapter that speaks it, the type is a free string, and an
  unknown one errors naming the registered types instead of failing the whole
  config load. The second switch went with it: the OpenAI dialect was keyed by
  the provider's *id*, so the dialect became the type (`openai`, `openrouter`,
  `openai-compatible`) and identity now decides nothing about request shape. See
  [Provider registry](../architecture/provider-registry.md).
- ~~**Model catalog hardcoded in a `main` package.**~~ `[done 2026-07-26]` R3.3
  moved it into an embedded `providers.default.json` owned by `providerconfig`,
  with the same shape and the same validation as a user's file. The six constants
  went with it, and so did the second copy of three of them that fed the
  environment fallback — that fallback is now derived from the catalog, so catalog
  order is precedence order. See [Provider catalog](../architecture/provider-catalog.md).
- ~~**Context windows hardcoded.**~~ `[done 2026-07-25]` R3.2 deleted the map.
  Windows are declared per wire format by the adapter that speaks it, so a model
  id is only resolved inside the dialect that names it, and an adapter registered
  from outside can answer for its own models. The TUI's second table
  (`curatedModelContext`) went with it.
- **`/connect` allowlist hardcoded twice.** `connectableProviderIDs`
  (`internal/providerconfig/connect.go:15-20`) and `defaultKeyValidator`
  (`connect.go:36-50`).
- ~~**No capability negotiation.**~~ `[done 2026-07-25]` R3.2 added the optional
  `llm.Describing` interface. The `compatibilityProfile` is now observable rather
  than construction-implicit, and the asymmetries listed here are *declared*:
  Anthropic says it does not report retries and caches implicitly, the OpenAI
  dialects say they key the cache on `SessionKey`. What is still true is that
  nothing is *negotiated* — a host cannot ask Anthropic to stop caching, which
  would be a `Request` field rather than a capability. Context-overflow detection
  stayed out on purpose: it is a classification bug in the OpenAI adapter, not a
  capability.
- **No multimodal seam.** `Message` has only `Text string`
  (`provider.go:30-36`); there is no content-part abstraction, so images and
  documents have nowhere to live. Every adapter now declares `Vision: false`
  (R3.2), which is the flag this seam flips.
- ~~**No auth shape beyond a bearer string.**~~ `[done 2026-07-26]` R3.5 made
  `Credential` a tagged variant and added the second arm: `exec` reads a bearer
  token from a command's standard output, so Bedrock, Vertex and an enterprise
  gateway all get a home by borrowing the CLI the user already has installed,
  without atenea implementing anyone's auth protocol. Resolution left the store —
  a store persists, and persisting must not mean executing — for a
  `CredentialResolver` that dispatches on the declared type, times the command
  out, refuses one whose file anyone can write, and caches for listing while
  resolving fresh for a selection. What is still true is that the token is
  resolved once per selection and baked into the adapter: a conversation that
  outlives its token fails at the wire and `/model` is the recovery. See
  [Provider credentials](../architecture/provider-credentials.md).
- ~~**A second, parallel provider system exists.**~~ `[done 2026-07-26]` R3.4
  deleted `internal/wailsprovider`. The desktop app holds the same
  `providerconfig.Service` the TUI does, opened through the same
  `OpenDefault`, so there is one catalog, one credential path and one selection —
  choosing a model in either host changes it in both. The one thing the parallel
  system could do that the service could not, declaring an arbitrary local
  endpoint, became `Service.Declare`/`Forget` rather than a special case of the
  desktop. See [Wails provider surface](../architecture/wails-provider.md).

Cost to add one new wire format when this was written: **7 files**, two of them
`main` packages (`config.go`, `service.go`, new `internal/llm/*.go`, `context.go`,
`connect.go`, `cmd/atenea/main.go`, plus `wailsprovider` for parity). After R3.1
it is one factory plus one registry entry — and for anything OpenAI-shaped, a
closure over existing options rather than an adapter. The remaining files on that
list were R3.2 (`context.go`, now deleted), R3.3 (`cmd/atenea/main.go`, whose
catalog is now embedded JSON in `providerconfig`) and R3.4 (`wailsprovider`, now
deleted). All of them are closed: adding a wire format is one factory plus one
registry entry, and adding an endpoint on an existing format is data — a catalog
line, or `Declare` from either UI.

### 3.3 Tools cannot describe themselves, so core describes them by name

> `[done 2026-07-24]` Resolved by R2: the table below is history. A tool declares
> its `Effects`, its `GrantRule` and its `Presentation`, and every row is now
> derived from those answers. The inverted security default is gone with it — an
> undeclared tool asks — and session grants reach every tool, MCP included. See
> [Tool capabilities](../architecture/tool-capabilities.md).

`tool.Tool` (`internal/tool/registry.go:19-24`) is a clean 4-method contract, and
`Registry.Permissions()` derives the permission set from what was registered
(`registry.go:107-113`) — registration is the source of truth. MCP proves the
interface is externally implementable (`internal/mcpclient/manager.go:253-316`).

But everything *about* a tool beyond execution is decided elsewhere, keyed by
its name string:

| Concern | Where it is decided by name |
|---|---|
| Ask before running | `internal/wiring/wiring.go:87` (`"bash","write","edit","web_fetch"`) |
| Session-grant shape | `internal/permission/rule.go:37-46` (+ bash-specific input parsing at `:117-129`) |
| Transcript rendering | `internal/tui/view.go:330,339,344,351,354` |
| Compact permission label | `internal/tui/permission_panel.go:387-395,503-561` |
| Triggers git-status refresh | `internal/tui/git_summary.go:20-25` |
| Built-in subagent tool sets | `internal/agent/builtins.go:18,24` (string slices) |
| Mode-only exclusion / plan set | `internal/wiring/wiring.go:180,192` |

Consequences:

- A new tool *runs* with zero edits elsewhere and gets a generic transcript line
  (`view.go:360`) — that part is healthy.
- But it can never be gated, granted, or well-presented without editing core
  files it does not own. A third-party tool therefore cannot be first-class.
- **Security default is inverted for extensions.** `StaticPolicy.Decide`
  (`internal/permission/policy.go:43-50`) returns `Allow` for anything not in the
  ask list, and `wiring.go:83-86` states this explicitly: MCP tools are allowed.
  Any third-party MCP server today gets unattended execution. Acceptable for a
  hand-curated setup; not acceptable as the default of an extension ecosystem.
- Session grants are only expressible for `bash`/`write`/`edit`
  (`rule.go:35-46`); everything else, including all MCP tools, re-asks forever
  with no path to "allow for the session".

Files a new built-in tool touches today: **up to 11** (enumerated in the tool
audit: implementation, description, test, two registry lists, ask policy, grant
rule, two TUI files, git summary, builtin agents).

### 3.4 The event taxonomy is open by accident and projected twice

`session.EventKind` is `type EventKind string` with ~24 constants
(`internal/session/event.go:8-65`). Being a string type it is an open set at the
type level, and there is no `exhaustive` linter configured — so switches over it
are non-exhaustive by construction *and* by tooling. Two independent switches
exist:

- `internal/tui/transcript.go:59-151` (`foldEvent`), no `default:`.
- `frontend/src/features/chat/chat.ts:215-341` (`applyEvent`), no `default:`.

They already disagree on which kinds they render. Declared-but-unhandled kinds
include `Session.Cwd`, `Session.Mode`, `Composer.Prompt` and
`Prompt.Checkpoint.*`. A plugin-emitted kind would persist fine (SQLite stores
`Kind` as plain TEXT, no CHECK constraint), traverse the bus unfiltered
(`internal/event/bus.go:22-27`), and then be **silently dropped by both UIs**.

Payload evolution is equally manual. `SessionEvent` has no version field, and
adding one field means editing four hand-synced sites: the `schema` const
(`sqlitestore.go:22-57`), the migration column list (`:134-149`), `AppendEvent`
(`:279-369`) and `sqliteRawEvents` (`:510-601`). Worse, `decodeSummaryFields`
(`internal/session/compaction.go:78-125`) *rejects unknown JSON fields*, so an
older binary reading data written by a newer one fails outright — a
forward-compatibility hazard, not a safety net.

Note the transport is typed in Go and untyped at the boundary: `EmitFunc` is
`func(string, ...interface{})` mirroring Wails (`bus.go:8`), the TUI receives the
native struct over a channel (`engine.go:154-161`) while the web UI receives a
JSON-marshaled copy — two serialization paths for one bus.

### 3.5 No hooks where integrators need them

- **No pre/post tool-use seam.** `consume` (`runner/turn.go:214-235`) calls
  `Registry.Materialize(perms).Settle` directly; the permission gate, the input
  `repair` pass (`registry.go:146-152`) and output capping are hardcoded steps
  inside the loop rather than composable stages.
- **No message transform.** `toLLMMessages` (`turn.go:264-281`) is a fixed
  projection — no redaction/augmentation point.
- **Prompt assembly is string concatenation** in a fixed order
  (`prompt.go:68-81`); there is no ordered list of contributors, so a plugin
  cannot add a prompt section.
- **Session lifecycle hooks exist but are a fixed 3-slot struct**
  (`agent.Hooks{BeforeAdmit, AfterAdmit, AfterRun}`, `internal/agent/service.go:21-25`),
  one value per call, no observer registry. This is *adequate* — flagged only so
  it is not mistaken for a general seam.
- Subagents get `def.Prompt` as their **entire** system prompt
  (`internal/session/subagent/subagent.go:219`) — no env block, no repo
  instructions, no skills. An asymmetry worth deciding deliberately rather than
  inheriting.

### 3.6 Branding and paths are hardcoded across the tree

Literal `atenea` path segments and identifiers, each built independently:

- `<UserConfigDir>/atenea/atenea.db`, `<UserConfigDir>/atenea/checkpoints`
  (`internal/session/open.go:21,25,34,36`)
- `<UserConfigDir>/atenea/mcp.json` (`internal/mcpclient/config.go:29`)
- `<UserConfigDir>/atenea/providers.json`, `models-cache.json`
  (`internal/providerconfig/config.go:42-49`), `credentials.json`
  (`credentials.go:52-54`)
- MCP client identity `{Name: "atenea", Version: "dev"}`
  (`internal/mcpclient/manager.go:93`) — the version literal is wrong; real
  version vars exist in `cmd/atenea/version.go`
- Discovery directories `.atenea/skills`, `.agents/skills`, `.claude/skills`
  (`wiring.go:205-216`) and `.atenea/agents`, `.agents/agents`
  (`wiring.go:128-131`), all string literals with no override

Only two escape hatches exist (`ATENEA_DB`, `ATENEA_CHECKPOINTS`). `os.UserConfigDir()`
is used directly, so `XDG_CONFIG_HOME`/`XDG_DATA_HOME` conventions are not
honored on Linux beyond what that function does.

Two asymmetries in the discovery rules are probably bugs rather than design:
skills search project **and** `$HOME`, but agents search project only; skills
honor `.claude/`, agents do not. `skill.ExtractBuiltins` runs only on the Wails
path (`app.go:148-152`), so the TUI never materializes built-in skills.

Genuine win worth documenting as a contract: reading `.claude/` and `.agents/`,
and using the de-facto `.mcp.json` schema, already makes atenea interoperable
with other agent CLIs (`.okf/architecture/mcp.md:44-45`).

### 3.7 Two composition roots, one shared core

`internal/wiring.Build` is a real single inner composition root, called by both
frontends (`internal/wailsworkspace/manager.go:167`, `internal/tui/engine/engine.go`).
Above it, each frontend re-implements the outer assembly:

- `.env` loading duplicated (`main.go:19`, `cmd/atenea/main.go:67`)
- store opening duplicated (`app.go:175`, `cmd/atenea/main.go:78`)
- demo/fake provider **byte-for-byte duplicated** (`app.go:431-439`,
  `cmd/atenea/main.go:247-255`)
- provider bootstrap implemented **twice with different config models**
  (`internal/wailsprovider` vs `internal/providerconfig`)
- gate/grants constructed separately (`app.go:108`, `engine.go:143-144`)
- `ExtractBuiltins` called on one path only

`internal/wiring/wiring.go` also hardcodes what an embedder would want to
configure: `outputLimit = 32*1024` (`:31`), `askPolicy` (`:87`), skill/agent
discovery paths (`:129-130,205`), the plan-mode allowlist (`:192`) and the
`present_plan` exclusion (`:180`).

### 3.8 MCP is the plugin system in embryo, and is under-built

Strong: MCP tools implement the same `tool.Tool` and land in the *same* registry
(`wiring.go:174-175`) with no special-casing; names are namespaced
`mcp_<server>_<tool>` with a hash fallback over 128 chars
(`manager.go:341-348`); duplicate names fail at connect
(`manager.go:120-123`); server crash self-heals the manager map
(`manager.go:139-146`); tool-level errors come back as normal output text
(`manager.go:318-338`) rather than crashing the turn.

Gaps that keep it from being a plugin substrate:

- **stdio only.** `ServerConfig` (`manager.go:30-36`) has no transport
  discriminator, so a remote server cannot even be expressed
  (`.okf/architecture/mcp.md:85-88` confirms this is deferred).
- ~~**Allow-by-default**~~ `[done 2026-07-24]` R2 flipped it: an MCP tool declares
  nothing about its effects, and silence is gated, so a connected server no longer
  gets unattended execution. Each of its tools is grantable for the session as a
  whole, which is what the panel shows. What remains is the *config schema*: no
  per-server declared sensitivity and no persisted "trust this server" allowlist —
  see R8.2.
- **Never auto-connects** (`.okf/architecture/mcp.md:11-12`) — every session
  starts with zero servers until the user acts.
- **Invisible to subagents**: `cfg.MCPTools` is appended only to the main
  registry, not `childRegistry` (`wiring.go:138-142,174`).
- **Tools only** — no prompts (→ slash commands), no resources (→ `@` mentions),
  no sampling (server borrowing the host model).

### 3.9 Contribution-facing gaps

- **No LICENSE.** Hard blocker for outside contributions and any extension
  ecosystem. `[done 2026-07-24]` — MIT (R1.2).
- **No CONTRIBUTING.md, no SECURITY.md.**
- **`AGENTS.md` points at two things that do not exist**: `CONTEXT.md` at the
  repo root and `.okf/architecture/adr/`. An agent or human following the stated
  way of working hits a dead end immediately.
- **`skills-lock.json` is an orphan.** No Go code reads or writes it
  (`grep -rn "skills-lock"` over `*.go` is empty); it is the lockfile of an
  external third-party skill installer. It implies a distribution model atenea
  does not have.
- **Comment language.** 120 of 143 non-test Go files still carry Spanish
  comments — including the files that would become the published contracts
  (`internal/llm/provider.go`, `internal/tool/registry.go`). `AGENTS.md` mandates
  English docs and the project is already migrating; for an external contributor
  this is the practical barrier right after LICENSE.
- **Two 1.7k–1.9k LOC TUI files** (`view.go` 1894, `model.go` 1696) are exactly
  where every new tool and event forces an edit — the highest merge-conflict
  surface a contributor will meet.
- **Three hand-rolled frontmatter parsers** with different capabilities
  (`internal/skill/skill.go:22-85` supports block scalars; `internal/agent/agent.go:24-71`
  does not support YAML lists; MCP uses plain JSON). No `version` field, no
  validation command. Unknown keys are tolerated only by accident.
- **Release ships the TUI only** (`.goreleaser.yml`, `main: ./cmd/atenea`,
  linux/darwin × amd64/arm64, no Windows); the Wails app has no published
  artifact. Worth stating as intent rather than leaving implicit.

## 4. Recommendations

Ordered by leverage. R1–R3 are the ones that actually unlock third parties.

### R1 — Make the module consumable, and publish contracts only

1. `module github.com/K3N4Y/atenea` in `go.mod`; drop the dead `replace` comment.
   `[done 2026-07-24]` — lowercase path chosen over the repo's literal
   `K3N4Y/Atenea`: it is the Go convention, GitHub resolves the fetch
   case-insensitively, and renaming the repo to `atenea` later needs no code
   change.
2. Add `LICENSE` (Apache-2.0 if a plugin ecosystem is the goal — the patent grant
   matters to corporate contributors; MIT if simplicity wins).
   `[done 2026-07-24]` — MIT chosen: simplicity over the patent grant, and
   consistent with an MCP-only third-party boundary (R8) where extensions run
   out-of-process rather than linking against this code.
3. Move **contract types only** out of `internal/`, keeping implementations
   private:
   - `agentcore/tool` — `Tool`, `Call`, `Result`, capability interfaces (R2)
   - `agentcore/llm` — `Provider`, `Request`, `Message`, `Event`, `Usage`, `Capabilities` (R3)
   - `agentcore/session` — `SessionEvent`, `EventKind` (the stream contract, R5)
   - `agentcore/permission` — `Decision`, `Verdict`, `Rule`
   Keep `runner`, `wiring`, `tui`, stores internal until they have earned
   stability. Migrate with type aliases (`type Tool = tool.Tool`) so no
   call site changes in one commit.
   `[done 2026-07-24]` — the four packages exist and the aliases live in one
   `contract.go` per internal package, so no call site moved. Documented in
   [Published contracts](../architecture/public-contracts.md). Three decisions
   worth recording:
   - `Policy` and `Gate` were published alongside `Decision`/`Verdict`: the
     vocabulary is useless without the interfaces that speak it.
   - `Rule.Matches` came off the type. The shape of a grant is a contract;
     re-deriving a bash prefix from a tool's input is not, and publishing it
     would freeze bash semantics into the API that R2 is meant to replace.
   - R2's capability interfaces must be declared in `agentcore/permission`, not
     `agentcore/tool`: `Grantable` returns a `Rule` while `Policy` takes a
     `Call`, so declaring them tool-side would be an import cycle.
   `agentcore/boundary_test.go` enforces the two invariants that keep this from
   rotting: no package under `agentcore/` may import `internal/`, and none may
   import a third-party module.
4. Ship **contract test kits** next to the contracts: `tooltest.Contract(t, tool)`,
   `llmtest.Contract(t, provider)`, and export the existing store contract
   (`store_contract_test.go` already proves the pattern works). This is how you
   accept outside providers and tools without reviewing them line by line.
   `[done 2026-07-24]` — `agentcore/tool/tooltest` and `agentcore/llm/llmtest`
   ship next to the contracts they check; the store contract moved to
   `internal/session/sessiontest`. Both public kits take a *factory* (each check
   executes for real and must not see another's side effects) plus the happy-path
   input or request, since only the implementer knows what their tool accepts or
   which model their adapter serves. Documented in
   [Published contracts](../architecture/public-contracts.md#contract-test-kits).
   Four decisions worth recording:
   - Checks report through a two-method `reporter` instead of `*testing.T`, which
     is what lets each kit's own test feed it broken implementations and assert
     that the right check fires. Both kits are tested against a compliant
     implementation and against one violation per clause.
   - The store kit stayed private: `Store` is not published and cannot be injected
     from outside, so a public kit for it would advertise a seam that does not
     exist. The move still paid — the contract was an unexported function inside
     `package session`, so the two decorators in `internal/event` (the store the
     runner actually talks to) could not run it. Now they do, and they pass.
   - The kits made two prose promises explicit in the contracts they check: a tool
     must not panic or hang on any input (`agentcore/tool/tool.go`), and a turn is
     bracketed — `StepStarted` first, exactly one `StepEnded`/`StepFailed` last,
     because the host materializes the assistant's message when the turn closes
     (`agentcore/llm/provider.go`).
   - First run against the shipped adapters found a real violation: the OpenAI
     adapter emitted `StepFailed` with `Text` only and no `Err`, leaving a host
     nothing to classify with `errors.As` — the reason §3.2's `ContextOverflowError`
     path could never work outside Anthropic. Fixed in `internal/llm/openai.go`.
     Everything else — 11 builtins, the MCP tools, the subagent `task` tool, the
     two adapters on both their happy path and a failed turn, `SwitchableProvider`,
     the four stores — passed unchanged, including under `-race`.

Rationale: contracts public, loop private. Third parties implement interfaces;
they do not reach into the turn loop. Cheap to do, and it is a precondition for
everything else.

### R2 — Let tools describe themselves (highest leverage)

> `[done 2026-07-24]` The six name-keyed switches are gone. A tool now declares
> what its calls affect, what granting one authorizes and how one should read,
> and the host derives the ask policy, the grant, the workspace refresh and both
> UI surfaces from those answers. The extension default flipped to ask: an MCP
> tool is silent about its effects, and silence is gated. Documented in
> [Tool capabilities](../architecture/tool-capabilities.md). Six decisions worth
> recording:
> - **`Sensitivity` became `Effects`, a set of flags rather than an ordered
>   scale.** The scale does not survive the code: the git summary needs
>   `{write, edit, bash}` but not `web_fetch`, so `>= MutatesWorkspace` would be
>   a lie, and `todo_write`, `task` and `present_plan` would have to declare
>   themselves `ReadOnly`, which they are not. Flags answer both questions
>   honestly, and the vocabulary is narrow on purpose — a flag exists only where
>   the host has a distinct reaction to it, so reading files and mutating the
>   agent's own state are both `NoEffects`.
> - **The tool declares, the policy decides.** The flags carry no "gated" bit;
>   `permission.EffectsPolicy` owns the effects-to-decision mapping. A tool knows
>   what it does, only the host knows how cautious this deployment wants to be —
>   and because the rule is "any declared effect asks" rather than an allowlist of
>   gated flags, adding a flag later can only leave the host more careful.
> - **"Said nothing" and "declared NoEffects" must never be flattened.** Every
>   resolver returns `(value, answered bool)` for exactly this reason: collapsing
>   the two is how an allow-by-default gets reintroduced by accident.
> - **An unregistered tool name is `Allow`.** It cannot run either way — `Settle`
>   refuses it before executing anything — so the decision only picks which
>   failure is seen. Asking would prompt for a call that can never happen; denying
>   would tell the model its call was refused when it actually named a tool that
>   does not exist, which it can fix.
> - **`SessionGrants` split into a store and a decorator.** Deciding whether a
>   grant covers a call means asking the tool that would settle it, which comes
>   from the registry of the moment, while the grants belong to the whole sitting.
>   The store is caller-owned and survives a rewire; `GrantedPolicy` is rebuilt
>   with every registry.
> - **`Presentation` is data, so no `agentcore/ui` was needed.** A tool returns
>   label, subject, body and kind; the host owns every pixel and sanitizes every
>   string, since all of them are text the model wrote. `Body != ""` is what picks
>   the compact permission panel, so a tool that cannot state its call as text
>   degrades to the honest panel instead of a wrong one.
>
> Two parts of this recommendation were deliberately left out. The plan-mode tool
> set stays a literal in `internal/wiring`: it is not derivable from `Effects`
> (`todo_write` declares `NoEffects` and is deliberately absent from plan mode),
> so it is an explicit policy that R4.2 promotes to a config field. And the
> per-server MCP sensitivity declaration plus its persisted allowlist stay with
> R8.2, where this audit already puts them — the flip is livable without them
> because every gated tool is now grantable for the session.

Extend `tool.Tool` with **optional** interfaces discovered by type assertion —
the idiom the codebase already uses for `session.CompactionStore`/`UndoStore`:

```go
// Sensitivity replaces the hardcoded ask list.
type Gated interface { Sensitivity() Sensitivity } // ReadOnly | MutatesWorkspace | Executes | Network

// GrantRule replaces permission.RuleFor's switch.
type Grantable interface { GrantRule(Call) (Rule, bool) }

// Present replaces view.go's name switch: returns data, not rendering.
type Presenter interface { Present(Call, Result) Presentation }
```

Then:

- `askPolicy` (`wiring.go:87`) becomes derived: `Sensitivity()`, defaulting to
  **Ask** for anything that does not declare itself read-only. This flips the
  extension default from allow to ask — the single most important security change
  for a plugin ecosystem. MCP servers declare sensitivity in `.mcp.json`, with a
  persisted per-server allowlist for the "trust this server" case.
- `permission.RuleFor` (`rule.go:37-46`) becomes a dispatch to `Grantable`, so
  every tool — MCP and plugin tools included — can offer "allow for the session".
- `view.go:330-360` and `permission_panel.go:387-561` render a `Presentation`
  struct (`Kind: Diff | FileRef | Command | Activity` + fields). Rendering stays
  in the UI; the *choice* of renderer stops being a name switch. Both frontends
  read the same struct, which also closes the TUI/web divergence for tools.
- `git_summary.go:20-25` derives from `Sensitivity() >= MutatesWorkspace`.
- `agent.Def.Tools` gains validation at parse time against the live registry
  (today unknown names fail silently, `agent.go:56-64`).

Effect: files touched to add a fully first-class tool drops from ~11 to **3**
(implementation, description, test) plus one registration line.

### R3 — Provider registry + capabilities, and delete the parallel system

1. **Registry instead of switch.** A `map[string]Factory` (registered from an
   `init` or an explicit table) replaces `defaultProviderFactory`'s branches. The
   `Type` field becomes a free string resolved through the registry; an unknown
   value errors with the list of registered types instead of being rejected by a
   closed enum.
   `[done 2026-07-25]` — an explicit map, not `init()` registration: there is one
   composition root and it can name what it wants, so `init()` would cost
   determinism and testability for nothing. Documented in
   [Provider registry](../architecture/provider-registry.md). Three decisions
   worth recording:
   - **Both switches went, not one.** Keying only on `Type` would have left the
     `ID` switch alive *inside* the `openai-compatible` factory — a registry with
     a switch in it, and the same name-keyed disease R2 cured for tools. So the
     OpenAI dialect became the type: `openai`, `openrouter` and
     `openai-compatible` are three wire formats, and a provider's identity now
     decides nothing about how a request is shaped.
   - **A bounded, dated migration pays for that.** A config written before this
     says `openai-compatible` for `openai` and `openrouter`; read literally it
     would silently drop `prompt_cache_key` and OpenRouter's routing fields.
     `migrateLegacyDialect` reproduces the old id switch exactly once, at the
     config boundary, and the first `/model` selection rewrites the file.
   - **An unknown type stopped being a config error.** `providers.json` is shared
     between builds with different factories registered; rejecting the file over
     one entry took every other provider down with it. Now it loads, the
     speakable providers work, and only selecting the unspeakable one fails —
     with an error naming what this build does have. Without this the registry
     would be extensible in theory and unusable in practice.
   `/connect`'s id-keyed allowlist and validator were deliberately left alone:
   OpenRouter and OpenCode share a wire format and validate differently, so key
   validation is per-provider, not per-type.
2. **`Provider.Capabilities() Capabilities`** — streaming, tools, reasoning,
   vision, cache shape, retry-event support, default max output,
   `ContextWindow(model) (int, bool)`. This kills `internal/llm/context.go`'s
   hardcoded map (the provider answers for its own models), makes the
   `compatibilityProfile` explicit instead of construction-implicit, and lets the
   UI stop guessing (e.g. it can show "no retry telemetry" rather than silence
   for Anthropic).
   `[done 2026-07-25]` — shipped as an **optional** interface (`llm.Describing`)
   rather than a second method on `Provider`, resolved through one
   `CapabilitiesOf` that returns `(value, answered)`: the same idiom R2 used for
   tools, and for the same reason. Documented in
   [Provider capabilities](../architecture/provider-capabilities.md). Five decisions worth
   recording:
   - **The full field list shipped, including six nothing reads yet.** R2's rule
     — a flag exists only where the host has a distinct reaction — would have cut
     this to `ContextWindows` and `DefaultMaxOutputTokens`. It was overruled
     deliberately: the other six *are* the asymmetries §3.2 catalogued as
     invisible, and declaring them is what makes them visible. `Reasoning: false`
     for Anthropic is the example that justifies the call — the adapter never
     sends the `thinking` parameter, which nobody could read off the code.
   - **Windows are declared per dialect, not in one table keyed by model id.**
     `claude-opus-4-8` and `anthropic/claude-opus-4.8` are the same family under
     two adapters, and the old map had to hold both without being able to say
     which was which — so it answered for adapters the caller was not talking to.
   - **The registry describes as well as builds.** `Registry` became
     `map[string]Format{Build, Describe}` because the model picker labels every
     configured provider and all but the selected one are never constructed. Both
     halves close over the same `llm.Option` values, so a description cannot drift
     from the provider it describes — a test asserts the equality. `Open`'s
     factory parameter became a `Registry` as a consequence.
   - **`SwitchableProvider` does not implement `Describing`.** Answering for a
     delegate that declared nothing would turn "said nothing" into "declared the
     zero value". `llm.ActiveCapabilities` unwraps through the existing `Acquire`
     seam instead, and the TUI resolves it on every use — `/model` swaps the
     adapter, exactly as a rewire swaps the tool registry.
   - **Preventive compaction gained a fix on the way.** A request that leaves
     `MaxOutputTokens` at zero still gets the adapter's own ceiling on the wire,
     so the estimate now reserves it: for Anthropic that is 8192 tokens the
     threshold used to ignore.
3. **Data-driven default catalog.** Move `cmd/atenea/main.go:212-243` into an
   embedded `providers.default.json` owned by `providerconfig`. Adding a provider
   or model becomes a data change, reviewable by anyone, and stops living in an
   unimportable `main` package.
   `[done 2026-07-26]` — the file has the same shape as a user's `providers.json`
   and is decoded by the same `decodeConfig`, so the shipped default is one
   instance of the published format rather than a privileged one. Documented in
   [Provider catalog](../architecture/provider-catalog.md). Four decisions worth
   recording:
   - **The environment fallback came along, because leaving it would have kept the
     duplication R3.3 exists to remove.** `environmentFallbackSnapshot` held a
     second copy of three base URLs and three default models, plus its own
     `llm.NewOpenAIProvider` calls with hand-picked options — a fourth place
     deciding request shape after R3.1 had reduced that to one. It is now
     `EnvironmentFallback(cfg, getenv, registry)`: walk the catalog, take the first
     provider whose key is set, build through the registry.
   - **That made catalog order the precedence order, and changed an answer.** The
     old chain preferred OpenRouter, then OpenAI, then Anthropic; the catalog leads
     with Anthropic, which is what the picker already presented as the default.
     `OPENCODE_API_KEY` now resolves too, instead of dropping a user who holds a
     valid credential into the offline demo.
   - **The model override became a declared `model_env` field, not string surgery
     on `api_key_env`.** Trimming `_API_KEY` and appending `_MODEL` happens to work
     for the three providers that shipped, and breaks for the first gateway named
     by another convention. A field is also discoverable; a naming rule inside a
     loop is not.
   - **`DefaultCatalog()` panics on a malformed file and returns a fresh `Config`
     per call.** Panic because an embedded asset that does not parse is a build
     defect, caught by this package's own test before any binary ships; fresh
     because callers normalize and merge in place, and a shared backing array is
     the failure mode `DefaultRegistry()` already avoids.
   One flakiness hole opened and was closed on the way: the PTY tests blanked three
   key variables by name, so making `OPENCODE_API_KEY` selectable would have made
   them depend on the developer's shell. They now derive the list from the catalog,
   which closes it for the next provider added too.
4. **Delete `internal/wailsprovider`**; make the desktop app consume
   `providerconfig`. This removes a whole duplicated provider model, its second
   `OPENROUTER_API_KEY` lookup and its second set of URL/model constants.
   `[done 2026-07-26]` — the desktop now holds the same `*providerconfig.Service`
   as the TUI and its bindings are a projection of it. Documented in
   [Wails provider surface](../architecture/wails-provider.md). Five decisions
   worth recording:
   - **The deletion had to give something back first.** `wailsprovider` was not
     only duplication: its `local` kind let a user point the app at an arbitrary
     OpenAI-compatible endpoint, which `providerconfig` had no way to express — a
     TUI user could only get LM Studio by hand-editing `providers.json`. Deleting
     the module without that would have traded duplication for a lost feature, so
     it became `Service.Declare`/`Forget` and **both** hosts gained it. That is the
     rule this item is an instance of: fold the second system in by making the
     first one capable, never by dropping what the second one did.
   - **A declared provider is refused what a loaded one is forgiven.** `Declare`
     rejects an unspeakable wire format and an unreachable base URL; loading the
     config still tolerates both (R3.1's decision). The asymmetry is the point: a
     shared file must not lose four working providers over a fifth entry meant for
     another build, but a person typing an endpoint right now can be told. Which is
     also why the URL check lives in `Declare` and not in `normalizeAndValidate`.
   - **Removal needed a fact, not a rule.** The built-in catalog is merged in at
     every launch, so forgetting `anthropic` would look like it worked and undo
     itself at the next start. `Service` remembers which ids came from the defaults
     and `ProviderModels.BuiltIn` carries it outward, so the UI hides the remove
     button for the same reason `Forget` refuses it. Forgetting the *active*
     provider is refused too — asking for another selection first beats pulling the
     provider out from under a live conversation.
   - **`wiring.Config.Local` became `LocalPrompt func() bool`.** The local system
     prompt was reached through the desktop's `kind == local`, which the switch to a
     catalog would have silently dropped (a real fix: that prompt is what stopped
     local models narrating tool calls as text). It is now declared per provider as
     `local_models` — a fact about the endpoint, not a preference — and read once
     per turn instead of baked into an assembly. So a `/model` switch to or from a
     local endpoint changes the prompt on the next turn, in the TUI too, which
     never had this behavior at all. Deriving it from the type would have been
     wrong: OpenCode Zen also declares `openai-compatible` and serves frontier
     models.
   - **The provider handle stopped needing a rebuild, and the fourth window table
     went with the change.** Wiring now receives the `*llm.SwitchableProvider`
     itself, so `wailsworkspace.ProviderState` and `wailsprovider.Snapshot` are both
     gone — there is no provider/config pair left to keep consistent. And with
     capabilities in reach, the desktop hands the declared context window to the UI
     with the active selection, which deleted
     `frontend/src/features/chat/contextWindow.ts`'s hand-maintained map (the copy
     R3.2 left behind) along with its 200K default for everything it did not know.
   The frontend's `localStorage` copy of the selection went too: the backend owns
   `providers.json`, so a stale client copy could no longer re-point a running app
   at an endpoint that had stopped existing.
5. **Widen `Credential` now**, while it is cheap: add an `exec` credential type
   (run a command, read a token from stdout). That covers Bedrock/Vertex/enterprise
   gateways without building OAuth, and establishes the variant shape the existing
   comment already plans for.
   `[done 2026-07-26]` — the arm's payload is nested (`{type, api_key}` /
   `{type, exec:{command, timeout_seconds, ttl_seconds}}`) and `Credential.Validate`
   enforces that exactly the arm `Type` names is populated, so the type cannot
   degenerate into a bag of optional fields the way it would have flat.
   Documented in [Provider credentials](../architecture/provider-credentials.md).
   Six decisions worth recording:
   - **The store persists, the resolver executes, and they are two types.**
     Putting resolution on `CredentialStore` would have made an OS-keyring backend
     — the whole reason that interface exists — responsible for knowing what a
     subprocess is. `CredentialResolver` owns the dispatch, the timeout, the
     guardrails and the cache; `Service` holds both, as two fields, because they
     are two jobs.
   - **Two entry points, because a listing and a conversation want different
     freshness.** `CachedToken` serves model listing, which walks *every*
     configured provider and would otherwise spawn one subprocess per
     exec-credentialed provider each time the picker opens. `Token` resolves fresh
     and is what a selection uses, because the string it returns is baked into the
     adapter a whole conversation runs on — and because re-selecting a model has
     to be the way a user recovers from an expired token. A single TTL split
     between the two cases would have been wrong for both.
   - **Validation at both ends, never in the middle.** `Put` refuses a malformed
     credential and so does resolution, but decoding stays lenient with no type
     check: `credentials.json` is shared between builds and versions, and
     rejecting the file over one entry would take every other provider's
     credential down with it — R3.1's stance on an unknown wire format, applied to
     an unknown credential type. An *absent* type is its own error rather than a
     silent `api_key`, which is R2's "said nothing is not declared nothing" in
     this package.
   - **argv, never a shell.** No quoting rules, no word splitting, no injection
     semantics to reason about in a file that already grants code execution.
     `["sh", "-lc", "..."]` still expresses a pipeline, and puts the shell where a
     reader can see it.
   - **Resolution had to come out from under `s.mu`.** `Select` used to resolve
     while holding the write lock; with a command in the path that freezes the
     picker, the footer and a running turn for the whole timeout.
     `applySelection` resolves first and locks only to persist and swap — what
     `Connect` already did with its network validation, and what `Connect` now
     also does with its selection. `Open` grew a `context.Context` for the same
     reason: activating the persisted selection can run a command, and the caller
     must be able to bound it.
   - **`/connect` deliberately did not grow an exec path.** Its masked input and
     its per-provider network check mean nothing for a credential whose answer is
     ephemeral: "the command produced a token just now" validates nothing about
     the next run. An exec credential is declared by editing the file the spec
     already treats as user-editable, and `Connectable()`'s notion of connected —
     a credential is stored — is unchanged for both arms.
   One behaviour changed on the way, and is the honest reading rather than a
   side effect: a provider that declares no `api_key_env` now uses a stored
   credential instead of the keyless placeholder. A provider authenticated by a
   command has no variable to name, and ignoring its credential would have been a
   silently wrong answer. The environment override still wins, and wins before
   anything is executed.
6. **Add a content-part seam to `Message`** before multimodal is urgent — a
   `Parts []Part` with a text part today, so adding images later is not a
   breaking change to a published contract.

Cost to add a wire format after this: **1 new file + 1 registry line + 1 catalog
entry**.

### R4 — Consolidate to one composition root and add the real integration surface

1. **One host.** `internal/host` (later `agentcore/host`) exposing
   `host.New(host.Config{...}) *Host` that owns: dotenv, paths, store, provider
   service, gate, grants, MCP manager, `wiring.Build`, `agent.Service`. Both
   `main.go` (Wails) and `cmd/atenea` (TUI) construct it. Deletes the duplicated
   `.env` load, the duplicated demo provider, and the `ExtractBuiltins`
   asymmetry.
2. **Promote `wiring.Config`'s hardcoded values to fields**: `OutputLimit`,
   `Policy`, `SkillDirs`, `AgentDirs`, `PlanPermissions`. An embedder configures
   them; both current callers pass defaults.
3. **Headless *CLI* mode — the highest-value integration feature.** Note the
   terminology: `.okf/specs/2026-07-13-headless-agent-service-design.md` is
   already implemented and delivered a *UI-independent* turn lifecycle
   (`internal/agent.Service`). What is missing is a **non-interactive
   entrypoint** driving it. `agent.Service` is exactly the layer this mode
   should call, so most of the work is argument parsing and serialization, not
   new core logic. The event stream you already have *is* the protocol;
   serialize it:
   ```
   atenea run [-p PROMPT] [--output-format text|json|stream-json]
              [--permission-mode deny|allowlist|auto] [--session ID] [--cwd DIR]
   atenea mcp list|add|remove
   atenea skill list|validate
   atenea version
   ```
   Reads stdin when piped, NDJSON of `SessionEvent` on stdout for `stream-json`,
   meaningful exit codes. Interactive `ask` is impossible headless, so
   `--permission-mode` must be explicit and default to the safe end.
   This single feature unlocks CI use, editor plugins, other agents calling
   atenea, and TTY-free end-to-end tests — and it is **language-agnostic**, which
   for most integrators beats a Go SDK.
4. Use stdlib `flag` per subcommand with a small dispatch rather than adding a CLI
   framework; the surface above does not justify the dependency.

### R5 — Make the event stream a stable, forward-compatible contract

1. **Add `default:` to both projections** (`transcript.go`, `chat.ts`) with a
   generic pass-through for unknown kinds and a debug log. Reserve a `x-` /
   `ext.` namespace for extension-emitted kinds.
2. **Single source of truth for the taxonomy**: `go:generate` the TypeScript
   union from the Go constants. The two projections already disagree; generation
   is the only durable fix.
3. **Add an escape hatch to `SessionEvent`**: one `Attrs map[string]string` or
   `Extra json.RawMessage` column. New features and extensions stop requiring the
   4-site SQLite dance for every field.
4. **Fix the forward-compat hazard**: `decodeSummaryFields`
   (`compaction.go:78-125`) must tolerate unknown fields (log, ignore) instead of
   erroring, or old binaries will hard-fail on new data.
5. Add the `exhaustive` linter to CI for `EventKind` switches, or accept
   open-set semantics explicitly and rely on (1). Do not leave it implicit.

Do **not** introduce a migration framework yet — the `ALTER TABLE ADD COLUMN`
loop (`sqlitestore.go:126-171`) is fine until there are external consumers. The
`Attrs` field is what actually removes the pain.

### R6 — One tool-execution middleware chain, one prompt-section list

Two seams, both of which *simplify* current code:

```go
type Middleware func(next SettleFunc) SettleFunc
```
applied in `Registry.Materialize`. The permission gate, the `repair` pass and
output capping become the first three built-in middlewares instead of hardcoded
steps in `turn.go:214-235`. Audit logging, sandboxing, redaction and telemetry —
the four things every integrator asks for — become composable without touching
the loop.

```go
type PromptSection struct { Order int; Name string; Render func(Context) string }
```
replaces the fixed concatenation in `prompt.go:68-81`, so skills, modes, env and
extensions contribute sections rather than being spliced in by hand.

Explicitly **do not** add: a session-lifecycle observer registry (the 3-slot
`agent.Hooks` is sufficient), message-transform hooks (do it as middleware if it
is ever needed), or a pluggable compaction policy beyond the existing
`Compactor` interface (already correct).

### R7 — One paths package, XDG-aware, brand in one place

Create `internal/paths` (later public) as the only place that knows the product
name and the filesystem layout:

- `ConfigDir()`, `DataDir()`, `CacheDir()` honoring `XDG_CONFIG_HOME`/
  `XDG_DATA_HOME`/`XDG_CACHE_HOME` with `os.UserConfigDir()` as fallback
- `DB()`, `Checkpoints()`, `Credentials()`, `Providers()`, `ModelsCache()`,
  `MCPConfig()`
- `SkillDirs(root)`, `AgentDirs(root)` from one ordered, documented list
- One `EnvPrefix = "ATENEA_"` constant so `ATENEA_DB`, `ATENEA_CHECKPOINTS`,
  `ATENEA_CONFIG_DIR` are derived, not re-typed
- One `Product` / `Version` pair, wired from `cmd/atenea/version.go`, and passed
  to the MCP client identity (`manager.go:93` currently hardcodes `"dev"`)

While consolidating, fix the two discovery asymmetries: agents should search
`$HOME` and honor `.claude/agents` exactly as skills do, and `ExtractBuiltins`
should run from the shared host (R4) so the TUI gets built-in skills too.

Document the `.claude/` + `.agents/` + `.mcp.json` compatibility as an explicit,
tested contract — it is a real differentiator and should not be allowed to rot.

### R8 — Make MCP the one and only third-party code boundary

**Recommendation: do not build a second plugin mechanism.** Native Go interfaces
stay for first-party code; MCP is the boundary for everything third-party. It is
already wired as a first-class citizen of the registry, it gives process and
crash isolation for free, it is language-agnostic, and it is where Claude Code
and OpenCode are converging.

Work to make it a genuine plugin substrate:

1. **Transports.** Add a `type` discriminator to `ServerConfig` and support
   `http`/`streamable-http`/`sse` alongside `stdio`. Without this, hosted
   extensions are impossible.
2. **Permissions.** Ship with R2's ask-by-default, plus per-server declared
   sensitivity and a persisted per-server/per-tool allowlist. This is the
   security precondition of an ecosystem.
3. **Expose MCP tools to subagents** (`childRegistry`, `wiring.go:138-142`) — a
   subagent that cannot use the servers the main chat can is a surprising hole.
4. **Beyond tools**: map MCP *prompts* to slash commands and MCP *resources* to
   `@` mentions, and support *sampling* so a server can borrow the host model.
   This is the difference between "tool bridge" and "plugin".
5. **Lifecycle**: opt-in auto-connect for declared servers (today always manual),
   per-server connect/call timeouts, health and restart-with-backoff, and stable
   tool names across reconnects.

Explicitly rejected alternatives, so they are not revisited by accident:

- **Go `plugin` package** — same-compiler/same-dependency-graph requirement, no
  Windows, no unload, breaks with any version skew. A trap.
- **hashicorp/go-plugin or a bespoke gRPC protocol** — duplicates MCP with a
  private schema and no ecosystem.
- **WASM host** — the only alternative with a real advantage (true sandboxing for
  untrusted extensions). Genuinely interesting *later*, if untrusted third-party
  code becomes a requirement; today MCP's process isolation covers ~95% of it at
  a fraction of the cost.

### R9 — Second-tier extension surfaces: unify the manifests

Skills, subagents and commands are already extensible without code, which is the
right shape. Consolidate the plumbing:

1. **One `frontmatter` package** with a real YAML parser, replacing the two
   hand-rolled parsers (`skill.go:22-85`, `agent.go:24-71`) whose capabilities
   already diverge (block scalars vs. comma lists).
2. **Add a `version` field** to skill and agent manifests, and make unknown keys
   tolerated *by design* (documented) rather than by accident.
3. **`atenea skill validate` / `atenea agent validate`** (part of R4's CLI) so a
   contributor gets a real error instead of silent non-discovery.
4. **Decide on `skills-lock.json`**: either own an install/pin model
   (`atenea skill add gh:owner/repo` + lockfile with content hashes, which is what
   that file's format implies) or delete the orphan. Leaving it advertises a
   system that does not exist.
5. Slash commands: the TUI hardcodes `/new`, `/compact`, `/model`, `/mcp`,
   `/connect` (`internal/tui/complete.go:352-433`) while skill-derived commands
   are dynamic. Register the built-ins into the same `command.Set` so one list
   feeds both frontends and name collisions are detectable.

### R10 — Contributor experience

1. `LICENSE`, `CONTRIBUTING.md` (quality gates from `AGENTS.md`, contract-test
   expectations from R1), `SECURITY.md` (matters once extensions execute code).
2. Fix the `AGENTS.md` dead pointers: create `CONTEXT.md` and
   `.okf/architecture/adr/`, or remove the references.
3. **Comment-language migration.** New code is already English by rule. Prioritize
   the packages that become public contracts (`llm`, `tool`, `permission`, session
   event types) — their comments *are* the published documentation — then migrate
   the rest per-package as files are touched.
4. Split `internal/tui/view.go` (1894) and `model.go` (1696) by concern. R2's
   `Presenter` removes the main reason they grow with every feature; splitting
   removes the merge-conflict surface for contributors.
5. Add an ADR for each decision taken here — especially "MCP is the only
   third-party boundary" and "contracts public, loop private". These are the
   decisions future contributors will otherwise relitigate.

## 5. Phased plan

**Phase 0 — unblock (days).** LICENSE. Module path. `internal/paths` (R7). Fix
`AGENTS.md` pointers; resolve `skills-lock.json`. Delete the dead `replace`.
*Outcome: the repo becomes legally and technically contributable.*

**Phase 1 — agnostic core (weeks).** R2 tool capability interfaces and removal of
the six name-keyed switches (landed 2026-07-24). R3 provider registry and
capabilities (both landed 2026-07-25) + data-driven catalog, the deletion of
`wailsprovider` and the widened `Credential` (all three landed 2026-07-26).
*Outcome: tools and providers become additive instead of invasive; the extension
security default flips to ask.*

**Phase 2 — integration surface.** R4 single host + headless `run` with NDJSON.
R1 public contract packages and contract test kits (both landed early,
2026-07-24).
*Outcome: anything can drive atenea — CI, editors, other agents — and outside
contributions of tools/providers become reviewable.*

**Phase 3 — plugin substrate.** R8 MCP transports, permissions, subagent
exposure, prompts/resources/sampling, lifecycle. R9 unified manifests +
`validate`.
*Outcome: a third party ships an extension without touching this repo.*

**Phase 4 — hardening.** R5 event contract (default cases, generated TS union,
`Attrs`, forward-compat fix). R6 tool middleware + prompt sections. R10
contributor experience.
*Outcome: the contracts survive versions and outside code.*

## 6. What not to do

- Do not add a Go in-process plugin API (`plugin` package) or a bespoke
  gRPC plugin protocol. MCP covers it with isolation and an ecosystem.
- Do not build a DI container or a generalized pub/sub bus. The current explicit
  wiring plus the typed bus is clearer and already testable.
- Do not add a SQLite migration framework before there are external consumers;
  add the `Attrs` escape hatch instead.
- Do not make everything public. Publishing the runner or the stores freezes
  internals that still need to move; publish contracts only.
- Do not build a hook framework with many extension points. One tool middleware
  chain and one prompt-section list cover the real cases; more surface is more to
  keep stable forever.

---
updated_at: 2026-07-24
summary: The agentcore/ boundary — which types atenea publishes as contracts, which stay private under internal/, and the rules that keep the split honest.
---

# Published contracts (`agentcore/`)

> Status: implemented 2026-07-24 (audit recommendation R1.3).
> Module path: `github.com/K3N4Y/atenea`.

## The rule

**Contracts public, loop private.**

Everything under `agentcore/` is a type or an interface a third party implements
or reads. Everything that *runs* the agent — the turn loop, the stores, the
wiring, the UIs, the tools and provider adapters atenea ships — stays under
`internal/`, where it is free to move without breaking anyone.

The reason for the asymmetry: an interface someone implements is cheap to keep
stable and expensive to change; an implementation is the opposite. Publishing the
runner or the stores would freeze internals that still need to move, so they are
not published. See the [agnosticism and extensibility
audit](../audits/2026-07-24-agnostic-extensibility-audit.md) §4 R1.

## What is published

| Package | Contract | Implemented or consumed by |
|---|---|---|
| `agentcore/tool` | `Tool`, `Call`, `Result` | anyone shipping a tool |
| `agentcore/llm` | `Provider`, `Request`, `Message`, `ToolCallPart`, `ToolDef`, `Event`, `EventKind`, `Usage` | anyone shipping a model adapter |
| `agentcore/session` | `SessionEvent`, `EventKind`, `Seq`, `Role`, `Message`, `ToolCall`, `Usage`, `ContextEpoch`, `CompactionCheckpoint`, `StructuredSummary`, `CompactionReason`, `PromptCheckpoint` | anyone reading or emitting the durable event stream |
| `agentcore/permission` | `Policy`, `Gate`, `Decision`, `Verdict`, `Request`, `Rule` | anyone replacing the ask-before-run behavior |

`agentcore` itself is a documentation-only package: no code, just the rule and
the test that enforces it.

Two additions beyond the audit's literal list, both for coherence: `Policy` and
`Gate` are published alongside `Decision` and `Verdict`, because the vocabulary
is useless without the interfaces that speak it.

## What stayed private, and why

- **The registry** (`internal/tool`): `Registry`, `Materialized`, `SettleFunc`,
  `Permissions`, `OutputStore`, `UnknownToolError`. How a host materializes and
  settles calls is not a third party's business. `SettleFunc` becomes public when
  the middleware chain (R6) makes it one.
- **The adapters and the catalog** (`internal/llm`): `OpenAIProvider`,
  `AnthropicProvider`, `SwitchableProvider`, `FakeProvider`, `ProviderSnapshot`,
  the context-window table, `ListModels`. `Capabilities` (R3) is the contract
  that will replace the construction-time compatibility profile.
- **The store and the projections** (`internal/session`): `Store`, `MemoryStore`,
  `SQLiteStore`, `Session`, `Inbox`, `EffectiveCheckpoint`, `RunnerContext`,
  `ValidateCompactionCheckpoint`, `DecodeStructuredSummary`. The *shape* of the
  log is a contract; how it is persisted and validated is not.
- **The classification and the grant derivation** (`internal/permission`):
  `StaticPolicy`, `SessionGrants`, `MemoryGate`, `RuleFor` and the bash prefix
  logic. Which tools ask, and how a bash command reduces to a grantable prefix,
  are decisions of this product. `Rule.Matches` came off the type for this
  reason: the shape of a grant is a contract, deriving and matching it against a
  specific tool's input is not.

## How the split is wired

Each `internal/` package that lost types keeps a single `contract.go` holding
type aliases and constant re-exports:

```go
// internal/tool/contract.go
import contract "github.com/K3N4Y/atenea/agentcore/tool"

type (
	Tool   = contract.Tool
	Call   = contract.Call
	Result = contract.Result
)
```

An alias is the same type, so implementation code keeps one spelling
(`tool.Tool`, `llm.Request`, `session.SessionEvent`) regardless of which side of
the boundary defines it, and the ~60 files that use these types did not change.

**The rule for new code**: a new contract type belongs in `agentcore/` and gets
an alias in the matching `internal/` package. A new implementation detail belongs
in `internal/` and nowhere else. `contract.go` is what keeps that decision
visible instead of implicit.

## Invariants, enforced by test

`agentcore/boundary_test.go` parses the imports of every Go file under
`agentcore/` and fails on:

1. **Any import of `internal/`.** A contract that reaches into the implementation
   is not a contract.
2. **Any import of a third-party module.** Implementing a contract must not force
   a dependency on whatever atenea happens to use. Standard library only.

A direct import is the only way a contract can reach the private side — a
transitive path would have to pass through another `agentcore/` package, which
the same walk covers — so scanning source is complete, and it stays hermetic (no
toolchain call, no build tags).

## Dependency direction

```
agentcore/permission ──> agentcore/tool
agentcore/session       agentcore/llm      (both standalone)

internal/*  ──> agentcore/*                (never the reverse)
```

`agentcore/session` deliberately does not import `agentcore/llm`: `session.Usage`
mirrors `llm.Usage` and the producer copies the fields when crossing. The durable
contract does not depend on the provider contract.

**Note for R2** (tool capability interfaces): `Gated`, `Grantable` and
`Presenter` belong in `agentcore/permission` (and a future `agentcore/ui`), not
in `agentcore/tool`. `Grantable` returns a `permission.Rule` while
`permission.Policy` takes a `tool.Call`, so defining those interfaces in
`agentcore/tool` would create an import cycle. Go's convention — the consumer
defines the interface — avoids it: a tool implements interfaces declared where
they are consumed, and the dependency stays `permission -> tool`.

## What this does not yet give a third party

R1.3 makes the contracts importable. It does not, by itself, make the module
usable:

- **No contract test kits** (`tooltest.Contract`, `llmtest.Contract`) — audit R1.4.
- **No stability promise or version tag.** Nothing under `agentcore/` is v1 yet;
  R2, R3 and R5 will each add to these packages, and the additive shape of the
  types is what keeps that from breaking a consumer.
- **No headless entrypoint** to drive the loop with (audit R4.3). The Go contracts
  are for extending atenea, not for driving it; driving it is the CLI's job.

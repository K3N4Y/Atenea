---
updated_at: 2026-07-27
summary: The agentcore/ boundary — which types atenea publishes as contracts, which stay private under internal/, the test kits that make them checkable, and the rules that keep the split honest.
---

# Published contracts (`agentcore/`)

> Status: implemented 2026-07-24 (audit recommendations R1.3 and R1.4).
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
| `agentcore/tool` | `Tool`, `Call`, `Result`; the optional capabilities `Declaring`/`Effects` and `Presenter`/`Presentation`/`PresentationKind`, with their resolvers `EffectsOf` and `PresentationOf` | anyone shipping a tool |
| `agentcore/llm` | `Provider`, `Request`, `Message`, `Part`/`PartKind` with `TextMessage`, `TextOnly` and `UnsupportedPartError`, `ToolCallPart`, `ToolDef`, `Event`, `EventKind`, `Usage`; the optional capability `Describing`/`Capabilities`/`PromptCaching`, with its resolver `CapabilitiesOf` | anyone shipping a model adapter |
| `agentcore/session` | `SessionEvent`, `EventKind`, `Seq`, `Role`, `Message`, `ToolCall`, `Usage`, `ContextEpoch`, `CompactionCheckpoint`, `StructuredSummary`, `CompactionReason`, `PromptCheckpoint` | anyone reading or emitting the durable event stream |
| `agentcore/permission` | `Policy`, `Gate`, `Decision`, `Verdict`, `Request`, `Rule`; the optional capability `Grantable` with its resolver `GrantRuleFor` | anyone replacing the ask-before-run behavior, or shipping a tool that can be granted for a session |

`agentcore` itself is a documentation-only package: no code, just the rule and
the test that enforces it.

Each of the two contracts a third party *implements* ships with its test kit:

| Kit | Runs the contract of | Applied to |
|---|---|---|
| `agentcore/tool/tooltest` | `tool.Tool` | every builtin, the MCP tools, the subagent `task` tool |
| `agentcore/llm/llmtest` | `llm.Provider`, plus `Describing` when the adapter implements it | `FakeProvider`, `SwitchableProvider`, the OpenAI and Anthropic adapters (happy path and failed turn) |
| `internal/session/sessiontest` | `session.Store` (private) | `MemoryStore`, `SQLiteStore` (`:memory:` and file), `EmittingStore`, `ChildPermissionStore` |

Two additions beyond the audit's literal list, both for coherence: `Policy` and
`Gate` are published alongside `Decision` and `Verdict`, because the vocabulary
is useless without the interfaces that speak it.

The optional capability interfaces arrived with R2 and are what make a tool
first-class rather than merely runnable. What each one is for, why `Effects` is a
set of flags rather than an ordered scale, and how the host resolves them is in
[Tool capabilities](tool-capabilities.md).

R3.2 applied the same idiom to the provider contract: `Describing` is optional, so
`Provider` stays a one-method interface, and `CapabilitiesOf` returns
`(value, answered)` so silence never reads as a denial. It is what lets an adapter
answer for the context windows of its own models instead of core keeping a table.
See [Provider capabilities](provider-capabilities.md).

R3.6 changed `Message` itself, which is the first and so far only *breaking*
change to a published type: `Text string` became `Parts []Part`, so content has
one representation and an image is a new `PartKind` rather than a new field on a
type third parties compile against. `TextMessage` builds the text-only case,
`TextOnly` reads it back and hands the caller an `*UnsupportedPartError` instead
of letting it silently drop what it cannot express. See
[Message content](message-content.md).

The session taxonomy is an open set. Consumers preserve unknown kinds through a
generic projection instead of dropping them, while extension producers use the
reserved `ext.<vendor>.<event>` or experimental `x-<name>` namespaces. See
[Durable event stream](event-stream.md).

## What stayed private, and why

- **The registry** (`internal/tool`): `Registry`, `Catalog`, `Materialized`,
  `SettleFunc`, `Permissions`, `OutputStore`, `UnknownToolError`. How a host
  materializes and settles calls is not a third party's business, and `Catalog` is
  a host-side seam (a UI resolving a name to the tool that would settle it), not
  something an extension implements. `SettleFunc` becomes public when the
  middleware chain (R6) makes it one.
- **The adapters and the catalog** (`internal/llm`): `OpenAIProvider`,
  `AnthropicProvider`, `SwitchableProvider`, `FakeProvider`, `ProviderSnapshot`,
  the per-dialect context-window tables, `ListModels`. R3.2 published the
  `Capabilities` contract those tables now answer through, so what stays here is
  the answers themselves — this build's adapters and the models they happen to
  serve.
- **The store and the projections** (`internal/session`): `Store`, `MemoryStore`,
  `SQLiteStore`, `Session`, `Inbox`, `EffectiveCheckpoint`, `RunnerContext`,
  `ValidateCompactionCheckpoint`, `DecodeStructuredSummary`. The *shape* of the
  log is a contract; how it is persisted and validated is not.
- **The classification and the grant derivation** (`internal/permission`):
  `EffectsPolicy`, `SessionGrants`, `GrantedPolicy`, `MemoryGate` and `RuleFor`.
  Which effects are worth interrupting the user for is a decision of this
  deployment, not of the contract — a tool says what it does, the host decides how
  cautious to be about it. The bash prefix derivation moved with R2 to
  `internal/tool/bash.go`, where the semantics it encodes actually live.
  `Rule.Matches` came off the type for the same reason: the shape of a grant is a
  contract, deriving and re-matching it against a specific tool's input is not.

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

## Contract test kits

> Status: implemented 2026-07-24 (audit recommendation R1.4).

A published interface is a promise, and most of what it promises cannot be
expressed in a type. `tool.Tool` compiles whether or not `Execute` panics on a
malformed input; `llm.Provider` compiles whether or not `Stream` ever closes its
channel. The kits are that unwritable half, made runnable:

```go
tooltest.Contract(t, func(t *testing.T) tooltest.Subject {
    return tooltest.Subject{Tool: myTool, Input: json.RawMessage(`{"path":"foo.go"}`)}
})

llmtest.Contract(t, func(t *testing.T) llmtest.Subject {
    return llmtest.Subject{Provider: myAdapter, Request: req}
})
```

Both take a **factory**, not a value: several checks execute for real and must not
observe each other's side effects, so each gets its own subject (and with it its
own `t.TempDir`, its own stub server). Both take the happy-path input or request
with the subject, because only the implementer knows what their tool accepts or
which model their adapter serves.

What they check, and why each clause is in there:

| `tooltest` | Because |
|---|---|
| name stable, `[A-Za-z0-9_-]+`, ≤128 bytes | the registry indexes by it and the provider rejects the whole tool list over one bad name |
| description stable and not blank | it is the whole of what the model knows about the tool |
| schema a stable `{"type":"object"}` | it travels to the provider as raw JSON Schema |
| accepts its declared input | a tool that rejects its own happy path |
| accepts that input re-serialized | the same model spaces and escapes the same JSON differently between turns, so an input read by matching bytes breaks on turn two |
| survives malformed input | the model will send `{`, `null` and `[]` eventually |
| returns on a cancelled context | a cancelled context is how a user interruption arrives |
| safe for concurrent use | the turn settles tools in parallel goroutines |
| `Effects()` stable *(if declared)* | the classification is read per call and cached nowhere, so a tool that changes its answer is gated inconsistently |
| `GrantRule` pure and naming its own tool *(if grantable)* | a grant is matched by tool name, so one naming another tool either never applies or waves through that tool's calls; and the same derivation decides what the user was offered AND whether a later call is covered by it |
| `Present` pure, `Activity` for an unsettled call, no panic *(if a presenter)* | it runs on the goroutine drawing the UI, on every redraw, while the model is still streaming the input — and a host asked for a file card with no diff has nothing to render |

The three capability clauses are skipped for a tool that does not implement the
interface they are about. Not implementing one is legal and simply means the host
has to be careful; implementing one badly is not, because the host then acts on a
claim it cannot verify.

| `llmtest` | Because |
|---|---|
| the channel closes | a consumer drains with `for ev := range out`; a channel left open is a hung session and a leaked goroutine |
| the turn opens with `StepStarted` and closes with exactly one terminal event, last | the host materializes the assistant's message when the turn closes, so a stream that just stops loses the turn from the history |
| `StepFailed` carries `Err` | the host classifies with `errors.As`; it cannot classify a string |
| `Usage` only on `StepEnded` | the turn's tokens are accounted once |
| text, reasoning and per-call tool inputs are bracketed | a delta with no open block, or a block never closed, leaves a UI waiting |
| `ToolCall` carries an id, a name and the complete input | a call without them is unanswerable |
| content the adapter cannot express is refused, not dropped | a skipped part leaves no trace anywhere, so the model answers about an image it never received and nothing downstream can tell that is what happened |
| cancellation closes the channel | interrupting a turn must not leak the goroutine behind it |
| safe for concurrent use | a main turn and its subagents share one provider |

Neither kit says anything about what the tool *does* or whether the model answered
*well*. That is the implementation's own test, and it is the half a host does not
need to trust.

Three decisions worth recording:

- **The checks report through a two-method `reporter`, not `*testing.T`.** That is
  what lets each kit's own test feed it deliberately broken implementations and
  assert that the right check complains. A contract kit that silently passes
  everything is worse than no kit, so both kits are tested against a compliant
  implementation (nothing may be reported) and against one violation per clause
  (the check that owns it must fire).
- **The kits are meaningful under `-race`.** The concurrency clause is a race
  detector clause; without the flag it only proves nothing panicked.
- **The store kit stays under `internal/`.** `session.Store` is not published (the
  shape of the log is a contract, the persistence is not), and there is no way to
  inject a Store from outside, so publishing a kit for one would advertise a seam
  that does not exist. What the move bought is real anyway: the contract used to be
  an unexported function inside `package session`, which meant the two decorators
  in `internal/event` — the store the runner actually talks to — could not run it.
  Now they do.

The kits already earned their keep: the first run of `llmtest` against the shipped
adapters found that the OpenAI adapter emitted `StepFailed` with only `Text` and
no `Err`, so a host had nothing to classify (fixed in `internal/llm/openai.go`).

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

agentcore/tool/tooltest ──> agentcore/tool      (a kit depends on its contract,
agentcore/llm/llmtest   ──> agentcore/llm        never the reverse)

internal/*  ──> agentcore/*                (never the reverse)
```

The kits live under `agentcore/` and are walked by the same boundary test, which
they satisfy: `testing` is standard library, so a kit needs nothing a contract
cannot have.

`agentcore/session` deliberately does not import `agentcore/llm`: `session.Usage`
mirrors `llm.Usage` and the producer copies the fields when crossing. The durable
contract does not depend on the provider contract.

R2 kept that direction. `Declaring` and `Presenter` went into `agentcore/tool`,
since they mention only types that package already owns; `Grantable` went into
`agentcore/permission`, because it returns a `permission.Rule` while
`permission.Policy` takes a `tool.Call` and declaring it tool-side would be a
cycle. Go's convention — the consumer declares the interface — is what resolves
it: a tool implements an interface declared where it is consumed. No
`agentcore/ui` was needed, because `Presentation` is data rather than rendering.

## What this does not yet give a third party

R1.3 makes the contracts importable and R1.4 makes them checkable. Neither, by
itself, makes the module usable:

- **No stability promise or version tag.** Nothing under `agentcore/` is v1 yet,
  and R3.6 is why that still matters: R2 and R3.2 only ever *added* — every new
  type optional, discovered by type assertion — but replacing `Message.Text` with
  `Parts` would have broken an outside adapter had one existed. It was done now
  precisely because none does, and because the seam it lands is what keeps the
  next such change from being necessary. R5 will add to `agentcore/session` and is
  expected to be additive again.
- **No headless entrypoint** to drive the loop with (audit R4.3). The Go contracts
  are for extending atenea, not for driving it; driving it is the CLI's job.

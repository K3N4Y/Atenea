---
updated_at: 2026-08-01
summary: How a tool declares what it affects, what granting it authorizes and how it should read — the optional capability interfaces that replaced six name-keyed switches, and the ask-by-default security flip that came with them.
---

# Tool capabilities

> Status: implemented 2026-07-24 (audit recommendation R2).
> See the [agnosticism and extensibility audit](../audits/2026-07-24-agnostic-extensibility-audit.md) §3.3 and §4 R2.

## The problem it solves

`tool.Tool` was a clean four-method contract — name, description, schema, execute
— and everything *else* about a tool was decided somewhere else, by a `switch` on
its name. Six of them, in six files:

| Concern | Was decided by name in |
|---|---|
| Ask before running | `internal/wiring/wiring.go` (`"bash","write","edit","web_fetch"`) |
| Session-grant shape | `internal/permission/rule.go` |
| Transcript rendering | `internal/tui/view_transcript.go` |
| Compact permission label and body | `internal/tui/permission_panel.go` |
| Triggers a git-status refresh | `internal/tui/git_summary.go` |
| Subagent tool sets validated | nowhere — an unknown name failed silently |

A tool could therefore *run* with no edits elsewhere, but it could never be
gated, granted or well presented without editing core files it does not own. A
tool contributed at runtime — every MCP tool — could not be first-class even in
principle.

## The shape

Three optional interfaces, discovered by type assertion. That is the idiom the
codebase already uses for `session.CompactionStore` and `session.UndoStore`: the
base contract stays small, and a capability is something a tool opts into.

```go
// agentcore/tool
type Declaring interface { Tool; Effects() Effects }
type Presenter interface { Tool; Present(Call, Result) Presentation }

// agentcore/permission — see "Where each one lives" below
type Grantable interface { tool.Tool; GrantRule(call tool.Call) (Rule, bool) }
```

Each has one resolver, and the resolver is the only place the assertion is
written: `tool.EffectsOf`, `tool.PresentationOf`, `permission.GrantRuleFor`. Each
returns `(value, answered bool)`, because **"the tool said nothing" and "the tool
said nothing of substance" are different facts** and flattening them is how a
security default gets inverted by accident.

### `Effects` — what a call affects

A set of flags, not an ordered scale:

```go
const (
    WritesFiles Effects = 1 << iota  // creates, modifies or deletes files in the workspace
    RunsCommands                     // executes something neither host nor tool wrote
    ReachesNetwork                   // contacts a destination taken from the input
)
const NoEffects Effects = 0          // affects nothing outside the conversation
```

The audit proposed an ordered `Sensitivity` (`ReadOnly | MutatesWorkspace |
Executes | Network`). It does not survive contact with the code: the git summary
needs `{write, edit, bash}` but *not* `web_fetch`, so `>= MutatesWorkspace`
would be a lie, and `todo_write`, `task` and `present_plan` would have to declare
themselves `ReadOnly`, which they are not. A set answers both questions honestly:

- ask before running ⇔ any flag set, **or nothing declared**
- refresh the git summary ⇔ `WritesFiles | RunsCommands`

The vocabulary is deliberately narrow: a flag exists only where the host has a
distinct reaction to it. Reading files is not a flag — nothing changes when a tool
reads. Neither is mutating the agent's own state (a todo list, a plan), because it
never leaves the session. Both are `NoEffects`.

`RunsCommands` is the widest flag and implies the others: what a shell command
really touches is whatever it runs, so `bash` declares only `RunsCommands` and
naming `WritesFiles` beside it would add nothing a host can act on.

`ReachesNetwork` is about a destination taken from the *call*. The host's own
connection to the model provider is not it: that connection is the host's, already
accounted for, and not chosen by the call. This is why `task` declares
`NoEffects` even though a subagent talks to the provider.

How the shipped tools declare themselves:

| Tool | Effects | Decision |
|---|---|---|
| `bash` | `RunsCommands` | Ask |
| `write`, `edit` | `WritesFiles` | Ask |
| `web_fetch` | `ReachesNetwork` | Ask |
| `read`, `glob`, `grep`, `skill` | `NoEffects` | Allow |
| `present_plan`, `todo_write` | `NoEffects` | Allow |
| `task` | `NoEffects` | Allow — the child's calls are gated on their own behalf |
| any MCP tool | *undeclared* | **Ask** |

`grep` declares `NoEffects` even though it runs ripgrep, because it builds that
command line itself: the model chooses the pattern, never the program.
`RunsCommands` is for a call that executes what it was handed.

### `Grantable` — what "allow for the session" authorizes

Only the tool can answer honestly, because what an input amounts to is the tool's
own semantics. A shell command reduces to a verb-plus-subcommand prefix; a file
write reduces to the tool itself; an input that cannot be summarized without
overreaching reduces to nothing, and the call keeps asking every time.

The same derivation decides, later, whether an existing grant covers a new call
(`permission.covers` re-derives instead of pattern-matching). That is what keeps a
grant honest: a command the user could never have granted can never be matched by
a grant either. It also makes purity a hard requirement, which `tooltest` checks.

MCP tools grant the whole tool. That is exactly what the panel showed — this tool,
from this server — and narrowing it would mean interpreting arguments whose
meaning lives in the server, not here.

`web_fetch` deliberately does **not** implement `Grantable`: a blanket grant on
outbound network cannot be summarized by anything a panel could honestly show.

### `Presenter` — how a call should read

```go
type Presentation struct {
    Kind    PresentationKind // Activity | FileChange | FileCreation
    Label   string           // "Read", "Bash", "SubAgent"
    Running string           // "Reading" — Label while the call is in flight
    Subject string           // the one thing this call is about
    Body    string           // the full text the call amounts to
    Detail  DetailMode       // Preview | Hidden | OnDemand
}
```

The division of labour is the point. A tool must not return markup, colors or
widths — it cannot know whether it is drawn in a terminal, in a browser or read
aloud. A host must not decide that "the second field of a write call is the
content" — that is the tool's schema and it changes when the tool does.

Four consequences worth stating:

- **Every string is raw text the model wrote.** The host sanitizes, flattens and
  truncates it (`displaySubject` in the TUI, one place for every tool).
- **The zero value is a usable presentation of nothing**, so a tool that says
  nothing and a tool that returns the zero value are handled identically. That is
  what makes `Presenter` optional rather than load-bearing.
- **`Detail` belongs to the tool rather than the host.** Its zero value previews
  bounded output; `Hidden` suppresses model-only output; `OnDemand` keeps useful
  but noisy output collapsed until the reader expands it. A host does not switch
  on tool names to make that decision.
- **`Body != ""` is what selects the compact permission panel.** A tool that can
  state its call as text gets the panel whose body *is* that text; one that cannot
  falls through to the detailed panel showing raw input. An MCP tool degrades to
  the honest panel rather than to a wrong one.

## The security flip

`StaticPolicy` allowed anything not on a hardcoded ask list, and
`internal/wiring` said so explicitly: MCP tools were allowed. Any third-party MCP
server got unattended execution.

`permission.EffectsPolicy` replaces it and reads the registry instead of a list:

```
declared no effect  -> Allow
declared any effect -> Ask
declared nothing    -> Ask     <- every MCP tool, and anything this build has never seen
```

A tool becomes unattended by *saying what it does*, which is a claim its author
signs — not by being added to a list elsewhere in the tree. And because the rule
is "any effect" rather than an allowlist of gated flags, adding a flag to the
vocabulary later can only ever leave the host more careful.

Two details that are easy to get wrong:

- **An unregistered tool name is `Allow`, not `Ask` or `Deny`.** Such a call
  cannot run either way — `Settle` refuses an unregistered tool before executing
  anything, with no side effects — so the decision only picks which failure is
  seen. Asking would put a prompt on screen for a call that can never happen;
  denying would tell the model its call was refused when the truth is that it
  named a tool that does not exist, which is something it can fix.
- **A nil catalog asks about everything.** Nothing can be shown to be harmless, so
  nothing is.

The flip is livable because R2 also makes every gated tool grantable: an MCP tool
asks once and "Allow *mcp_x* this session" stops the questions for that sitting.
R8.2 adds the durable layer without changing this default. An MCP declaration
may classify a server with `sensitivity`; omitting it still means undeclared and
therefore `Ask`. Its `allowedTools` list becomes namespaced permission rules
composed over `EffectsPolicy`, before session grants. Persisted trust is a host
policy decision, not a false `NoEffects` declaration, and cannot overrule Deny.

## The catalog seam

The TUI renders from *durable events*, which carry a tool's name and nothing else.
So asking a tool about itself needs a way to get from a name to the tool:

```go
// internal/tool
type Catalog interface {
    Lookup(name string) (Tool, bool)
    Names() []string
}
```

`*Registry` implements it and is immutable after `NewRegistry`, so it can be
handed to a UI and read from any goroutine. `wiring.Build` publishes it as
`Built.Tools` alongside the runner, the engine swaps it under `mu` on every
rewire, and the Model resolves it per use through an optional `catalogAgent`
interface — never cached, because a rewire replaces the registry and a stale copy
would answer for tools that are gone.

**Every question over a Catalog has an answer for "not registered."** A name
travels a long way: the model produced it, a durable event carried it, a UI
renders it several turns later, and by then the tool can be gone (a workspace
rewire, a disconnected MCP server). Each helper documents which way it degrades —
`MayChangeFiles` says yes (a redundant git read costs milliseconds; a stale diff
stat is a visible bug), `RuleFor` says not grantable, `PresentationFor` falls back
to the tool's name and a generic input summary.

## Where each one lives, and why

`Declaring` and `Presenter` are in `agentcore/tool`: they mention only types that
package already owns.

`Grantable` is in `agentcore/permission`, as [public
contracts](public-contracts.md) anticipated. It returns a `permission.Rule` while
`permission.Policy` takes a `tool.Call`, so declaring it tool-side would be an
import cycle. Go's convention — the consumer declares the interface — resolves it,
and the dependency stays `permission -> tool`.

The *policies* stay private under `internal/permission`. A tool says what it does;
only the host knows how cautious this deployment wants to be about it. That split
is why `EffectsPolicy` owns the effects-to-decision mapping rather than the flags
carrying a "gated" bit.

## Session grants, split

`SessionGrants` used to be both the store of the user's answers and the policy
decorator that read them. It could not stay both: deciding whether a grant covers
a call means asking the tool that would settle it, and that tool comes from the
registry of the moment, while the grants belong to the whole sitting.

So they split along their lifetimes:

- `permission.SessionGrants` — the store. Caller-owned, survives a rewire, so the
  user's answers are not dropped when an MCP server connects.
- `permission.GrantedPolicy` — the decorator. Rebuilt with every registry inside
  `wiring.Build`, where the base classification and the catalog both exist.

## Label vocabulary

The transcript used to name tools inconsistently: `Read` and `SubAgent` for two of
them, the raw `bash`, `write`, `skill`, `web_fetch` for the rest — while the
permission panel said `Bash`, `Write`, `WebFetch`. One `Label` per tool fixes it,
and both surfaces now read from it:

`Read` · `Write` · `Edit` · `Bash` · `Glob` · `Grep` · `Skill` · `Plan` · `Todo` ·
`WebFetch` · `SubAgent`

`read` is the only tool with a `Running` form (`Reading`). A tool that says
nothing keeps its raw name, which is what a long MCP name looks like in the
transcript.

The `Present` implementations live together in `internal/tool/present.go`, unlike
`Effects` and `GrantRule` which sit beside the tool they describe. They are one
vocabulary: the labels have to be consistent with each other, and reading them
apart is how a transcript ends up saying "Read" for one tool and "bash" for the
next.

## What this cost, and what it bought

Files a new first-class built-in tool touches: **3** (implementation with its four
contract methods plus its capabilities, description, test) plus one registration
line in `internal/wiring`. It was up to 11.

An MCP tool — code atenea never sees — now gets: ask-before-run by default, a
session grant, a permission panel titled by its name showing its raw input, and a
transcript line. Everything except a rich presentation, which it cannot have
because MCP carries no way to state one. When it does, `mcpTool.Present` is where
it goes.

## Still open

- **Both frontends do not yet share the struct.** The TUI reads `Presentation`;
  `frontend/src/features/chat/` still renders tools its own way. Closing that
  divergence means the presentation crossing the bus, which touches the event
  payload (audit R5).
- **MCP tools cannot declare effects.** They ask because they are silent, which is
  correct but coarse. A per-server declaration in `.mcp.json` plus a persisted
  allowlist is R8.2.
- ~~**Plan mode's tool set is still a literal** in `internal/wiring`.~~
  `[done 2026-07-26]` R4.2 promoted it to `wiring.Config.PlanMode`, together with
  the `present_plan` exclusion from normal mode, which is the same decision seen
  from the other side. It stays configuration rather than a capability for the
  reason this section gave: it is not derivable from `Effects` — `todo_write`
  declares `NoEffects` and is deliberately absent from plan mode. See
  [composition root](composition-root.md#planmode-is-one-field-because-it-is-one-decision).
- **`Def.Tools` validation reports to the log.** A contributor writing
  `.atenea/agents/foo.md` gets a warning on stderr instead of silence, which is the
  change; `atenea agent validate` (R4.3, R9.3) is the real answer.

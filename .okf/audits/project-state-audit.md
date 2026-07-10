---
updated_at: 2026-07-09
summary: Audit of the project’s implementation state, coverage, and risks.
---

# Status audit — atenea

> Date: 2026-06-27
> Method: 10 agents in parallel, one area each, **read only** (code was not modified). A subset of high severity findings was verified by reading the
> code directly (marked `[verificado]`).
> Scope: Go backend (`app.go`, `git.go`, `terminal.go`, `internal/...`) + frontend
> Vue/TS (`frontend/src/...`).

`atenea` is a Wails desktop app (Go + Vue/TS): an AI agent harness for
coding (agent loop, file/bash tools, sub-agents, skills, persistent
sessions, pty terminal, git integration, chat UI).

---

## 1. Safety net (actual status today)

| Gate | Command | Result |
|---|---|---|
| Go Suite | `go test ./...` | OK, all packets pass |
| Format/static | `gofmt -l .` / `go vet ./...` | clean |
| Frontend Suite | `npm test` (Vitest) | OK, 42 files, 399 tests |

**General verdict:** mature and healthy project on the happy path, without
build or test blockers. Green tests DO NOT cover real concurrency, network borders/
security, or non-trivial paths: that's where the problems live. There is no compilation blocker, but there are several high risks: a latent crash, crashes, silent corruption of edits, and two bypasses of security controls.

### Approximate count of findings
- High: ~13
- Medium: ~20
- Low: ~18
- Info: ~15

---

## 2. Hand-verified findings

These 7 high severity findings were confirmed by reading the actual code, not just
by agent report:

1. `ListCommands` reads `a.commands` without lock (`app.go:595`) — accessor `currentCommands()` exists and is skipped.
2. Subagent runs bash without gate — `subagent.go:164` does `NewRunner` without `SetPermissionGate` (found by 2 agents).
3. WebFetch SSRF by redirect — `webfetch.go:51` creates the client without `CheckRedirect`; `checkSSRF` only validates the initial host.
4. OpenAI client without timeout — `openai.go:43` without `WithRequestTimeout`; `git.go:167` and `app.go:452` use `context.Background()`.
5. `ApplyEdits` does not validate ranges — `apply.go` does not check `Range.End`/`Anchor <= len`; `insertsAt[99]` is never broadcast (content lost).
6. Git renames/paths unicode break the diff — `git.go:80` leaves `Path = "viejo -> nuevo"` literal; the `git.go:63` comment itself supports it as MVP.
7. Data race on `Snapshot.Seen` — `turn.go:160` runs tools in parallel; `patcher.go` reads `snap.Seen` without lock while `RecordSeenLines` writes under lock (panic `concurrent map read and map write`).

---

## 3. Most important (high severity), grouped by impact

### Security — bypass controls
- **Subagents run bash WITHOUT permissions gate** `[verificado]`. The `ask-before-run`
 is only wired in the main runner (`app.go:162`). The child runner
 (`subagent.go:164`) never receives `SetPermissionGate`, so `r.gate==nil` and
 `turn.go:166` do not ask for approval. The model can do `task -> subagente "general"
  -> bash` with arbitrary command without user confirmation. Completely defeat
 the main chat control.
- **WebFetch: SSRF bypass by redirect / DNS-rebinding** `[verificado]`. `checkSSRF`
 validates only the initial host and the client (`webfetch.go:51`) does not define `CheckRedirect`
 (continues up to 10 hops without re-validating). A public URL can reply `302` to
 `169.254.169.254` (metadata cloud) or `127.0.0.1`.

### Crash / crashes
- **Data race on `Snapshot.Seen` -> panic `concurrent map read and map write`**
 `[verificado]`. The runner runs each tool-call of the turn in his goroutine
 (`turn.go:160`); the Patcher reads `snap.Seen` out of lock while `RecordSeenLines`
 writes it under lock. If the model emits `read+edit` or `grep+edit`
 on the same file in the same turn, the process panics and dies. It is the closest thing to a real blocker
.
- **OpenAI client without timeout -> hung turns** `[verificado]`. `NewOpenAIProvider`
 (`openai.go:43`) without `WithRequestTimeout`. Worst: title and commit message use
 `context.Background()` without deadline (`git.go:167`, `app.go:452`). A hung SSE leaves
 the goroutine alive forever with the body open.

### Data corruption/loss
- **`ApplyEdits` does not validate ranges/anchors -> content silently discarded**
 `[verificado]`. No `Range.End`/`Anchor <= len(lines)` check. A `INS.POST 99`
 on 3 lines falls into `insertsAt[99]`, which the broadcast loop never traverses: the
 insertion disappears without error. A `SWAP` with a huge `End` truncates the file queue.
- **`DeleteSession` does not wait for the in-flight run -> the deleted session is resurrected**.
 `App.DeleteSession` cancels and deletes, but does not wait for the `Run` goroutine; a late
 `AppendEvent` re-creates the session with a partial log that reappears in the sidebar.

### Broken functionality
- **Git: renamed files break diff** `[verificado]`. `gitStatus` let
 `Path = "viejo -> nuevo"` literal; opening the `os.ReadFile` diff on that non-existent path
 fails and the side-by-side never opens.
- **Git: paths with spaces / accents / unicode break status->diff** `[verificado]`.
 `git status --porcelain` quotes and escapes those paths; are forwarded with quotes to
 `git diff`/`ReadFile` and fail. Critical for the audience of this app (projects in
 Spanish with accented names). Fix: `--porcelain -z` (or `core.quotepath=false`) and
 starting by NUL resolves renames and unicode together.

### Concurrency
- **`ListCommands` reads `a.commands` without mutex** `[verificado]` — data race against
 `SetWorkspace`; `currentCommands()` exists just for this (`app.go:595`).
- **`shutdown` incomplete** — `OnShutdown` only closes terminals: does not cancel runs, does not
 wait for goroutines, does not close SQLite -> half writes / db without clean flush.
- **Possible invalid order assistant<->tool in turn** — the `Message` the assistant
 (with `tool_calls`) and the `Tool.Success` are published from different goroutines without
 guaranteed order; with quick/local tool the `Seq` of the tool result can be before
 of the assistant -> the API rejects the history (HTTP 400).
- **`context.Canceled` is shown as an error in the UI** — Stop / workspace change /
 follow-up cancels the run and the raw `context.Canceled` is painted as a red error.
- **Frontend: race in `loadSession`** — `await SessionHistory(id)` without rechecking that
 `sessionID.value===id` after await; Double click A->B mixes the history of two chats.

> The **steering of the loop is dead code** as the app wires it today: sending a
> second message while the agent is working aborts the shift instead of queuing it/
> steering it (work is lost). `run.go` implements steer but `SendPrompt` always
> cancels.

---

## 4. Detailed findings by area

Format of each finding: **title** — `severidad` / `categoria` — `ubicacion`.
Description and recommendation below.

### Area 1 — Wails app boundary and life cycle
Files: `app.go`, `main.go`, `dotenv.go`, `terminal.go`, `wails.json`, bindings
generated in `frontend/wailsjs/`.
Purpose: Go<->JS bridge; exposes bindings that the UI invokes, starts/closes the app,
builds the wiring of the agent anchored to the workspace, forwards events via `EventsEmit`,
loads secrets from `.env` in dev.
Status: medium-high. Mutable wiring released under `mu`, permissions gate correct,
errors propagated without swallowing. Bounded real gaps.

- **ListCommands reads a.commands without the mutex (data race vs SetWorkspace)** — `high` /
 `concurrency` — `app.go:595` (vs accessor `app.go:193-197`, swap `app.go:170-175`).
 `[verificado]`. Returns `a.commands.List()` directly while the rest uses
 `currentCommands()` under `a.mu` and `wire()` replaces the pointer with the taken lock.
 Fix: `return a.currentCommands().List(), nil`.
- **shutdown does not cancel runs, does not wait for goroutines, nor closes SQLite** — `high` /
 `error-handling` — `terminal.go:14`, `app.go:317-324`, `app.go:645-656`. `OnShutdown`
 only does `term.CloseAll()`. `Run` goroutines continue writing to the store; The
 SQLite connection (`Close()` in `sqlitestore.go:78`) is never closed. Fix: cancel
 runs, `wg.Wait()` bounded, close store.
- **a.ctx is written in startup and read in EmitFunc from unsynchronized goroutines**
 — `medium` / `concurrency` — `app.go:370`, `app.go:292-294`, `terminal.go:42`.
 Benign by temporal order, but shared field without barrier. Fix: protect with
 `a.mu`/atomic.
 - **FileDiff creates new file diff with filepath.Join(root, path) without validating
 traversal** — `medium` / `security` — `git.go:108-145`, exposed `git.go:189`.
 `os.ReadFile(filepath.Join(root, path))` without `Clean` or membership check for
 root. Fix: validate with `filepath.Rel` that is still under root.
- **loadDotEnv mutates the global environment with os.Setenv without validating keys** — `low` /
 `security` — `dotenv.go:51-62`, `main.go:17`. Inherits to all threads
 (git, pty, bash). `.env` in an untrusted dir may alter `PATH`. Fix: allowlist
 keys; reject `PATH`/`LD_*`/`DYLD_*`.
- **StartPty/ResizePty do not validate cols/rows (0 -> pty of size zero)** — `low` /
 `behavior` — `terminal.go:39-45`, `terminal.go:57-62`. Fix: normalize to 80x24 if <1.
- **Test gaps: SetWorkspace concurrent with ListCommands, shutdown, dotenv parser**
 — `medium` / `missing-test` — `app_commands_test.go`, `app_workspace_test.go`,
 `dotenv_test.go`. Fix: concurrent test `-race`; shutdown test; harden dotenv.
- **SetWorkspace cancels runs but does not wait for them before rewiring (TOCTOU benign
 documented)** — `info` / `concurrency` — `app.go:493-504`, `app.go:527-533`.
 Intentional (ponytail). No action required.

### Area 2 — Session runner / agent loop
Files: `internal/session/runner/{runner,run,turn,publish}.go`,
`internal/session/{session,epoch,mode,event}.go`.
Purpose: central shift loop; drains the inbox, builds each Request from the history
durable, calls the Provider, translates the stream to durable SessionEvents, seats tool
calls in parallel (errgroup), decides continuation/limit/abort/epoch/mode.
State: well structured, good coverage (including -race) on happy paths. Risks
in concurrent ordering and in the app<->loop interaction around the abort.

- **Invalid order between the assistant's Message (Step.Ended) and the Tool.Success of the
 turn** — `high` / `concurrency` — `turn.go:142-210`, `publish.go:62-90` and `169-187`.
 The Publisher lock serializes appends but does NOT impose order; a quick/local tool
 can persist your `Tool.Success` with `Seq` smaller than the assistant. The projection
 orders by `Seq` -> emits the tool result before the assistant with `tool_calls` ->
 API rejects (HTTP 400). The tests only affirm `Tool.Called < Tool.Success`, never
 `Step.Ended < Tool.Success`. Fix: persist the assistant before any
 Tool.Success of the turn; test that affirms the order with instant tool.
- **Run returns raw context.Canceled on each abort and the app shows it as an error**
 — `high` / `ux` — `turn.go:194-208`, `run.go:73-76`, `app.go:649-651`, `bus.go:32-37`,
 `chat.ts:389-392`. Deliberate cancellation presented as a ruling. Fix: treat
 `context.Canceled`/`DeadlineExceeded` as a clean shutdown (`errors.Is`).
- **start relaunches a new Run without waiting for the old one: two overlapping loops duplicate
 Tool.Failed** — `medium` / `concurrency` — `app.go:634-656`, `run.go:106-126`,
 `turn.go:200-208`. The old one keeps writing in cleanup (`WithoutCancel`); the new
 reads `PendingToolCalls` and can write a second `Tool.Failed` for the same callID
 -> two `Message{Role:tool}` -> API rejects. Fix: serialize runs per session;
 idempotent insertion by callID.
 - **Unsupervised settle goroutines if Publish fails midway (g.Wait is not called)**
 — `medium` / `concurrency` — `turn.go:154-156`, `turn.go:160-189`. `return false, err`
 without `g.Wait()`; Launched goroutines (bash sleep) follow and write post-return. Fix:
 cancel and drain the pool before returning.
- **PermissionGate non-ctx error leaves the tool unresolved and the turn continues** —
 `medium` / `error-handling` — `turn.go:170-177` and `194-208`. If `Ask` fails due to cause
 != cancellation, returns nil without setting; `Tool.Called` remains hanging and the loop chains
 another turn. Fix: publish `Tool.Failed` upon non-ctx gate failure.
- **The loop never uses DeliverySteer: the follow-up cancels instead of steer (steer is
 dead code)** — `medium` / `behavior` — `app.go:395-404`, `app.go:538-548`,
 `run.go:39,59,77,80-83`. Sending a second message aborts the current turn. Fix:
 decide contract (steer vs enqueue) and do not abort for new input.
- **The runTurn retry loop does not limit rebuilds or check ctx: spin risk with real epoch
 (M10)** — `info` / `maintainability` — `turn.go:56-67`, `epoch.go:17-22`,
 `memstore.go:168-176`. Today inert; With the real driver it could rotate. Fix: limit of
 retries + `ctx.Err()` to the top of the for.
- **failInterruptedTools swallows ErrSessionNotFound but the flow assumes projectable
 history** — `info` / `missing-test` — `run.go:106-126`, `run.go:38-64`. Arista
 fragile (force without input). Fix: cover the contract with test.

### Area 3 — LLM Provider (OpenAI-compatible)
Files: `internal/llm/{openai,provider,tool,fake,doc}.go`.
Purpose: border with the model via SSE OpenAI-compatible (OpenRouter); parse deltas
text/reasoning/tool_calls to bracketed `llm.Event`, assemble tool calls by index,
map history/roles/system, report bugs as `StepFailed`.
State: reasonably solid, well tested on happy path. Risks on edges that the
tests do not touch.

- **OpenAI client without timeout: a turn can hang indefinitely** — `high` /
 `concurrency` — `openai.go:43-49`. `[verificado]`. Without `WithRequestTimeout`; the
 title/commit callers use `context.Background()` (`app.go:452`, `git.go:167`).
 Fix: timeout per request and/or `context.WithTimeout` in those callers; read-timeout of
 stream.
- **Stream failure in the middle of tool call leaves ToolInput bracket open** — `medium` /
 `error-handling` — `openai.go:218-231`. If the stream fails between args,
 `StepFailed` and `return` are output without looping through `order`: the UI received `ToolInputStarted`/`Delta`
 but never `ToolInputEnded`/`ToolCall`. Fix: emit `ToolInputEnded` for bracket
 opened before `StepFailed`, or document closure in the UI; test with SSE cut.
- **Tool call whose first delta arrives without id produces empty CallID and never issues
 ToolInputStarted** — `medium` / `bug` — `openai.go:131-159`, `223-231`. Gate
 `tc.ID != ""`; some gateways send args before the id -> discarded deltas and
 `CallID` empty that breaks the round-trip. Fix: use the index as the bracket key, not
 the id.
 - **Silenced json.Unmarshal errors when mapping tools and reasoning schema** —
 `low` / `error-handling` — `openai.go:326-329`, `253-263`. Tool without parameters if the
 schema does not parse (`if err == nil`). Fix: log the schema failure.
- **The top-level 'reasoning' field is sent to EVERY endpoint, including pure OpenAI** —
 `low` / `behavior` — `openai.go:82-84`. OpenRouter's own hardcoded field; can
 give 400 on strict endpoints. Fix: conditional by flag/base URL.
- **API key is passed in clear without wording; risk of leak in SDK error logs** —
 `low` / `security` — `openai.go:43-49`, `app.go:357-363`. `StepFailed` dumps raw
 `err.Error()` to a durable log. Fix: sanitize/compose before persisting.
- **Test gap: cancellation of ctx mid-stream and missing usage** — `info` /
 `missing-test` — `openai_test.go`. Fix: httptest tests that cancel ctx, skip usage,
 cut SSE in tool args.

### Area 4 — File tools + hashline diff/patch
Files: `internal/tool/{read,write,edit,glob,grep,ripgrep,path}.go`,
`internal/tool/hashline/`.
Purpose: read/write/edit with path sandbox and hashline patch engine
anchored to `[ruta#HASH]` and line views (`Seen`).
Status: well thought out anti-drift design, but the applicator does not validate limits, the hash is
weak, there is race on `Seen`, and the writes are not atomic.

- **ApplyEdits does not validate ranges/anchors against size: content discarded on
 silent** — `high` / `bug` — `apply.go:44-75`, `patcher.go:100-103`. `[verificado]`.
 Insertion with anchor > len falls into `insertsAt[pos]` with pos>len and is never issued (empirical check.
: `INS.POST 99` over 3 lines is discarded without error). Fix: validate
 `Range.End <= len`, `1 <= Anchor <= len`; actionable error.
- **SWAP with End greater than the end of the file truncates the queue without warning** — `medium` / `bug`
 — `apply.go:28-36,67-75`. `SWAP 2.=999` over 3 lines -> `a\nX` (lost real
 lines). Fix: reject `Range.End > len`.
- **Data race over Snapshot.Seen: Patcher reads map without lock** — `high` /
 `concurrency` — `patcher.go:67,95` + `firstUnseenAnchoredLine:147-165`,
 `snapshot.go:72-105`, `turn.go:157-189`. `[verificado]`. `ByHash` returns the pointer
 to the live `*Snapshot`; iterates `snap.Seen` outside the mutex while `RecordSeenLines`
 writes `Seen` under the mutex -> `concurrent map read and map write` (panic). Reachable
 with `read+edit` or `grep+edit` from the same file in one turn. Fix: defensive copy of
 `Seen` or query via method with lock.
- **CRC32 anchor hash truncated to 16 bits: trivial collision (demonstrated)** — `medium` /
 `bug` — `hash.go:25-28`. 65536 values; real collision found in small sample. A pinned edit
 could be applied to the wrong content without triggering the save. Fix:
 expand to 32+ bits.
- **Non-atomic writes and without preserving the mode (looses executable bit; corruption
 upon mid-failure)** — `medium` / `bug` — `patcher.go:20-22,111`, `write.go:28-30,116`.
 `os.WriteFile` direct with fixed perm 0644. Fix: temporary + `os.Rename`; `os.Stat` to
 preserve `FileMode`.
- **read goes into unproductive loop if a single line exceeds MaxBytes** — `medium` /
 `behavior` — `read.go:173-191,149-163`. Returns `:from` pointing to the same line;
 the model never moves forward. Fix: emit the truncated line with notice or advance.
- **Overlapping Replace/Insert are applied without error and with surprising results** — `low`
 / `behavior` — `apply.go:26-62`. `SWAP 1.=2 +X` + `SWAP 2.=3 +Y` -> `X\nY\nd` without
 notice. Fix: detect overlap and return error.
- **grep searches inside hidden files (--hidden), exposing .env/credentials** —
 `low` / `security` — `ripgrep.go:218,222`. Diverges from glob (which respects `.gitignore`).
 Fix: consistent policy; respect `.gitignore` by default.
- **The ':' selector of the read starts with the LAST ':' and does not support routes with ':'** —
 `info` / `behavior` — `read.go:74-88`. Documented as limitation v1. Future fix:
 selector as a separate JSON parameter.
- **Snapshots per session grow without limit (without Invalidate or pruning)** — `info` /
 `performance` — `snapshot.go:28-57`, `snapshots.go:23-46`. Each version saves the entire
 file; slow leak. Fix: limit history by path; release the store when closing
 the session.

### Area 5 — Execution and external tools
Files: `internal/tool/{bash,bash_unix,bash_other,webfetch,skill,todo,present_plan,
echo,registry,output,snapshots}.go`, `internal/terminal/`, `terminal.go`.
Purpose: shell execution (group kill, secret scrub), webfetch distilled with
guard SSRF, skill loading, all, plan, tools registration with permissions, bounded
output, terminal pty.
Condition: overall solid; two real security holes (bash subagent without gate,
SSRF by redirect) and pty zombies.

- **Subagents run bash without the permission gate** — `high` / `security` —
 `subagent.go:164`, `app.go:162`, `builtins.go:24`. `[verificado]`. The child runner no
 receives `SetPermissionGate`; subagent "general" brings bash. Direct escalation path.
 Fix: propagate gate+needsApproval to the child runner; test that verifies
 `Tool.Permission.Requested`.
- **WebFetch: SSRF bypass via redirect and DNS rebinding** — `high` / `security` —
 `webfetch.go:51-58`, `94-101`, `137-159`. `[verificado]`. Client without `CheckRedirect`;
 `checkSSRF` only validates the initial host. `302 -> 169.254.169.254`/`127.0.0.1` passes.
 Fix: `CheckRedirect` to re-validate each hop, or `net.Dialer.Control` to validate the real IP
; test with `302` to metadata.
- **Terminal pty never reassumes shell (cmd.Wait absent): zombie leak** —
 `medium` / `concurrency` — `session.go:23-46,58-63`. `Close` makes `Kill`+`f.Close`
 but no one calls `cmd.Wait()`. Open/close tabs accumulate zombies. Fix: `go cmd.Wait()`
 after Kill or replay in the reading goroutine.
- **Scrub for secrets in bash depends on the name; keys without SECRET/TOKEN/PASSWORD/
 API_KEY are filtered** — `medium` / `security` — `bash.go:150-174`. `AWS_ACCESS_KEY_ID`,
 `DATABASE_URL`, `GH_PAT`, etc. they remain legible. Fix: send allowlist
 - **Uncapped bash output buffer during execution (OOM before bounding)** —
 `medium` / `performance` — `bash.go:102-114,179-188`. Unlimited `bytes.Buffer` until
 `Run` returns; post-hoc chap. Fix: bounded writer (ring buffer head+tail).
- **Double bash bounding and then OutputStore.Cap with different limits** — `low` /
 `maintainability` — `bash.go:179-188`, `registry.go:131`, `output.go:26-34`. The second
 slice is head-only by bytes and part UTF-8. Fix: unify; safe border cut of
 rune.
- **listSkillFiles emits ABSOLUTE paths from host to model** — `low` / `security` —
 `skill.go:99-115`. Filters filesystem structure (home, user). Fix: routes
 relative to base dir.
- **The pty terminal does not open in the selected workspace** — `low` / `behavior` —
 `session.go:24`, `terminal.go:39-45`. Without `cmd.Dir`; boots at launch cwd.
 Fix: pass the root workspace as `cmd.Dir`.
- **WebFetch uploads http->https silently; http-only URLs fail instead of warning** —
 `info` / `behavior` — `webfetch.go:117-131`. Intentional. Optional: clearer
 error message.

### Area 6 — Session persistence, permissions and event bus
Files: `internal/session/{sqlitestore,memstore,store,inbox,permission}.go`,
`internal/event/{bus,store}.go`.
Purpose: log of events per session (event-sourcing) on ​​SQLite/memory with
projections; inbox (queue/steer FIFO); gate ask-before-run; event bus that forwards to the
frontend via channel `session:<id>`.
Status: solid and well tested (shared contract, monotonic Seq). Gaps in concurrency/robustness edges


- **DeleteSession does not wait for the run in flight: a late append resurrects the session** —
 `high` / `concurrency` — `app.go:609-618`, `sqlitestore.go:432-449`, `app.go:646-655`.
 The subsequent INSERT re-creates the session (reappears in the sidebar with partial log). Fix:
 wait for end of run before deleting, or tombstone rejecting appends.
- **SQLite without busy_timeout or WAL: correct intra-process, fragile with other opener** —
 `medium` / `concurrency` — `sqlitestore.go:48-75`, `app.go:317-342`. A second
 instance opening `atenea.db` will fail with `SQLITE_BUSY` without retry. Fix: add
 `busy_timeout` and `journal_mode=WAL` to the DSN.
- **MemoryPermissionGate.Ask overwrites a previous Ask of the same (sessionID, callID) and
 filters its channel/goroutine** — `medium` / `concurrency` — `permission.go:56-81,87-98`.
 The second Ask steps on the channel of the first; the first one is left hanging. Fix: detect
 collision and eject/error the old one.
 - **Sessions() sorts by MAX(rowid), which SQLite reuses after DeleteSession** — `low` /
 `behavior` — `sqlitestore.go:224-271`. After deleting the rows with the highest rowid, a new
 session can receive recycled rowids and appear lower. Fix: sort by
 `MAX(seq)` + timestamp, or `AUTOINCREMENT`/`created_at`.
- **Bus.PublishError serializes err.Error() losing type (StepLimitExceededError)** —
 `low` / `ux` — `bus.go:32-37`, `app.go:649-651`. The UI cannot distinguish limit of
 steps from provider failure. Fix: structured payload `{code, message}`.
- **EmittingStore only serializes AppendEvent; projections without lock (correct but not
 tested with real SQLite)** — `info` / `concurrency` — `store.go:31-78`. Correct by
 `MaxOpenConns(1)`. Optional: concurrent test under -race.

### Area 7 — Subagents, agents, skills, commands and prompt
Files: `internal/session/subagent/subagent.go`, `internal/agent/`, `internal/skill/`,
`internal/command/command.go`, `internal/session/prompt/prompt.go`.
Purpose: discover and assemble agents/skills/commands; TaskTool raises
child subagents (isolated runner) with depth/concurrency cap; `prompt.Build` assembles the
system prompt.
Status: well designed and tested (correct recursion cap, deterministic duplicates,
isolated parsing errors). Serious risk: the gate does not spread to the child.

- **The subagent runs bash without the parent's permission gate (ask-before-run bypass)**
 — `high` / `security` — `subagent.go:164`, `app.go:149,159,162`. `[verificado]`. Same
 finding as Area 5 (2 agents found it). Fix: `r.SetPermissionGate(parentGate,
  parentNeedsApproval)` in child runner; inject the gate into `NewTaskTool`.
- **A custom subagent without a 'tools' field is left without any tools, in
 silence** — `medium` / `behavior` — `subagent.go:150-153`, `agent.go:58-64`. `Parse`
 does not apply default to Tools. Fix: reject/warn in Discover, or read-only default.
- **Skill/agent names with spaces break the derived slash-command** — `low` /
 `behavior` — `command.go:35-45,106-124`, `skill.go:73-78`. `parse` cuts the name at
 the first space -> uncallable command. Fix: validate/slugify names.
 - **Shared concurrency cap could deadlock if nesting 'task' is allowed via
 registry** — `low` / `concurrency` — `subagent.go:47,78-95,141-144`. Today for sure (child
 registry does not register `task`). Future fix: traffic light by level or free slot when waiting.
- **The subagent report is the last text of the wizard: it may come out empty** —
 `low` / `error-handling` — `subagent.go:179-185`. If the last turn was a tool-call,
 `report` remains empty with no signal. Fix: Explicit output when there is no text.
- **Frontmatter parse: body is cut off at the first '\n---'** — `info` / `behavior`
 — `skill.go:34-39`, `agent.go:37-42`. Fragile if the frontmatter contains `---`. Fix
 future: require `---` at the beginning of the line, or real YAML parser.
- **Failures when loading skills/agents are logged but swallowed** — `info` /
 `error-handling` — `skill.go:113-114`, `agent.go:101-102`, `app.go:122-124,136-137`.
 The author does not see Why is a skill missing? Fix: report to a diagnostic panel.

### Area 8 — Git integration (backend + frontend)
Files: `git.go`, `frontend/src/stores/git.ts`, `frontend/src/lib/diff.ts`,
`DiffScreen.vue`, `DiffView.vue`.
Purpose: git status (porcelain), unified diffs per file (staged/working/new),
commit with del message model, VSCode style side-by-side render.
Status: good core (separates staged/unstaged/untracked, sanitizes XSS with DOMPurify, without
repo it is not an error). Holes in non-trivial paths that git returns.

- **Renamed files are parsed with 'old -> new' and break the diff** — `high`
 / `bug` — `git.go:80`, `git.go:108-116`, `git.go:122-124`. `[verificado]`. `R orig ->
  renamed` leaves `Path` literal; the diff falls to `newFileDiff` with `os.ReadFile` from a non-existent path
. Fix: start with ` -> `, stay with the new path.
- **Paths with spaces, unicode or special characters arrive quoted/escaped** —
 `high` / `bug` — `git.go:64-93`, `git.go:108-124`. `[verificado]`. `--porcelain` quotes
 and escapes (octal) non-ASCII paths; the path with quotes does not match `git diff` nor
 `ReadFile`. Critic for projects in Spanish. Fix: `--porcelain -z` or
 `core.quotepath=false`, split by NUL.
- **Binary files: side-by-side remains empty or renders raw bytes as
 additions** — `medium` / `behavior` — `git.go:108-145`, `diff.ts:69-123`,
 `DiffScreen.vue:87-91`. Binary `git diff` does not include `@@`; `buildSideBySide` returns
 0 rows ("No changes"). Untracked dumps raw bytes as `+`. Fix: detect binary
 (NUL/utf8.Valid) and emit placeholder.
- **loadStatus does not clear error on success: a previous error is stuck on the panel** —
 `low` / `ux` — `git.ts:41-48`. Fix: `error.value=''` at the beginning of the try.
- **openDiff does not set the loading state; a slow FileDiff freezes the interaction** — `low`
 / `ux` — `git.ts:109-122`. Optional fix: flag `loadingDiff`, or limit the size of the diff.
 - **GitStatus/FileDiff not tested at the App level with concurrent SetWorkspace** — `info` /
 `missing-test` — `git.go:185-212`, `app.go:178-183`. Folder change between list and
 open -> empty diff/error. Optional fix: pass root next to the path.
- **The hunk regex in buildSideBySide tolerates the function suffix but it should be fixed**
 — `info` / `maintainability` — `diff.ts:97-99`. No bug. Optional: test with suffix of
 function after the second `@@`.

### Area 9 — Frontend state: chat and session store
Files: `frontend/src/stores/{chat,tabs,ui}.ts`, `frontend/src/lib/{sessions,
contextWindow,mcps,terminalSession}.ts`.
Purpose: translate durable Wails events (`session:<id>`) to a reactive log of items;
manage subscription, workspace persistence, sidebar, tabs, terminal registration
pty outside the tree components.
Condition: mature and very well tested. Remaining risks of concurrency between user
async actions.

- **Race in loadSession: async replay can contaminate the wrong session** —
 `high` / `concurrency` — `chat.ts:544-560`. No guard that `sessionID.value===id`
 after `await SessionHistory(id)`; double click A->B merge histories into the same
 `items.value`. Fix: `if (sessionID.value !== id) return` before replay; test async.
- **Optimistic state (running=true) can be stepped on by a late event of the previous turn
** — `medium` / `concurrency` — `chat.ts:562-578,349-366`. A late `Step.Ended`
 turns off `running` right after turning it on. Fix: map `running` to an id of
 shift/step.
 - **attach() async without unmount guard can boot/wire a pty after unmount**
 — `medium` / `concurrency` — `terminalSession.ts:45-62`, `TerminalPanel.vue:14-24`.
 `detach` runs while `attach` is still in flight. Fix: re-validate `container.isConnected`
 after the await, or flag `mounted`.
 - **mcpIcon constructs SVG with entry.accent/name without escaping: latent XSS if the catalog
 stops being static** — `low` / `security` — `mcps.ts:70-85`. Today static/reliable.
 Fix: when changing to dynamic, validate accent (hex) and XML-escape of the text.
- **Step.Failed/applyError does not turn off the streaming pointers: item remains 'streaming'
 forever** — `low` / `behavior` — `chat.ts:363-366,389-392,222-224`.
 cursor "typing" hanging; the next Delta is appended to the old item. Fix: close streaming
 in flight in `Step.Failed`.
- **loadSessions/loadWorkspace/loadModel last-resolver wins unguarded** — `low` /
 `concurrency` — `chat.ts:434-436,562-578,367-371`. Momentary flashing of the sidebar.
 Optional fix: version refreshes.
 - **send() does not capture binding errors: a rejection leaves running=true hanging** — `low`
 / `error-handling` — `chat.ts:562-578,593-601,605-615`. No try/catch; spinner spins
 forever. Fix: try/catch to replace `running=false` and `errorText`.
- **Persistent tabs re-create terminals but can remain attached to dead ptys** —
 `info` / `behavior` — `tabs.ts:13-67`, `terminalSession.ts:21-87`. Verify in Go that
 `StartPty(id)` after restart does not crash or leave orphan ptys.
- **Persistence: change of shape of 'workspace' in localStorage without migration/validation**
 — `info` / `maintainability` — `chat.ts:699-705`. Optional fix: validate that string
 is not empty before `SetWorkspace`.

### Area 10 — UI frontend: views and components
Files: `frontend/src/views/ChatView.vue`, `components/{ChatComposer,DevToolsPanel,
DevEventPanel,MessageList,ToolCall,MarkdownContent,AssistantMessage,AppSidebar,
WorkspacePicker,MentionMenu,CommandMenu}.vue`, `lib/{markdown,mention,command,reveal,
useSmoothText}.ts`.
Purpose: harness presentation (AI markdown with visual stream, tools, thinking,
plans, diffs), composer with @-mentions and /-commands, sidebar, workspace selector,
panel dev (git + terminal).
Status: mature and well tested; DOMPurify in the three points of v-html; menus with
`mousedown.prevent`. No obvious XSS or rendering blocking bugs.

- **AI markdown links can navigate the full webview (without target/rel or
 external opening)** — `medium` / `security` — `markdown.ts:19-22`,
 `MarkdownContent.vue:62`, `main.css:152`. A `<a>` model replaces the SPA (chat is lost); phishing surface in the webview. Fix: hook DOMPurify
 `afterSanitizeAttributes` with `target=_blank`+`rel=noopener noreferrer nofollow`, or
 delegate listener that opens via `BrowserOpenURL`; restrict `ALLOWED_URI_REGEXP` to
 http/https/mailto.
- **Vue vnodes leak (manual render) and setTimeout on copy buttons** —
 `low` / `performance` — `MarkdownContent.vue:20-22,35-38,41-55,58`. It is not called
 `render(null, btn)` when re-decorating; `setTimeout` touches orphaned nodes. Fix: unmount
 old icons or use static SVG; clear timeout in `onScopeDispose`.
- **tail of MessageList compares only status:output of last tool; input/
 diff/error changes do not trigger auto-scroll** — `low` / `ux` — `MessageList.vue:37-49`. The
 approval (pending) UI can be hidden from view. Fix: include `status:output:error:
  diff?length` in the signature.
- **Lost focus and no keyboard trap in the two-step deletion of the sidebar** —
 `low` / `ux` — `AppSidebar.vue:129-159`. Focus falls to the bodysuit; Escape does not cancel. Fix:
 focus commit(`nextTick`); `@keydown.escape`.
- **DevToolsPanel resize pointermove/pointerup listeners may hang
 if the panel is unmounted mid-dragging** — `low` / `concurrency` —
 `DevToolsPanel.vue:68-81`. Fix: `setPointerCapture` or cleanup in `onUnmounted`.
- **No test coverage of XSS/link behavior or copy leak** —
 `info` / `missing-test` — `markdown.test.ts:13-16`, `MarkdownContent.test.ts`. Fix:
 cases for `href=javascript:`, `on*` attributes, target/rel; test that changes `text`
 verifying that vnodes/timeouts do not accumulate.
- **DOMPurify is invoked separately in three components without a central policy** — `info`
 / `maintainability` — `markdown.ts:21`, `DiffView.vue:40`, `DiffScreen.vue:34-37`. Fix:
 centralize in a `sanitize()` helper with hooks/allowlist.

---

## 5. Transverse pattern (common root of several highs)

Several bugs originate from the same place: **the app cancels the old run but does not wait for it**
before booting/deleting/rewiring (`SetWorkspace`, `DeleteSession`, second
`SendPrompt`). From there they come: resurrected session, overlapping runs that duplicate `Tool.Failed`,
gate stepped on, and visible `context.Canceled`. **Serialize the runs per session** (one
goroutine per session draining the inbox, or wait for done before relaunching) closes several
at once.

---

## 6. Prioritization recommendation (without applying anything yet)

1. **Security first:** bash gate in subagents and `CheckRedirect`/SSRF in webfetch.
 These are bypasses of existing controls.
2. **Crash/hang:** defensive copy of `Snapshot.Seen` (panic) and timeouts in the
 provider/callers.
3. **Edits/data integrity:** validate ranges in `ApplyEdits`; wait for run
 before `DeleteSession`; extend the hash to 32 bits; atomic writes.
4. **Usable Git in Spanish:** `--porcelain -z` resolves renames and unicode together.
5. **High value/low cost polishing:** `ListCommands` with lock (one-liner);
 `context.Canceled` as clean lock; guard from `sessionID` to `loadSession`; external
 links in the markdown.
6. **Contract decision:** who does a follow-up while the agent works (steer vs
 enqueue vs abort) — today he aborts and loses work.

### Suggested test coverage (where green doesn't catch anything today)
- `go test -race ./...` in CI.
- Test that crosses `Apply` with `RecordSeenLines` on the same path under `-race`.
- `SetWorkspace` concurrent with `ListCommands`/`ListProjectFiles`.
- SSRF with `302 -> 169.254.169.254`.
- Subagent with bash that must trigger the gate.
- `gitStatus` with rename and with accented name.
- `loadSession('A')` + `loadSession('B')` async without intermediate await.
- OpenAI: cancellation of ctx in the middle of the stream; chunk without usage; SSE cut in tool args.

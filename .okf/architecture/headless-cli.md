---
updated_at: 2026-07-26
summary: The non-interactive command-line surface — the subcommand dispatch, `atenea run`, the three output formats over the durable event stream, the three permission modes a host with nobody to ask can honestly offer, the exit codes, the rule that keeps a run from ever waiting on stdin, and the refusal to answer from the offline demo.
---

# Headless CLI

> Status: implemented 2026-07-26 (audit recommendation R4.3).
> See the [agnosticism and extensibility audit](../audits/2026-07-24-agnostic-extensibility-audit.md) §1, §2 and §4 R4.
> Builds on the [composition root](composition-root.md) (R4.1, R4.2), the
> [tool capabilities](tool-capabilities.md) (R2) and the
> [shared headless agent service](../specs/2026-07-13-headless-agent-service-design.md).

This is the surface an integrator uses. Everything else in atenea is a UI; this is
the way in for a CI step, an editor plugin, another agent, or a test that has no
terminal. It is language-agnostic on purpose: the protocol is a process, its
arguments, its streams and its exit code.

## The surface

```
atenea                                     start the interactive terminal interface
atenea run -p PROMPT [flags]               run one non-interactive turn
... | atenea run [flags]                   the prompt on stdin instead
atenea version                             print the build metadata
atenea --version                           alias of the above
atenea help | -h | --help                  the command list
```

`atenea run`:

| Flag | Default | Meaning |
|---|---|---|
| `-p`, `--prompt` | — | the prompt; with no `-p` it is read from stdin instead |
| `--stdin` | off | also read stdin and append it to `-p` |
| `--output-format` | `text` | `text` \| `json` \| `stream-json` |
| `--permission-mode` | `deny` | `deny` \| `allowlist` \| `auto` |
| `--allow-effects` | — | with `allowlist`, the effects a tool may declare and still run |
| `--session` | a fresh `cli-…` id | run under this session id in the shared store |
| `--cwd` | the working directory | the workspace root the agent is anchored to |

Long flags take one dash or two; the help prints two, which is the spelling
everything here uses.

## What was missing, and what it is built on

`.okf/specs/2026-07-13-headless-agent-service-design.md` had already delivered a
*UI-independent* turn lifecycle: `internal/agent.Service` admits a prompt, starts a
run, replaces and cancels runs, and reports completion, without knowing what a UI
is. What did not exist was a **non-interactive entrypoint driving it** — the whole
of `cmd/atenea/main.go` was one string comparison for `--version` and otherwise the
TUI.

So this feature adds no agent behavior. A run is the **third caller of `host.New`**,
next to the desktop app and the terminal interface, and it composes the same pieces
in the same order:

```
cmd/atenea (main)
   └── internal/cli          argument surface, exit codes, serialization
         ├── host.New        outer root: dotenv, skills, root, store, providers, sitting
         ├── wiring.Build    inner root: tools, skills, subagents, runner
         └── Sitting.Agent   the turn lifecycle — unchanged
```

`internal/cli` is argument parsing, one policy choice and a serializer. If
something in it starts deciding what a turn does, it is in the wrong package.

### It asks for the same bootstrap as the interactive hosts

`host.Config{Dotenv: ".env", ExtractBuiltinSkills: true}` — the same values
`cmd/atenea` passes for the TUI. A headless run that discovered fewer skills than an
interactive one would be a difference between the hosts that nothing announces, and
`AGENTS.md` puts the burden the other way: a change to shared behavior has to hold
for both. In a release build `dotenv.Load` is compiled out, so the `.env` only ever
loads during development.

The one thing it does differently is the log. The TUI redirects the standard log to
`/tmp/atenea.log` because stderr would paint over Bubble Tea's alternate screen; a
headless run leaves it on stderr, because there is no screen to corrupt and a CI
job's diagnostics belong in its output.

## Output formats

### `text` — the default

The answer goes to **stdout**, streamed as the model produces it. The activity that
produced it goes to **stderr**, one line per tool call:

```
$ atenea run -p "what does wiring.Build assemble?"
· Read internal/wiring/wiring.go
· Grep Build\(
wiring.Build assembles the tools, the skills, the subagents and the runner…
```

That split is what makes the human format the pipeable one too:
`atenea run -p "…" > answer.md` keeps only the answer, `2>/dev/null` silences the
progress. A format has to be traded for a machine format to get structure, not to
get a redirect.

The activity line asks each tool how its call should read
([`tool.Presentation`](tool-capabilities.md)), so a tool atenea has never heard of —
one an MCP server contributed — is described by whatever it declares, and one that
declares nothing still reads honestly: its own name and the input the model sent.
Nothing here switches on a tool name.

Both strings on an activity line are text the model wrote, so both are stripped of
control characters before they reach a terminal. The answer on stdout is **not**:
that is the data the caller asked for, and rewriting it would corrupt a redirected
file.

### `stream-json` — the protocol

NDJSON: one durable `session.SessionEvent` per line, in `Seq` order, flushed per
line so a consumer reading line by line sees an event while the turn is still
running. Nothing but events reaches stdout.

```console
$ echo "Say hello." | atenea run --output-format stream-json
{"SessionID":"cli-1785102821650589500","Seq":1,"Kind":"Session.Cwd","Message":null,"Text":"/tmp/demo","CallID":"","ToolName":"","Input":null,"Usage":null,"Error":"","Diff":"","Compaction":null,"Checkpoint":null}
{"SessionID":"cli-1785102821650589500","Seq":2,"Kind":"","Message":{"ID":"msg-1","Role":"user","Text":"Say hello.","ToolCalls":null,"ToolCallID":"","IsError":false,"Seq":0},"Text":"","CallID":"","ToolName":"","Input":null,"Usage":null,"Error":"","Diff":"","Compaction":null,"Checkpoint":null}
{"SessionID":"cli-1785102821650589500","Seq":3,"Kind":"Step.Started","Message":null,"Text":"","CallID":"","ToolName":"","Input":null,"Usage":{"InputTokens":6608,"OutputTokens":0,"ReasoningTokens":0,"CacheReadTokens":0,"CacheWriteTokens":0,"CacheableInputTokens":0},"Error":"","Diff":"","Compaction":null,"Checkpoint":null}
{"SessionID":"cli-1785102821650589500","Seq":4,"Kind":"Text.Started","Message":null,"Text":"","CallID":"","ToolName":"","Input":null,"Usage":null,"Error":"","Diff":"","Compaction":null,"Checkpoint":null}
{"SessionID":"cli-1785102821650589500","Seq":5,"Kind":"Text.Delta","Message":null,"Text":"Hello from atenea.","CallID":"","ToolName":"","Input":null,"Usage":null,"Error":"","Diff":"","Compaction":null,"Checkpoint":null}
{"SessionID":"cli-1785102821650589500","Seq":6,"Kind":"Text.Ended","Message":null,"Text":"Hello from atenea.","CallID":"","ToolName":"","Input":null,"Usage":null,"Error":"","Diff":"","Compaction":null,"Checkpoint":null}
{"SessionID":"cli-1785102821650589500","Seq":7,"Kind":"Step.Ended","Message":{"ID":"msg-2","Role":"assistant","Text":"Hello from atenea.","ToolCalls":null,"ToolCallID":"","IsError":false,"Seq":0},"Text":"","CallID":"","ToolName":"","Input":null,"Usage":null,"Error":"","Diff":"","Compaction":null,"Checkpoint":null}
```

Three things about that shape are decisions rather than accidents:

- **The event taxonomy is not the CLI's.** It is
  [`agentcore/session.EventKind`](public-contracts.md), the same stream the desktop
  frontend and the TUI transcript already read. This format serializes it and
  invents nothing: no envelope, no second vocabulary, no per-format renaming. The
  set is open — a consumer switching on `Kind` needs a default branch, because an
  unknown kind means the producer is newer than the consumer — and events with no
  taxonomy carry `""` (the user message at `Seq 2` above).
- **The keys are the Go field names, and it is verbose because of that.** The
  desktop frontend consumes exactly these keys over Wails (`ev.Kind`,
  `ev.ToolName`, `ev.Input`), so tagging the struct to make this prettier would
  either break a shipped consumer or give one contract two spellings depending on
  which host emitted it. One serialization is worth more than snake_case and
  `omitempty`. Tightening it is a change to the published stream, which is R5's
  business, not this format's.
- **A subagent's events arrive too**, carrying the child session's id rather than
  the run's. `wiring` publishes them on the parent's channel on purpose so a UI can
  see what a subagent is doing; a consumer tells them apart by `SessionID`.

There is deliberately **no result line at the end**. Appending a non-`SessionEvent`
document would break "one event per line", which is the only promise the format
makes. The outcome is the exit code, and `--output-format json` is there for a
caller that wants it as data.

### `json` — one document at the end

```console
$ atenea run -p "the answer?" --output-format json
{"session_id":"cli-1785102843574215780","status":"ok","exit_code":0,"result":"42","tool_calls":0,"denied_tool_calls":0,"usage":{"input_tokens":0,"output_tokens":0,"reasoning_tokens":0,"cache_read_tokens":0,"cache_write_tokens":0,"cacheable_input_tokens":0}}
```

| Field | |
|---|---|
| `session_id` | the session the run used — pass it to `--session` to continue |
| `status` | `ok` \| `turn_failed` \| `permission_denied` \| `canceled` |
| `exit_code` | the process exit code, so a captured document still carries it |
| `result` | the assistant's answer: the coalesced message of the last step that closed |
| `tool_calls` / `denied_tool_calls` | how many calls were made, and how many the mode refused |
| `usage` | what the provider reported, summed over every step; zero for a provider that reports none |
| `error` | the cause when there is one; absent otherwise |

This document is the CLI's own format, which is why it is snake_case while the
events are not. Every field in it is a **fold of the same event stream** rather than
a separate account of the run, so it cannot contradict the events a consumer just
read — and when the stream reports a failure the run handle did not, the document
follows the stream.

The document describes a run that *started*. A startup failure (exit 5) reports on
stderr only, because there is no result to fold — which is also what distinguishes
it from a turn that failed, where there is.

One field cannot come from the stream: `denied_tool_calls`. A refusal reaches the
stream as a `Tool.Failed` carrying a message, so counting them there would mean
matching that message — the string coupling `internal/tui/transcript.go` already has
and that R5 exists to remove. The count is taken at the decision, in the policy
wrapper, and an exit code is not built on a matched string.

## Permission modes

Headless means there is nobody to answer `permission.Gate.Ask`. That is not a
missing feature, it is a different question. With a user present, "I cannot show
this call to be harmless" means *ask*; with no user, it means *refuse*, because the
only other option is running it on nobody's authority.

All three modes are derived from what each tool declares about its own effects
([R2](tool-capabilities.md)), never from a list of tool names.

| Mode | A tool declaring `NoEffects` | A tool declaring an effect | A tool declaring **nothing** |
|---|---|---|---|
| `deny` (default) | runs | refused | refused |
| `allowlist` | runs | runs if every declared effect is inside `--allow-effects` | refused |
| `auto` | runs | runs | runs |

### `deny`

The default, and it is not "refuse everything": the whole read-only half of the
catalog declares `tool.NoEffects` — `read`, `glob`, `grep`, `skill`, `todo_write`,
`present_plan`, `task` — and runs. An unattended investigation therefore works with
nothing granted.

A refused call fails as a tool failure and the model is **told**, as the result of
its own call, so it can adapt: report the change instead of making it, describe the
command instead of running it. The turn continues and ends normally; the exit code
is what makes the refusal visible to whatever invoked the run.

### `allowlist`

The operator states a budget of **effects**, not a list of tools:

```bash
atenea run -p "fix the failing test" \
  --permission-mode allowlist --allow-effects writes-files
```

A call runs when every effect its tool declares is inside the budget. `write` and
`edit` declare `writes-files` and run; `bash` declares `runs-commands` and does not;
`web_fetch` declares `reaches-network` and does not.

The vocabulary is `tool.Effects`, so the flag's accepted values are derived from the
same method that renders them (`writes-files`, `runs-commands`, `reaches-network`
today) and a flag added to that vocabulary is spellable on the commit that defines
it. Three properties follow, and they are why this is a budget rather than a name
list:

- **A tool joins by declaring, not by being named.** A tool atenea has never heard
  of is inside the budget the moment its author says what it does. Nothing in the
  host has to have heard of it — which is exactly what R2 bought and what a name
  list would have thrown away.
- **An unknown effect is outside every budget.** An operator can only spell the
  flags this binary knows, so a tool declaring one from a newer vocabulary falls out
  of the subset test. Adding a flag can only ever leave a run more careful.
- **Silence is refused.** A tool that declares nothing is denied *even with every
  effect allowed*. That is the security property that makes this mode worth having
  next to `auto`, and the sentence that separates them: **`allowlist` decides on
  evidence, `auto` decides in its absence.**

`--permission-mode allowlist` with no `--allow-effects` is a **usage error**, not
`deny` under a second name — a mode that silently behaved like another would make a
CI configuration that reads as permissive behave as strict, with nothing anywhere
saying so. `--allow-effects` outside `allowlist` is a usage error for the same
reason read from the other end: a flag that would be silently ignored is a flag that
lies about what the run allowed.

### `auto`

Every call runs unattended, including one whose tool declared nothing at all. It is
the dangerous one, and four things keep it from being reached by accident:

1. The default is `deny`. Saying nothing never gets you here.
2. The value must match exactly. No prefixes, no abbreviations, no case folding —
   `au`, `AUTO` and `all` are all usage errors that list the three real modes.
3. Every run in `auto` writes a warning to stderr before the first turn, naming what
   it does: *"permission-mode auto: every tool call runs unattended, including tools
   that declare nothing about what they do. Nothing in this run will ask."*
4. It is not a wider `allowlist`. A full budget still refuses the undeclared tool;
   this mode is a different type with a different answer, so the two are not
   neighbouring settings on one dial.

### The gate is not nil

Worth stating because it is the subtle way this could have been broken. The runner
consults its policy only when a gate is present, so a headless host that passed
`Gate: nil` would settle **every** call regardless of what its mode decided.
`permission.UnattendedGate` is installed instead: it answers immediately and the
answer is no. An `Ask` reaching it is a bug in whichever policy produced it, and the
safe response to a bug in a permission decision is to refuse — never to block, which
for a CI job is the one failure worse than a wrong answer.

No session grants are wired either. A grant is an answer a user gave, and there is
no user; `wiring` leaves the classification untouched for a nil grant store, which is
the honest description of a run that cannot be asked anything.

## Exit codes

| Code | Constant | Means |
|---|---|---|
| 0 | `ExitOK` | the turn finished and every tool call was permitted |
| 1 | `ExitTurnFailed` | the turn failed — the provider errored, the stream broke, a store write was refused |
| 2 | `ExitUsage` | the invocation was wrong: unknown subcommand or flag, unparseable value, no prompt, a `--cwd` that is not a directory |
| 3 | `ExitPermissionDenied` | the turn completed, but at least one call was refused by the permission mode |
| 4 | `ExitCanceled` | SIGINT or SIGTERM stopped the run |
| 5 | `ExitStartup` | the run never got as far as a turn: no model provider is configured, prompt admission failed, or stdin could not be read |

Two of them are worth arguing for.

**3 is its own code** because it is the one outcome a caller fixes by changing the
invocation rather than the prompt: the agent tried to do something this run was not
allowed to do. Folding it into 0 would hide it; folding it into 1 would misreport a
turn that ran to completion and reported honestly.

**4 is not 130.** atenea *handles* the signal — it stops the turn, flushes the
stream and returns — rather than dying from it, and a consumer wants to know "the
run was interrupted", which is one fact, not which of two signals delivered it
(130 for SIGINT, 143 for SIGTERM).

Several of these can be true of one run, and the precedence is fixed:

```
canceled  >  turn failed  >  permission denied  >  ok
```

Cancellation outranks everything the run produced, because an interrupted run makes
no claim at all about the conversation. A turn failure outranks a denial for the
reverse reason: a denial is a report the run made deliberately, and a broken turn
means there is no report to trust.

## The prompt: `-p`, stdin, or both

```bash
atenea run -p "explain internal/wiring"                    # the flag alone
cat prompt.txt | atenea run                                # stdin alone
git diff | atenea run -p "review this diff" --stdin        # both
```

**One rule: stdin is read only when the invocation asked for it** — by giving no
`-p`, so stdin is the only source there is, or by saying `--stdin`.

| `-p` | `--stdin` | stdin | result |
|---|---|---|---|
| yes | no | anything | the flag alone; stdin is never touched |
| no | no | pipe or file | the piped text |
| no | no | terminal | usage error |
| yes | yes | pipe or file | `-p`, a blank line, then the piped text |
| any | yes | terminal | usage error |

The rule is that shape because **"not a terminal" is not the same as "will reach
EOF"**. Reading stdin whenever it is not a terminal looks equivalent and hangs
forever on an open pipe nobody closes — which is the *normal* state of stdin under a
CI runner, `ssh` without `-n`, `docker run -i`, or a wrapper script. The worst case
is a true deadlock in one of the integrations this feature exists for: an editor
plugin spawning the process with `stdin=PIPE, stdout=PIPE` waits for the answer while
atenea waits for the EOF the plugin will only send once it has one. A prompt given
with `-p` is complete on its own, so nothing justifies making it depend on the other
end of a pipe.

**When both sources are used, both are kept**, `-p` first and the piped text after a
blank line. `git diff | atenea run -p "review this diff" --stdin` is the shape of
most integrations and it only works if neither input is dropped — and dropping one
would be the worst kind of failure: the run would succeed, answering a question the
caller did not ask, with nothing anywhere saying that half the input was discarded.
Making it explicit costs one flag and buys the guarantee above.

**A terminal on stdin is never read**, whoever asked. With no `-p` that is a usage
error and not a wait; with `--stdin` it is a usage error too, because there is
nothing on the other end to wait for. A job that blocks forever on an input nobody is
going to type is the one outcome a pipeline cannot recover from. "Terminal" is asked
of the terminal driver, not guessed from the file type: `/dev/null` is a character
device and is not a terminal, so `atenea run < /dev/null` — the standard way a CI
script guarantees a command cannot block — reads the empty device and reports `no
prompt: stdin was empty` rather than being refused as interactive.

A positional argument (`atenea run "do the thing"`) is a usage error that names
`-p`. Accepting it would add a third place a prompt can come from and a third
precedence rule.

## Sessions

`--session ID` runs under that id in the store both hosts already write to, which is
what makes a headless run able to continue a conversation the desktop app started —
and a CI job able to hold one across steps:

```bash
atenea run --session "ci-$GITHUB_RUN_ID" -p "read the failing test and explain it"
atenea run --session "ci-$GITHUB_RUN_ID" -p "now propose a fix" \
  --permission-mode allowlist --allow-effects writes-files
```

It is **use-or-create**, not resume-or-fail: an id the store does not have starts a
conversation under it. That is what supports caller-chosen ids like the one above,
which is worth more than catching a typo — a typo starts a fresh conversation rather
than failing, so name ids deliberately.

With no `--session` the run mints `cli-<nanos>`, following the terminal UI's `tui-`.
The prefix says which host created the session: the TUI's `/resume` lists only
`tui-` sessions, so a headless session does not appear there, while the desktop
sidebar reads the store without filtering and does show it, grouped under the
workspace the run stamped on it.

`--cwd DIR` becomes `host.Config.Root`: it anchors the file and exec tools, skill
and subagent discovery, and the system prompt. A path that is not a directory is
refused before anything is assembled, because everything downstream of a bad root
degrades quietly instead — no skills discovered, file tools resolving against
nothing — and the run would produce a plausible answer about the wrong directory.

## Cancellation

The first SIGINT or SIGTERM stops the run and waits for it. The runner unwinds on a
cancelled context — it fails the tool calls still in flight and closes the turn — so
the events already written stay consistent and the stream ends where the run did. A
second signal abandons that wait, because an operator pressing Ctrl-C twice is asking
to stop now, and the store is durable either way.

After the closing document the stream drops anything the abandoned runner still
writes. That is the guarantee a consumer needs: an interrupted stream is truncated at
a line boundary, never interleaved with the result.

The handler is installed per run rather than process-wide, so the interactive
interface — which reads Ctrl-C itself — does not have it taken away.

## No provider is a refusal

With no credential anywhere — nothing in the environment, nothing in
`credentials.json`, no stored selection — the host lands on the offline demo
provider, whose replies are canned (see
[composition root](composition-root.md#the-offline-demo-provider)). `atenea run`
**refuses to start** on it: exit 5, nothing on stdout, and an explanation on stderr
naming the environment variables that would fix it, read off the shipped catalog so
the advice cannot go stale.

```console
$ atenea run -p "explain this repo"
atenea run: no model provider is configured, so there is nothing to answer this prompt.
  Export one of ANTHROPIC_API_KEY, OPENAI_API_KEY, OPENROUTER_API_KEY, OPENCODE_API_KEY, or run `atenea` and use /connect.
$ echo $?
5
```

Interactively the demo is right: a person sees the notice and runs `/connect`, and
the terminal interface keeps that behaviour untouched. Headless there is nobody to
read a warning, and the failure mode is the one this codebase has refused three
times on the record — R2 on flattening "said nothing" into "declared nothing", R3.5
on ignoring a stored credential, R3.6 on a dropped content part. A run that answered
from the fake would put a fabricated answer on stdout and exit 0, so a job whose key
merely expired would look exactly like one that worked, and the normal way to consume
a CLI — pipe stdout, ignore stderr — is precisely the way that would never notice.

There is deliberately **no flag to allow it**. Nothing needs one: the tests inject a
provider through `host.Config.Providers` rather than depending on the fallback, and a
demonstration can point `providers.json` at any endpoint. A flag would be surface
whose only purpose is to re-enable a wrong answer.

## Adding a subcommand

The dispatch is a table and a `func(Env, []string) int`. `atenea mcp` and
`atenea skill` (R4.4) are one struct literal and one file each:

```go
var commands = []command{
    {name: "run",     summary: "…", run: runCommand},
    {name: "version", summary: "…", run: versionCommand},
}
```

The top-level help is generated from that table, so a command cannot be added
without appearing in it, and a test asserts it. Per R4.4 there is no CLI framework:
one `flag.FlagSet` per subcommand with `ContinueOnError`, which also puts the
stdlib's own usage exit code and `ExitUsage` on the same number instead of having to
intercept one to renumber the other.

## Testability

Nothing in `internal/cli` reads `os.Args`, `os.Stdout`, `os.Stdin` or installs a
process-wide signal handler: it all arrives in `Env`, and `Main` returns the exit
code instead of calling `os.Exit`. `Env.Host` is the same kind of seam
`host.Config.Store` and `host.Config.Providers` are — not a hook for production
behaviour, but how a test drives the real assembly without the real filesystem.

So the tests run whole invocations through the real dispatch, the real host and the
real wiring, with only the model and the database replaced: NDJSON parsed back into
`session.SessionEvent`, the exit codes, the prompt precedence, each permission mode
gating for real (asserting the file was or was not written), streaming asserted by
reading a line off a pipe while the turn is held open, and cancellation by pushing a
signal onto `Env.Interrupts`. No PTY, no subprocess, which is the point of the
feature applied to itself.

## Related

- [Composition root](composition-root.md) — `host.New` and `wiring.Config`, the two
  layers a run assembles.
- [Tool capabilities](tool-capabilities.md) — `tool.Effects` and
  `tool.Presentation`, which the permission modes and the `text` format are derived
  from.
- [Published contracts](public-contracts.md) — `agentcore/session.SessionEvent`, the
  contract `stream-json` serializes.
- [Shared headless agent service](../specs/2026-07-13-headless-agent-service-design.md)
  — the turn lifecycle this drives.
- [CLI distribution](distribution.md) — the binary this ships in.

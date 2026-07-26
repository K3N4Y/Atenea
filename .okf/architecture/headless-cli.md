---
updated_at: 2026-07-26
summary: The non-interactive command-line surface — the two-level subcommand dispatch, `atenea run`, the three output formats over the durable event stream, the three permission modes a host with nobody to ask can honestly offer, the exit codes, the rule that keeps a run from ever waiting on stdin, the refusal to answer from the offline demo, and the configuration commands `atenea mcp` and `atenea skill`, which need no provider at all.
---

# Headless CLI

> Status: implemented 2026-07-26 (audit recommendations R4.3 and R4.4).
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
atenea mcp list                            every declared MCP server, and where from
atenea mcp add NAME -- COMMAND [ARG...]    declare a stdio server globally
atenea mcp remove NAME                     delete one from the global config
atenea skill list                          every SKILL.md discovery walked
atenea skill validate [PATH...]            report the skills that will not load
atenea version                             print the build metadata
atenea --version                           alias of the above
atenea help | -h | --help                  the command list
```

`run` is the agent. `mcp` and `skill` are the two configuration surfaces it reads
at startup, and they are commands for the same reason the run is: everything a
person can set up through a UI, a pipeline has to be able to set up without one.
**Neither needs a model provider** — see [Configuring without a
provider](#configuring-without-a-provider).

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
| 0 | `ExitOK` | the command did what it was asked; for a run, the turn finished and every tool call was permitted |
| 1 | `ExitFailure` | the command failed at what it exists to do — for `run`, the turn failed (the provider errored, the stream broke, a store write was refused); for `mcp add`, the server could not be declared; for `skill validate`, a skill will not load |
| 2 | `ExitUsage` | the invocation was wrong: unknown command or verb or flag, unparseable value, no prompt, a `--cwd` or a path argument that is not there |
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

## `atenea mcp` — the servers, from a script

An MCP server used to be declarable in two ways: the desktop Settings panel, or a
text editor. A CI image that wants a server, a dotfiles repo that provisions a
machine, and an agent configuring another agent all had the editor and nothing
else.

```console
$ atenea mcp add playwright -- npx @playwright/mcp@latest
declared "playwright" in /home/k/.config/atenea/mcp.json

$ atenea mcp add --env GITHUB_TOKEN=$TOKEN github -- npx github-mcp
declared "github" in /home/k/.config/atenea/mcp.json
  env: GITHUB_TOKEN

$ atenea mcp list
NAME        SCOPE   COMMAND
github      global  npx github-mcp
playwright  global  npx @playwright/mcp@latest
```

`add` writes the **global** config, which is the one the desktop panel writes and
both hosts read; the workspace `.mcp.json` belongs to the project and is committed
with it, so a command run on one machine has no business editing it. Flags go
before `NAME`, and everything after it (optionally after `--`) is the server's
command line, exactly as `docker run` and every `mcp add` in this class of tool
spell it.

### What it prints, and what it does not

**There is no connected column.** Connection state is a property of a *running
host* — the servers are subprocesses the desktop app or the TUI owns — and this
process connects to nothing. A column that read `false` on every row of every
listing would be worse than absent, because a reader would take it for a report
about their servers rather than about this process. `atenea mcp list` answers what
is *declarable and true from disk*; the TUI's `/mcp` picker and the desktop panel
answer what is running.

**A shadowed declaration is listed.** The workspace file overrides a global entry
of the same name, and `mcpclient.LoadConfig` — what a host actually connects —
drops the loser. That makes the listing the only place in the product where the
override is visible, which is the whole answer to "why is this server running a
command I did not configure":

```console
$ atenea mcp list
NAME    SCOPE              COMMAND
github  workspace          docker run ghcr.io/github/github-mcp-server
github  global (shadowed)  npx github-mcp
```

`Declarations(root)` is now the one reader of both files and `LoadConfig` is
defined as *its list minus the shadowed entries*, so what a host connects and what
this prints cannot drift apart.

**An env value is never echoed**, on `add` or on `list`. An `env` entry is where a
server's token lives; the file is written `0600` for that reason, and a terminal is
recorded in more places than that file is. The confirmation names the keys.

### The two refusals

```console
$ atenea mcp remove github          # declared in the workspace file
atenea mcp remove: MCP "github" is declared in /repo/.mcp.json; edit that file to remove it
$ echo $?
1

$ atenea mcp add github -- npx other
atenea mcp add: MCP "github" is already declared in /home/k/.config/atenea/mcp.json
  Remove it first with `atenea mcp remove github`, or edit that file.
$ echo $?
1
```

The first is the case the desktop already handled, and it is now handled *in one
place*: `mcpclient.RemoveGlobalConfig(root, name)` reads the workspace file only to
explain the removal it cannot perform, and `App.RemoveMCPConfig` lost its
hand-rolled copy of that error. One sentence, both hosts.

The second exists because the config is a JSON map: an upsert would succeed and
take whatever was there with it, including an `env` carrying a token nothing else
has a copy of. A refusal is undone with one command; an overwrite is not undone at
all. A name the *workspace* declares is refused for a different reason — the global
entry would be written and immediately overridden, so the add would report success
and change nothing any host reads.

One more line exists so a removal cannot be misread. Deleting the global half of a
shadowed name leaves the server the caller was just looking at exactly where it
was:

```console
$ atenea mcp remove github
removed "github" from /home/k/.config/atenea/mcp.json
  MCP "github" is still declared in /repo/.mcp.json; edit that file to remove it too.
```

## `atenea skill` — what was discovered, and what was not

Skill discovery is deliberately forgiving: `skill.Discover` skips a `SKILL.md` it
cannot parse so that one broken skill cannot take the others down. The cost is
paid by whoever wrote the broken one — it simply never appears, and nothing
anywhere says why. These two verbs are the other half of that trade, and R9.3 is
where the audit asks for them: *a contributor gets a real error instead of silent
non-discovery*.

```console
$ atenea skill list
NAME             STATUS   DESCRIPTION                                             LOCATION
-                invalid  the frontmatter declares no 'name'                      /repo/.atenea/skills/broken/SKILL.md
dup              active   The project's own copy.                                 /repo/.atenea/skills/dup/SKILL.md
dup              shadowed The one that loses.                                     /repo/.agents/skills/dup/SKILL.md
quiet            invalid  (no description: never announced to the model)          /repo/.agents/skills/quiet/SKILL.md
ponytail         active   Forces the laziest solution that actually works, sim…   /home/k/.atenea/skills/ponytail/SKILL.md
```

Every `SKILL.md` the walk found is a row, sorted by name with the winner of a name
before the entries it shadows — so the two rows that explain a precedence question
are adjacent, which walk order would not have given (the shadowed copy lives in
another directory, pages away). `STATUS` is one of:

| | |
|---|---|
| `active` | discovered and announced to the model |
| `shadowed` | a same-named skill earlier in the search order won; `LOCATION` is the file that **lost** |
| `invalid` | `validate` fails on it: it does not parse, or it declares no description |

The search order is `wiring.DefaultSkillDirs(root)` — the same ordered list the
agent is built with, not a second copy of it — and the built-in skills are
materialized first through the same `host.ExtractBuiltinSkills` every entrypoint
calls, so the listing cannot show fewer skills than a run would have.

### `validate` is the verb that fails

```console
$ atenea skill validate
/repo/.atenea/skills/broken/SKILL.md: the frontmatter declares no 'name'
/repo/.agents/skills/quiet/SKILL.md: skill "quiet" declares no 'description', so it is never announced to the model
2 problems in 8 SKILL.md files under 6 discovery directories
$ echo $?
1

$ atenea skill validate
ok: 7 skills under 6 discovery directories, no problems
$ echo $?
0
```

Four decisions in that output:

- **It validates the discovery set by default, and named paths when given some.**
  Both, because they are different questions. "Why does my skill not show up" is a
  question about the set this workspace actually walks and about nothing else;
  "is this file right" is asked *before* the skill is anywhere near a discovery
  directory — a draft, or a repository checking its own skills in CI before anyone
  installs them. Neither answers the other, so `atenea skill validate ./skills/mine`
  walks what you name (a file whatever it is called, a directory for the `SKILL.md`
  under it) and the bare form walks what a run would.
- **A skill with no description is a problem, not a remark.** It parses, so
  discovery keeps it and the `skill` tool can load it by name — but `skill.Format`
  puts only described skills in the system prompt, so the model is never told the
  name it would have to ask for. That is the same silent non-discovery one step
  later. The rule lives in `Info.Announced()` so the prompt and the validator apply
  one rule rather than two that can drift.
- **Findings go to stderr and stdout stays empty**, the way `go vet` reports. The
  exit code is the answer and the detail is the explanation, so
  `atenea skill validate && deploy` reads correctly and nothing has to be parsed.
- **Nothing to validate is a failure.** A contributor who points this at the wrong
  path and is told `ok` ships the skill broken. Zero files checked is not a pass.

`skill.Scan` is what makes any of this possible: it reports *every* `SKILL.md` the
walk found, with the parse error or the shadowing that befell it, and `Discover` is
now defined as that list minus the unusable and the shadowed. The two cannot
describe different sets of files, which is the property the old arrangement — a
`return nil` inside the walk — could not have.

This is the R4 half of R9.3. The rest of R9 is untouched: no `version` field in the
manifest, no unified frontmatter parser, no `atenea agent validate`, no decision on
`skills-lock.json`.

## Configuring without a provider

`atenea mcp` and `atenea skill` **must work with no API key anywhere**, and that is
a tested property rather than an accident:

```console
$ env -i PATH=$PATH HOME=$(mktemp -d) atenea mcp list
atenea mcp list: no MCP server is declared.
  workspace: /repo/.mcp.json
  global:    /tmp/tmp.XXXX/.config/atenea/mcp.json
  Declare one with `atenea mcp add NAME -- COMMAND [ARG...]`.
$ echo $?
0
```

Neither command calls `host.New`. They read `mcpclient` and `skill` directly, plus
`wiring.DefaultSkillDirs` and `host.ExtractBuiltinSkills`, and they open no session
store, resolve no credential and start no server. The one thing they share with a
run is `--cwd`, which means the same thing everywhere in this CLI: the workspace
root, resolved the way `host.New` resolves an empty one, so `atenea mcp list` and
`atenea run` read the same `.mcp.json`.

The test says it out loud: the harness passes an `Env.Host` that **fails the test if
anything calls it**. Getting this wrong would not have looked like a decision — it
would have arrived through a shared setup helper, and `atenea mcp list` would refuse
to list a config file because no model was configured.

## Exit codes across the surface

The six codes are one vocabulary, and the subcommands did not add to it:

- **1 is the generic failure**, not "the turn failed". It always was — the
  interactive interface failing to start has returned it since before the dispatch
  existed — so the constant is `ExitFailure`. For `run` it is the turn; for
  `mcp add` a server that could not be declared; for `skill validate` a skill that
  will not load, which is also what every linter returns when it has findings.
- **2 stays the invocation being wrong**, and a validation failure is deliberately
  not one. `atenea skill validate` exiting 2 on a malformed skill would say the
  command was called wrong when it was called exactly right and answered. What *is*
  a usage error: an unknown verb, a `--cwd` that is not a directory, a path argument
  that does not exist, `mcp add` with no command, and a flag written after `NAME`.
- **A new code was considered and rejected.** A distinct "validation failed" would
  have to name something a caller reacts to differently, and nothing does: a script
  either proceeds or it does not, and the two branches it *would* want — "you called
  me wrong" and "your environment is wrong" — are already 2 and 5.

## Adding a subcommand

The dispatch is a table and a `func(Env, []string) int`. A command is one struct
literal and one file:

```go
var commands = []command{
    {name: "run",     summary: "…", run: runCommand},
    {name: "mcp",     summary: "…", run: mcpCommand},
    {name: "skill",   summary: "…", run: skillCommand},
    {name: "version", summary: "…", run: versionCommand},
}
```

A command with sub-verbs is the same table one level down: `mcpCommand` is a call to
`verbs(env, "atenea mcp", blurb, mcpCommands, args)`, and that helper is the top
level's own logic — the generated list, the `unknown command "lst"` error, `-h` on
stdout and a mistake's help on stderr, `ExitUsage`. A group whose mistakes read
worse than the top level's would be two dispatches wearing one name.

`atenea mcp` with no verb is a **usage error**, not a default verb: choosing `list`
for the user would make the command mean something nobody typed. (A bare `atenea` is
the interactive interface because that is what it has always meant, not because the
dispatch picked a favourite.)

Both help screens are generated from the table they dispatch on, so a command
cannot be added without appearing in its own help, and a test asserts it at both
levels. Per R4.4 there is **no CLI framework**: one `flag.FlagSet` per command with
`ContinueOnError` — which also puts the stdlib's own usage exit code and `ExitUsage`
on the same number instead of having to intercept one to renumber the other — and
`go.mod` is unchanged by the whole of R4.4.

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

The configuration commands are driven the same way, against the real files: a
config `mcp add` wrote is read back by `mcpclient.LoadConfig` — the function every
host starts a server from, so a CLI that wrote a file only the CLI could read would
fail — and `skill validate` runs against genuinely malformed `SKILL.md` fixtures
with the exit code asserted. Two things the harness asserts by construction rather
than by an assertion: `Env.Host` fails the test if a configuration command
assembles a host, and `Env.Stdin` is an `os.Pipe` whose write end is never closed,
so a command that read stdin would hang instead of passing — the shape of the
deadlock R4.3 shipped in review, which these commands share an entrypoint with.

## Related

- [Composition root](composition-root.md) — `host.New` and `wiring.Config`, the two
  layers a run assembles.
- [Tool capabilities](tool-capabilities.md) — `tool.Effects` and
  `tool.Presentation`, which the permission modes and the `text` format are derived
  from.
- [Published contracts](public-contracts.md) — `agentcore/session.SessionEvent`, the
  contract `stream-json` serializes.
- [MCP servers](mcp.md) — the two config files `atenea mcp` reads and writes, and
  what connecting one means.
- [Shared headless agent service](../specs/2026-07-13-headless-agent-service-design.md)
  — the turn lifecycle this drives.
- [CLI distribution](distribution.md) — the binary this ships in.

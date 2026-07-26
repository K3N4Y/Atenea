---
updated_at: 2026-07-26
summary: The content-part seam on llm.Message — why the Text field was replaced rather than joined, why Part is a discriminated struct, and what an adapter owes content it cannot put on the wire.
---

# Message content

> Status: implemented 2026-07-26 (audit recommendation R3.6).
> See the [agnosticism and extensibility audit](../audits/2026-07-24-agnostic-extensibility-audit.md) §3.2 and §4 R3.
> Completes R3, on top of the [provider registry](provider-registry.md),
> [capabilities](provider-capabilities.md), [catalog](provider-catalog.md),
> [Wails surface](wails-provider.md) and [credentials](provider-credentials.md).

## The problem it solves

`llm.Message` said what a message contained with one field:

```go
// agentcore/llm — before
type Message struct {
	Role       string
	Text       string
	ToolCalls  []ToolCallPart
	ToolCallID string
	IsError    bool
}
```

An image, a PDF, an audio clip had nowhere to live. That is not urgent today —
nothing in atenea produces one — and that is precisely why it was worth doing
now. `Message` is a **published contract**: a third party writes an adapter
against it, and a host reads it. Adding a field to it later is a breaking change
to code that is not in this repository, so the cheapest moment to have the seam
is the moment before anyone needs it.

`Capabilities.Vision` has been declaring `false` for every adapter since R3.2,
with a doc comment naming this seam as the thing it was waiting for. That comment
is now correct in a different way: the seam exists, and the flag is what an
adapter flips when it can actually carry the content.

## The shape

```go
// agentcore/llm
type Message struct {
	Role       string
	Parts      []Part         // the message's content, in order
	ToolCalls  []ToolCallPart // role=assistant
	ToolCallID string         // role=tool
	IsError    bool           // role=tool
}

type Part struct {
	Kind PartKind
	Text string // TextPart
}

type PartKind int

const TextPart PartKind = iota

func TextMessage(role, text string) Message
func (m Message) TextOnly() (string, error)

type UnsupportedPartError struct{ Kind PartKind }
```

Adding an image later is a constant and the fields it gives meaning to. Nothing
existing changes shape, which is what "additive" has to mean for a type a third
party both reads and writes.

## Five decisions worth recording

### `Text` was replaced, not joined

Keeping `Text string` beside `Parts []Part` would have been the cheap migration —
no call site moves, no test rewrites. It was rejected, because it creates two ways
to say the same thing and therefore a question every adapter has to answer on its
own: *what does a message with both mean?* One adapter concatenates, another
prefers `Parts`, a third ignores whichever is empty, and the three disagree about
the same history.

This codebase has refused that shape twice already for the same reason: R2
recorded that "said nothing" and "declared `NoEffects`" must never be flattened,
and R3.2 recorded that `SwitchableProvider` must not implement `Describing`
because answering for a silent delegate invents a declaration. Both are the same
rule — **one question, one place that answers it** — and a `Text` field beside
`Parts` breaks it in the most load-bearing type of the provider contract.

`AGENTS.md` says to give little weight to development cost, and the cost was
real but bounded: five production call sites and about thirty test literals, all
of them found by the compiler because removing a field cannot fail silently.
`agentcore/` carries no stability promise yet, which is exactly the window in
which this is a rewrite rather than a break.

### `Part` is a discriminated struct, not a sealed interface

A sealed interface (`type Part interface { isPart() }` with `TextPart` and
`ImagePart` implementing it) makes illegal states unrepresentable: there is no
image part without an image. That is a real advantage and it lost anyway, on
three counts:

- **It is not the idiom of this contract.** `llm.Event`, `session.SessionEvent`
  and `tool.Presentation` are all `{Kind, plus the fields that kind gives meaning
  to}`. A fourth spelling in the same package makes the contract harder to read
  than any one of the four is on its own.
- **Sealing closes the type to the people the contract is for.** A host that
  decoded a part from JSON, or a test that wants a part of a kind this build does
  not define, cannot construct one. The `llmtest` clause below depends on being
  able to build exactly that.
- **Growth costs an exported type per kind.** With a struct, an image is one
  constant and the fields it needs; with an interface it is a new public type,
  and every reader's type switch has to gain a `default` anyway for the build
  skew a published contract has to survive.

The price is honest: a `TextPart` can carry an image's bytes in a field that does
not belong to it, and nothing stops it. That is the same price `Event` already
pays, and the same discipline pays it — `Kind` decides which fields are relevant,
the rest stay zero.

### Tool calls are not parts

`ToolCallPart` is named "part" and is not one. It stays in its own field, because
a tool call is not content the model produced for a reader: it is a request the
host answers by id, it is typed rather than free-form, and OpenAI's wire format
keeps `tool_calls` beside `content` anyway — folding them into `Parts` would only
mean taking them apart again in the adapter that has to split them.

### `TextOnly` returns an error, and that is the enforcement

Every shipped adapter carries text and nothing else. The dangerous failure is not
the adapter that refuses an image; it is the adapter that writes

```go
default: // a kind we do not know
    continue
```

and streams a turn as if nothing happened. The model is then asked about an image
it never received, the answer is wrong for a reason nothing recorded — not the
stream, not the durable history, not the user's screen — and the drop is
invisible from every side.

So the contract gives an adapter no way to obtain a message's text without also
being handed the reason the text is not the whole of it:

```go
text, err := message.TextOnly()
if err != nil {
	return nil, fmt.Errorf("anthropic: %w", err)
}
```

That is the same `(value, answered)` discipline R2 and R3.2 used for optional
capabilities, in its error-shaped form: the second return value is not optional
to receive, only to ignore, and ignoring it is a line a reviewer can see.

**Where the refusal happens.** Both shipped adapters translate the history before
they open a turn, so both fail from `Stream` itself — no channel, nothing to
close, no token spent, and the bracketing rule is satisfied trivially because
there is no turn. `Provider.Stream` has always allowed this (Anthropic already
refused an unknown role that way). An adapter that only finds out mid-stream may
instead close the turn with `StepFailed` carrying the cause in `Err`; the
`llmtest` clause accepts both, because they cost a host the same.

**Why the error type is published.** `*UnsupportedPartError` lives in
`agentcore/llm`, not inside each adapter, because it is the one turn failure a
host can act on without knowing which adapter it is talking to. The model is
fine, the credentials are fine, the network is fine — the content is what cannot
travel, and the recovery is to offer a model that reads it. `errors.As` through
the adapter's own wrapping is how a host reaches that conclusion.

### An empty message has no parts, and that is deliberate

`TextMessage(role, "")` returns a message with **no parts at all**, rather than
one part carrying the empty string. Three cases hang off that:

| Message | Parts | On the wire |
|---|---|---|
| an assistant turn that only called tools | none | tool blocks only; no empty text block |
| a tool result with nothing to report | none | a `tool_result` whose text is empty, as before |
| several text parts | each | joined in order into the one text field both dialects take |

Anthropic rejects an empty content block, and the old code guarded it with
`if message.Text != ""`. The guard is unchanged — it now reads
`if text != ""` over the concatenation — so no message that worked before is
shaped differently now. A message with no parts is text-only and its text is
empty; that is not an error and never was.

## The `llmtest` clause

`UnspeakableContentIsRefused` was added to the kit, which now runs six checks.
It streams a turn whose last message carries a part of a kind **no build of this
contract defines** (`PartKind(1 << 30)`), and insists the adapter says so: either
`Stream` returns an error, or the turn fails with the cause in `Err`. A turn that
succeeds is the violation.

Using an undefined kind rather than a real one is what keeps the clause true
after images land: there is always a kind newer than the adapter reading it, so
an adapter that speaks images is still asked what it does with content it cannot
speak.

The clause exists because this is precisely the failure a host cannot find on its
own. Every other thing the kit checks leaves a trace in the stream — a channel
that never closes, a block that never ends. A dropped part leaves nothing, which
is why prose alone was never going to be enough for it. Like every other clause,
it is tested both ways: against an implementation that refuses (both shapes) and
against one that drops.

`FakeProvider` gained the same three lines. A fake that accepts content it cannot
render would hide this failure from every test written against it, which is the
opposite of what a fake is for.

## What this did not do

- **No adapter gained vision.** `Capabilities.Vision` is still `false` for
  Anthropic and for all three OpenAI dialects, and its doc comment now says what
  is true: the seam is no longer what stands in the way, and the flag is what an
  adapter flips when it can put an image on the wire.
- **The seam stops at the provider boundary.** `session.Message`, the durable
  event stream, the SQLite schema and both UIs are unchanged; the event contract
  is R5's job. `runner.toLLMMessages` is the single place that projects durable
  text into content parts, and the single place that has to learn to project an
  image the day the stream carries one.
- **The estimator walks parts** (`llm.EstimateRequestTokens`) and weighs the same
  text identically however it is sliced. A part kind that carries bytes anywhere
  other than `Text` has to be sized there too: what the estimate omits it
  under-counts, and an under-count is preventive compaction not firing on the
  request that then overflows.

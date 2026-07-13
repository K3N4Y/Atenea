---
updated_at: 2026-07-12
summary: Specification for the tool input repair layer (validate-then-repair) integrated in the registry settle.
---

# Spec — Tool input repair (validate-then-repair)

Executable spec of the **tool input repair layer** (`internal/tool/repair`):
the piece that, before a tool executes, validates the raw JSON input from the
model against the tool schema and repairs known almost-valid shapes. A valid
input passes byte for byte untouched; a repaired input carries model-directed
notes prepended to the tool output; an irreparable input returns a
model-readable error **without** executing the tool.

We work with the cycle `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).

## 1. Context and motivation

Open and small models fail tool calls with **almost-valid** shapes far more
often than with semantically wrong ones (see
`../research/slm-tool-calling-reliability.md`): a required array arrives
JSON-stringified, a key arrives under a well-known alias (`file_path` instead
of `path`), literal newlines appear inside JSON strings, the object arrives
truncated, optional fields arrive as `null`, or a bare string arrives where an
object was expected. Rejecting those calls wastes a full model turn per retry
and small models often repeat the same mistake.

The approach is **validate-then-repair**, inspired by the CommandCode harness:
first validate the input against the schema; only if it does not conform,
apply a fixed set of known repairs and re-validate. Two properties follow:

- **Zero risk for valid inputs.** An input that already conforms is returned
  byte for byte identical (never re-serialized or normalized), with no notes.
- **The model learns.** Every applied repair leaves a note addressed to the
  model explaining what was fixed and how to send the input correctly next
  time; the notes travel prepended to the tool output as `<repair_note>`
  lines, so the model sees them in its next turn.

The layer lives in `internal/tool/repair` and is wired into the settle of
`Registry.Materialize` (`internal/tool/registry.go`), so every local tool call
goes through it uniformly.

## 2. Contract (`Repair`, `WithNotes`, `InvalidInputError`)

### `Repair`

```go
func Repair(toolName string, schema, input json.RawMessage) (json.RawMessage, []string, error)
```

- Validates `input` against the top-level object `schema` of the tool (the
  declared `properties` types and the `required` list).
- **Passthrough.** If the input parses directly and already conforms, it is
  returned byte for byte identical, with `nil` notes and no error.
- Otherwise it applies the repair rules (section 3) and re-validates. Each
  applied repair appends one note (Spanish text addressed to the model,
  explaining the fix and the correct shape for next time).
- If the input still does not conform after repairing, it returns a
  `*InvalidInputError` (and no repaired input): the tool must not execute.

### `WithNotes`

```go
func WithNotes(notes []string, output string) string
```

Prepends each note to the tool output as one
`<repair_note>...</repair_note>` line, followed by the output. With no notes
it returns the output untouched. The caller applies it **before** capping the
output, so the note header survives truncation.

### `InvalidInputError`

```go
type Issue struct {
    Field   string
    Message string
}

type InvalidInputError struct {
    Tool   string
    Issues []Issue
}
```

The error `Repair` returns for an irreparable input. Its message is addressed
to the model, never a raw Go decoder error: a header
(`Input inválido para la tool %q. Corrige y reintenta:`) followed by one
bullet (`• field: message`) per issue. Root-level issues (input is not JSON in
any repairable form) use the field `(root)`. It is a type, inspectable with
`errors.As`, so the runner can distinguish "invalid input" from other tool
failures.

## 3. Repair rules (in order)

The rules act in two layers, in a fixed order. Field-level rules iterate over
sorted keys so repairs and notes are deterministic.

### Tolerant parse of the raw input

Only when the direct `json.Unmarshal` into an object fails:

1. **Unescaped control characters.** Literal control bytes (`< 0x20`) inside
   JSON strings are escaped (`\n`, `\t`, `\r`, `\uXXXX`), respecting escape
   sequences already present, and the parse is retried.
2. **Truncated JSON.** An input starting with `{` is retried with a fixed list
   of closing suffixes (pending string quote plus brace/bracket combinations
   up to a few nesting levels) until it parses to an object.
3. **Single-object root array unwrap.** `[{...}]` where the schema expects an
   object is unwrapped to that inner object.
4. **Bare root string.** A lone JSON string, when the schema has exactly one
   required property of type `string`, is wrapped as `{required: value}`.

If none of these produces a parseable object, `Repair` returns
`*InvalidInputError` with a single `(root)` issue.

### Field rules over the decoded object

5. **Rename aliases.** A key absent from the schema that is a known alias of a
   declared canonical key (e.g. `file_path` -> `path`, `cmd` -> `command`,
   `items` -> `todos`) is renamed to the canonical key — only if the canonical
   key is absent from the input: an existing value is never overwritten.
6. **Drop nulls.** A field present with value `null` is removed: optional
   fields are omitted, not sent as `null`.
7. **Stringified coercion.** A string value whose content parses as the
   declared non-string type (`object`, `array`, `boolean`, `number`,
   `integer`) is replaced by the real value.
8. **Wrap string into array.** As a fallback of rule 7, a string value where
   the schema declares an `array` and the content is not itself a JSON array
   is wrapped in a one-element array.

### Final validation

After the rules run, the object is validated again: every required property
present and every present value matching its declared type (`integer` demands
a number without fractional part; `null` never matches any type). Remaining
issues become the `*InvalidInputError`.

## 4. Integration in the settle

The settle returned by `Registry.Materialize` (`internal/tool/registry.go`)
runs the layer between the permission gate and the execution:

- The allowed-set gate stays first: a tool outside the materialized set still
  returns `UnknownToolError` before anything else.
- **Before `Execute`.** The raw `Call.Input` goes through `repair.Repair`
  against the tool schema; the tool executes with the repaired input. An
  irreparable input returns the `*InvalidInputError` **without** executing the
  tool (no side effects); the runner settles it as a failed call whose message
  the model can act on.
- **Notes before the cap.** The repair notes are prepended to the tool output
  with `repair.WithNotes` **before** `OutputStore.Cap`, so the
  `<repair_note>` header survives capping and the model always sees it. The
  UI-only `Diff` keeps bypassing the cap.
- **Nil input skips the layer.** A `Call` with empty input (a tool without
  arguments) does not go through `Repair`: there is nothing to repair, and nil
  is not parseable JSON.
- `Repair` is pure per call (no shared mutable state), so the settle stays
  safe for the concurrent invocation the runner does from its `errgroup`.

## 5. Testing

Behavior-named tests next to the code, per the repo conventions:

- `internal/tool/repair/repair_test.go` (17 tests): one per repair rule
  (control chars, truncated JSON at several nesting depths, root array unwrap,
  bare root string, alias rename and its no-overwrite guard, null drop,
  stringified coercion per type, string-to-array wrap), the byte-for-byte
  passthrough of valid input, the readable error paths (missing required,
  unparseable root, uncoercible value — never leaking raw decoder messages),
  multiple repairs with one note each, and the `WithNotes` formatting.
- `internal/tool/registry_test.go` (6 integration tests): repair happens
  before `Execute` (a recorder tool captures the exact input it received), an
  irreparable input yields `*repair.InvalidInputError` with no side effects,
  valid input passes through untouched with no notes, repair notes survive
  output capping, nil input skips the layer, and concurrent settles with
  repairs stay independent (run with `-race`).

```bash
go test ./internal/tool/... -count=1
go test -race ./internal/tool/... -count=1

# Closing gates
gofmt -l .          # empty
go vet ./...        # clean
go test ./...       # whole suite green
```

## 6. Sources

- Research: `../research/slm-tool-calling-reliability.md` (why small models
  fail tool calls and why structured, actionable errors plus a repair layer
  help)
- Registry and settle: `atenea-m4-tool-registry-spec.md`,
  `../architecture/agent-loop.md`
- Way of working: `AGENTS.md`

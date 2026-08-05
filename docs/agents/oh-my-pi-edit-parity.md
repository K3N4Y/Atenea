---
updated_at: 2026-08-03
summary: Maintained evidence mapping Atenea's edit implementation and tests to pinned oh-my-pi sources.
---

# oh-my-pi edit implementation parity

## Pin and mapping

Upstream is `can1357/oh-my-pi@5af71dc9`. The maintained comparison covers:

| Area | Upstream source/tests | Atenea implementation/evidence |
| --- | --- | --- |
| Hashline | `packages/hashline/src/{parser,apply,patcher,snapshots}.ts`; hashline parser/apply/landing tests | `internal/tool/hashline`; `*_contract_test.go`, `boundary_repair_test.go`, `landing_shift_test.go`, `snapshot_recovery_test.go` |
| Apply patch | `packages/coding-agent/src/tools/apply-patch.ts`; `core/apply-patch.test.ts` | `internal/tool/editmode/apply_patch.go`; `edit_apply_patch_test.go`, `editmode/apply_patch_test.go` |
| Patch JSON | coding-agent patch tool and context-diff tests | `internal/tool/editmode/patch.go`; `edit_patch_test.go`, `editmode/patch_test.go` |
| Replace JSON | coding-agent edit/replace matching tests | `internal/tool/editmode/replace*.go`; `edit_replace_test.go`, `editmode/replace_test.go` |
| Streaming/matcher | `edit/streaming*.test.ts`, matcher-path tests | `edit_preview.go`; `edit_preview_test.go`, runner and TUI preview tests |
| Shared settlement | coding-agent structured edit rendering/result tests | `agentcore/tool`, runner publisher, `internal/tui/{transcript,view_diff}.go`; runner/TUI E2E tests |

## Deliberate Go adaptations

- Go's filesystem, locking, xxhash implementation, and bounded in-memory snapshot
  store replace Bun APIs. Same-directory replacement and explicit committed-but-
  durability-uncertain results preserve the safety intent.
- The public tool is a turn-frozen facade because Atenea configures model variants
  per provider turn. Wire schema and executor remain one immutable selection.
- Bubble Tea consumes generic runner previews and structured file results instead
  of upstream UI components. One settlement, not one file, invalidates workspace
  state.
- Permission approval is Atenea's session gate and is verified through the real
  production binary PTY path.

## Nonapplicability evidence

Bun runtime helpers cannot run in the Go core and are replaced above. Atenea's
standalone TUI has no ACP host boundary, and its tool/result contracts contain no
notebook cell model; ACP and notebook edit tests therefore have no executable
local consumer. These are deliberate exclusions, not unimplemented aliases.

## Deleted-test mapping

The removed original edit integration file and hashline parser/apply/patcher/
snapshot test files map one-for-one by concern to the current
`edit_*_test.go`, `hashline/*_contract_test.go`, recovery, filesystem matrix, and
result-fault suites. Retired operation-description tests map to modern
`edit_prompt_examples_test.go` and parser-v2 tests; no retired grammar remains a
compatibility contract.
compatibility contract.

## Delivery gates

```text
go test -count=1 -run TestTUI_ProductionEditApprovalUnderPTY ./cmd/atenea
go test -count=10 ./internal/tool ./internal/tool/editmode ./internal/session/runner ./internal/tui
go test -race ./internal/tool ./internal/tool/hashline ./internal/session/runner ./internal/tui ./internal/tui/engine
go test ./agentcore/... ./internal/... ./cmd/atenea/...
go vet ./agentcore/... ./internal/... ./cmd/atenea/...
gofmt -l .
go build -tags production -o ./build/bin/atenea ./cmd/atenea
```

The PTY gate uses isolated HOME, provider config, SQLite DB, checkpoints, and
workspace with a local OpenAI-compatible server; it verifies read provenance,
permission approval, rendered edit settlement, and exact final bytes.

// Package tool is the private side of the tool boundary: the registry that
// materializes and settles tool calls, the output capping, and the built-in
// tools atenea ships. The contract itself — Tool, Call, Result — is published in
// agentcore/tool and re-exported here by contract.go.
//
// The Registry announces the permitted subset of its catalog as llm.ToolDef and
// returns a SettleFunc closed over that same subset, so a denied or unknown tool
// fails before executing and produces no side effects. Settlement passes every
// input through the repair layer of internal/tool/repair BEFORE Execute:
// an almost-valid input is repaired against the tool's schema and what was
// repaired is prepended to the output as <repair_note> lines so the model
// corrects its next call, while an irreparable input returns a model-readable
// error without executing anything. The OutputStore then caps large output by
// call id, keeping the full text addressable.
//
// The built-ins:
//
//   - read, edit and write share the hashline engine of internal/tool/hashline
//     (freshness hash + file snapshot + lines seen): read numbers lines under a
//     [path#HASH] header and records the snapshot, edit applies a hashline patch
//     anchored to that header, and write creates a new file — the path edit
//     cannot take — recording its snapshot so a later edit anchors without
//     re-reading.
//   - glob finds files by ripgrep pattern and returns workspace-relative paths;
//     grep searches content and returns lines in hashline format so an edit can
//     be chained onto the result.
//   - lsp maintains installed language servers lazily for diagnostics,
//     navigation, symbols and semantic rename; successful writes and edits ask
//     the same instance for diagnostics.
//   - bash runs a command per call with bash -c (no persistent session: cwd and
//     env do not survive between calls), combines stdout and stderr, applies a
//     tiered timeout (fast by default, slow with slow_ok), kills the process
//     group on expiry, scrubs secrets from the environment and caps output
//     head+tail.
//   - web_fetch, skill, todo_write and present_plan complete the set.
package tool

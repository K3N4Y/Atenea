// Package host is atenea's outer composition root: everything both entrypoints
// need assembled before either one can build an agent.
//
// atenea ships two hosts over one core. The desktop app (main.go, app.go) drives
// internal/wailsworkspace; the terminal app (cmd/atenea) drives
// internal/tui/engine. Those two managers are deliberately different — one
// switches workspaces live and emits through Wails, the other owns a Bubble Tea
// event channel — and internal/wiring.Build is the single inner root they both
// call, anchored to a workspace root and rebuilt whenever that root or the set of
// connected MCP tools changes. This package is the layer above that line, which
// is the layer each entrypoint used to re-implement.
//
// What it owns falls into three groups:
//
//   - The bootstrap that has to happen before anything reads the environment or
//     the filesystem: the development .env and the workspace root.
//   - The resources the two hosts share on purpose, which is what makes a session
//     started in the terminal show up in the desktop sidebar and a model chosen in
//     either one the selection both see: the SQLite store and the provider
//     service, on their default paths.
//   - The [Sitting]: the state that belongs to one run of the process rather than
//     to one assembly of it, and therefore has to survive every rewire.
//
// It is not a container. Nothing is registered, nothing is looked up by type and
// nothing is reflected over: [Config] names the few things a caller varies, [New]
// assembles in a fixed order, and [Host] publishes the result as fields.
//
// Nothing here is fatal. A store that will not open degrades to memory and a
// provider config that will not load degrades to the fallback, each with a line
// in the log, because a host that refuses to start over either is a host the user
// cannot recover from.
package host

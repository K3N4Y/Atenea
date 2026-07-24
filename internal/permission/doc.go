// Package permission is the single ask-before-run gate for tool calls. The
// Policy classifies each call (Allow, Ask, or Deny) and the Gate blocks a
// call that must ask until the user's decision arrives from the UI. The
// runner consults both before settling any tool call; the classification
// itself (which tools ask) is wired once in internal/wiring and shared by the
// main runner and the subagents, so a child cannot evade the gate the main
// chat enforces.
//
// Policy is a real seam: StaticPolicy (classification by tool name) is the
// whole shipped policy today, and richer implementations — persisted
// allow/deny rules, "always allow", permission modes — plug in later without
// touching the runner or the UI.
package permission

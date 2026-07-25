// Package permission is the single ask-before-run gate for tool calls. The
// Policy classifies each call (Allow, Ask, or Deny) and the Gate blocks a
// call that must ask until the user's decision arrives from the UI. The
// runner consults both before settling any tool call; the classification
// itself (which tools ask) is wired once in internal/wiring and shared by the
// main runner and the subagents, so a child cannot evade the gate the main
// chat enforces.
//
// Policy is a real seam. StaticPolicy is the base classification by tool name;
// SessionGrants layers the user's "allow this for the rest of the session"
// answers over it (a bash command prefix, or a whole filesystem-mutating
// tool), so a granted call is allowed without ever reaching the Gate and
// without leaving a permission request in the session's history. Richer
// implementations — persisted allow/deny rules, permission modes — plug in the
// same way, without touching the runner or the UI.
package permission

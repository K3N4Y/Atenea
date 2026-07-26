// Package skill discovers and formats the workspace's skills for the agent.
//
// A skill is a directory holding a SKILL.md: frontmatter (name, description) plus
// a Markdown body with instructions and resources. The agent exposes them through
// two levels of progressive disclosure, as opencode does: only the metadata (name
// + description) travels in the system prompt (see Format), and the full body is
// loaded on demand when the model invokes the skill tool (see internal/tool).
//
// Discovery is deliberately forgiving — a SKILL.md that cannot be parsed is
// skipped so one broken skill cannot take the others down — which makes a
// malformed skill invisible rather than loud. Scan is the same walk with nothing
// skipped, so `atenea skill validate` can report what discovery silently dropped.
package skill

---
updated_at: 2026-08-01
summary: Explicit process-local interactive YOLO authorization, UI behavior, and the narrow recursive-rm breaker.
---

# Interactive YOLO permission mode

`atenea --yolo` and `atenea --dangerously-skip-permissions` authorize one
interactive process to skip permission prompts. Headless commands are unchanged.
The launch starts in YOLO; `/mode:ask` and `/mode:auto-accept` leave it, and only
an authorized process exposes and accepts `/mode:yolo`. Nothing is persisted.

The sitting owns authorization and activation so main-agent and subagent policy
stay identical across MCP rewires. Policy composition upgrades only `Ask` to
`Allow`; an existing `Deny` remains final. Safe auto-accept remains a separate,
narrow mode.

YOLO is deliberately not a sandbox. Unknown tools and shell commands are
allowed. One non-overridable breaker denies positively recognized recursive
direct `rm` commands whose normalized operand is filesystem root or the resolved
user home. The breaker parses shell syntax into an AST and visits executable simple
commands in lists, pipelines, conditionals, loops, groups, subshells, and command
substitutions. Redirections do not hide command operands. It recognizes static
words, ordinary quoting, `$HOME`/`${HOME}`/`~`, options, assignments, and the
supported `env`, `sudo`, and `command` wrapper forms; wrapper modes such as
`command -v` and `env --help` that inspect or exit without executing a command
are not classified as `rm` execution.

This is an AST recognizer, not shell evaluation. It intentionally does not
resolve aliases, execute function declarations, interpret generated command
names or operands, inspect arbitrary script files, or recognize non-`rm`
deletion tools. Single-quoted or escaped `$HOME`, and quoted or escaped `~`, are
literal paths, just as the shell treats them. Other deletion forms and other
destinations are allowed, which the startup warning and user documentation state
prominently.

The TUI shows a startup warning and persistent `YOLO` composer indicator.
`/mode` reports the effective permission mode.

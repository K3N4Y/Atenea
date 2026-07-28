# Security policy

Atenea is an agent that can read and modify workspace files, execute commands,
contact network services, load skills, and connect to local or remote MCP
servers. Treat every workspace, prompt, skill, provider, and MCP server as
potentially untrusted.

## Reporting a vulnerability

Do not open a public issue, discussion, or pull request for an undisclosed
vulnerability. Report it privately through
[GitHub Security Advisories](https://github.com/K3N4Y/Atenea/security/advisories/new).

Include, when available:

- the affected version, commit, host (desktop, TUI, or headless CLI), and
  operating system;
- a minimal reproduction and the security impact;
- whether the issue requires a malicious workspace, model response, skill, MCP
  server, provider, or user action;
- logs or screenshots with credentials, tokens, personal data, and private
  workspace content removed;
- any suggested mitigation or patch.

You should receive an acknowledgement within seven days. Please allow the
maintainer time to reproduce, fix, test, and release the correction before
publishing details. If the private advisory form is unavailable, open a public
issue containing only a request for a private security contact—do not include
vulnerability details.

## Supported versions

Security fixes target the latest release and the current default branch. Older
releases may not receive backports. If you can reproduce an issue only on an
older version, report it anyway and identify that version precisely.

## Security boundaries

Reports are especially valuable when they demonstrate one of the following:

- command execution, file mutation, or network access without the applicable
  permission decision, or an incorrect unattended-mode effect classification;
- an MCP server or tool escaping its declared permission, workspace, transport,
  or lifecycle boundary;
- a bypass of the announced-tool set that executes an unknown or stale tool;
- path traversal, symlink handling, or workspace-root confusion that accesses
  data outside the intended scope;
- disclosure or insecure persistence of provider keys, MCP environment values,
  credentials, session content, or tool output;
- injection through provider, MCP, skill, event, or tool data that reaches a
  shell, executable, UI control sequence, or trusted prompt boundary with
  unintended authority;
- a malformed or adversarial provider/tool stream that bypasses validation,
  leaves privileged work running after cancellation, or crosses session
  boundaries;
- a vulnerability in a published `agentcore/` contract or its host-side
  enforcement that affects third-party implementations.

The ask-before-run gate is a security boundary, not a sandbox. Approval to run a
command or third-party MCP tool grants that operation the authority of the Atenea
process and the current user. A report should distinguish a permission bypass
from harmful behavior that the user explicitly approved.

## Extensions and code execution

Atenea does not load third-party Go plugins in process. Its runtime extension
boundary is MCP:

- a stdio MCP configuration starts the configured executable as the current
  user;
- HTTP, streamable HTTP, and SSE configurations send data to the configured
  remote service;
- MCP tools may receive workspace context and tool input, and server-provided
  prompts or resources may contain untrusted instructions;
- tool sensitivity declarations affect permission decisions and must not be
  treated as proof that a server is benign.

Only configure MCP servers and skills you trust, inspect commands and URLs before
approving them, use the least permissive effect budget for headless runs, and do
not place secrets directly in repository configuration. Keep provider keys in
the supported credential store or environment variables and redact them from
reports.

Prompt injection or an undesirable model decision is not by itself a product
vulnerability. It is in scope when it crosses an enforced boundary—for example,
when untrusted content causes an unapproved tool call, exposes a secret, or
executes with more authority than the user granted.

## Safe research

Test only against systems, accounts, workspaces, and MCP servers you own or are
authorized to assess. Avoid privacy violations, persistence, destructive
commands, service disruption, and unnecessary access to data. Stop once you
have enough evidence to demonstrate the issue, and share that evidence only
through the private reporting channel.

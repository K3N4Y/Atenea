# Contributing to Atenea

Thank you for improving Atenea. Contributions should preserve the behavior of
both hosts—the Wails desktop application and the standalone TUI—and keep the
shared agent core independent of either UI.

Before starting substantial work, search the
[issue tracker](https://github.com/K3N4Y/Atenea/issues) for an existing report or
proposal. Open an issue when a change needs design agreement or changes a public
contract. Security vulnerabilities must be reported privately as described in
[SECURITY.md](SECURITY.md), not in a public issue.

## Development setup

The required versions and commands live in [AGENTS.md](AGENTS.md). In particular:

- use the Go version declared by `go.mod`;
- install Node.js and the frontend dependencies with `npm ci` from
  `frontend/`;
- install `rg` (ripgrep), which the grep and glob tools execute;
- ensure `frontend/dist` exists before building the root package. An empty
  directory is sufficient when the frontend has not been built.

Run the desktop application with `wails dev`, or build the TUI with:

```bash
mkdir -p frontend/dist
go build -tags production -o ./build/bin/atenea ./cmd/atenea
```

## Making a change

1. Reproduce bugs end to end through the affected host before changing code.
2. Add or update behavior-focused tests next to the implementation.
3. Keep shared behavior correct in both the desktop application and TUI.
4. Consult the [documentation index](.okf/README.md) and update the relevant
   architecture, specification, plan, or research document with the code.
5. Keep new code, comments, errors, tests, documentation, and commit messages in
   English. Comments should explain non-obvious reasons rather than restating
   the code.

Prefer small, reviewable commits. Commit subjects use Conventional Commits with
a package scope, for example:

```text
feat(providerconfig): add an OpenAI-compatible provider
fix(runner): settle canceled tool calls
docs(contributing): document public contract checks
```

Do not add generated-by or co-author attribution for an automated agent unless
the repository's current automation explicitly requires it.

## Architecture and public contracts

The architecture rule is **contracts public, loop private**:

- `agentcore/` contains contracts that third parties implement or consume;
- `internal/` contains the runner, stores, wiring, adapters, UIs, and built-in
  implementations;
- code under `agentcore/` must not import `internal/` or third-party modules.

Read [Published contracts](.okf/architecture/public-contracts.md) before changing
`agentcore/`. A public type belongs there only when an external implementation or
consumer needs it. Add the corresponding alias to the matching `internal/`
package when internal code uses the contract.

Published interfaces include executable promises that types alone cannot
express. Every new implementation must run the applicable contract kit in
addition to its implementation-specific tests:

- tools: `agentcore/tool/tooltest.Contract`;
- model providers: `agentcore/llm/llmtest.Contract`;
- session stores (a private host seam):
  `internal/session/sessiontest.StoreContract`.

Use a fresh subject factory for every contract check. Run contract tests under
`go test -race`; concurrency and cancellation behavior are part of the contract.
Changes to published contracts require updated package documentation, contract
tests, and `.okf/` architecture documentation. Avoid silently dropping unknown
event kinds, attributes, or message content.

## Quality gates

Run focused tests while developing. Before submitting a pull request, run the
same gates as CI from the repository root:

```bash
mkdir -p frontend/dist
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
cd frontend
npm run lint
npm run format:check
npm test
```

`gofmt -l .` must produce no output. Fix failures and flaky tests even when they
surface outside the files you changed; if a failure cannot safely be addressed
in the same pull request, document it clearly instead of hiding it.

For UI changes, exercise the real user flow in the affected host and include
screenshots or recordings when they help reviewers assess layout and behavior.

## Pull request checklist

- Explain the user-visible outcome and the reason for the chosen design.
- Link the relevant issue, when one exists.
- Describe how the change was tested, including both hosts when shared behavior
  changed.
- Add tests for new behavior and regression tests for fixes.
- Update affected `.okf/` documents and their index when required.
- Call out public-contract or security-boundary changes explicitly.
- Keep unrelated refactors out of the pull request.

By contributing, you agree that your contribution is licensed under the
repository's [MIT License](LICENSE).

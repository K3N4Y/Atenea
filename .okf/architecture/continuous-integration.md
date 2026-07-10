---
updated_at: 2026-07-10
summary: Continuous integration and automated review contract for Atenea.
---

# Continuous integration and automated review

Atenea uses GitHub Actions for executable quality gates and CodeRabbit for
repository-aware pull request review. The current automation protects the Go
agent and TUI work as well as the Vue frontend, while deliberately avoiding
desktop packaging until Wails delivery becomes an active development focus.

## GitHub Actions

The workflow at `.github/workflows/ci.yml` runs for every pull request and for
pushes to `main`. New commits cancel older runs for the same pull request or
branch. Repository permissions are read-only.

The jobs are independent so a frontend failure does not hide the result of the
Go checks, and vice versa.

### Go quality

The Go job uses the version declared in `go.mod` and runs:

1. `test -z "$(gofmt -l .)"`
2. `go vet ./...`
3. `go test ./...`
4. `go test -race ./...`

The race detector is a required gate because the runner, tools, and TUI use
goroutines, channels, and shared state.

### Frontend quality

The frontend job installs the exact lockfile state with `npm ci` and runs:

1. `npm run lint`
2. `npm run format:check`
3. `npm test`

All frontend commands run from `frontend/`.

### Deliberate omissions

The workflow does not run `npm run build`, `wails build`, or platform packaging.
Those gates should be introduced together when desktop release work resumes so
the required native dependencies and platform matrix are designed explicitly.

## CodeRabbit

The root `.coderabbit.yaml` enables assertive automatic reviews for non-draft
pull requests and requests English review output. Its instructions reinforce
the repository's existing engineering contract:

- behavioral tests and visible RED/GREEN evidence;
- race-aware review of concurrent Go code;
- end-to-end and PTY coverage for user-facing TUI changes;
- accessibility, strict typing, responsive behavior, and Vitest coverage in the
  frontend;
- minimal workflow permissions and deterministic dependency caches;
- English `.okf/` documentation synchronized with behavior and architecture.

Generated Wails bindings, dependency directories, build outputs, binaries, and
lockfiles are excluded from review to keep feedback focused on authored code.

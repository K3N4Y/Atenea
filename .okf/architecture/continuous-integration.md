---
updated_at: 2026-07-21
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

1. Install `ripgrep`, which is a runtime dependency of workspace file discovery
   exercised by the Go and PTY tests.
2. Create `frontend/dist/.gitkeep` so the root package's `go:embed` directive
   has an input in clean checkouts without building the frontend.
3. `test -z "$(gofmt -l .)"`
4. `go vet ./...`
5. `go test ./...`
6. `go test -race ./...`

The race detector is a required gate because the runner, tools, and TUI use
goroutines, channels, and shared state.

### Frontend quality

The frontend job installs the exact lockfile state with `npm ci` and runs:

1. `npm run lint`
2. `npm run format:check`
3. `npm test`

All frontend commands run from `frontend/`.

### TUI release quality

The release-quality job asks GoReleaser to create a clean snapshot release
without publishing it. This cross-compiles all four supported Linux/macOS and
`amd64`/`arm64` targets with the production build tag, then creates the same
archives and checksum manifest used by a tagged release. The Go suite separately
exercises `install.sh` against a local release fixture and runs the installed
binary through `atenea --version`.

`.github/workflows/release.yml` runs only for `v*` tags and grants its job
`contents: write` so GoReleaser can publish the verified TUI artifacts. Normal
CI remains read-only.

### Deliberate omissions

The workflow does not run `npm run build`, `wails build`, or desktop platform
packaging. Those gates should be introduced together when desktop release work
resumes so the required native dependencies and platform matrix are designed explicitly.
The placeholder created by the Go job exists only to satisfy compile-time asset
embedding and is not treated as a production frontend build.

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

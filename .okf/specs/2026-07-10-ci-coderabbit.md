---
updated_at: 2026-07-10
summary: Specification for Atenea's GitHub Actions and CodeRabbit automation.
---

# CI and CodeRabbit Design

## Goal

Add pull-request automation that protects the Go/TUI and frontend code without
building the Wails desktop application.

## GitHub Actions

Create `.github/workflows/ci.yml` and run it for pull requests and pushes to
`main`.

The workflow uses minimal read-only repository permissions and cancels an older
run when a newer commit arrives for the same pull request or branch.

Two independent jobs run in parallel:

### Go quality

- Check out the repository.
- Install the Go version declared by `go.mod` and cache module/build data using
  `go.sum`.
- Create a temporary file under `frontend/dist` so `go:embed` can compile the
  root package in a clean checkout without building the frontend.
- Fail when `gofmt -l .` reports files.
- Run `go vet ./...`.
- Run `go test ./...`.
- Run `go test -race ./...` because the runner, tools, and TUI include
  concurrent behavior.

### Frontend quality

- Check out the repository.
- Install the current Node.js LTS line and cache npm dependencies using
  `frontend/package-lock.json`.
- Install exact dependencies with `npm ci` in `frontend/`.
- Run `npm run lint`.
- Run `npm run format:check`.
- Run `npm test`.

The workflow deliberately omits `npm run build`, `wails build`, and desktop
packaging. Those checks can be added later when desktop delivery becomes an
active development focus.

## CodeRabbit

Create `.coderabbit.yaml` at the repository root.

- Enable automatic reviews for non-draft pull requests.
- Use an assertive review profile and produce review summaries in English.
- Ask CodeRabbit to enforce the repository's engineering rules: behavioral
  tests, race-aware Go changes, maintainable designs, and synchronized `.okf/`
  documentation.
- Add focused path instructions for Go/TUI, Vue/TypeScript, tests, workflows,
  and project documentation.
- Exclude generated Wails bindings, build artifacts, vendored dependencies,
  binaries, and lockfiles from review noise.

## Verification Strategy

Verification runs the same commands represented by the workflow:
`gofmt -l .`, `go vet ./...`, `go test ./...`, `go test -race ./...`,
`npm run lint`, `npm run format:check`, and `npm test`.

## Success Criteria

1. Pull requests and pushes to `main` run independent Go and frontend checks.
2. CI does not build the Wails desktop application.
3. CodeRabbit automatically reviews non-draft pull requests with repository-
   specific guidance and ignores generated/noisy files.
4. Relevant `.okf/` documentation describes the automation and remains linked
   from the documentation index.

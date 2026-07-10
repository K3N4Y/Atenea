---
updated_at: 2026-07-10
summary: Implementation plan for Atenea's CI and automated code review setup.
---

# CI and CodeRabbit Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add GitHub Actions checks for Go/TUI and frontend quality plus repository-specific automatic CodeRabbit reviews, without building Wails.

**Architecture:** A two-job GitHub Actions workflow runs Go and frontend gates independently, while `.okf/` documents the operational contract.

**Tech Stack:** GitHub Actions, Go 1.23+, Node.js LTS, npm, and CodeRabbit YAML.

---

### Task 1: GitHub Actions workflow

**Files:**
- Create: `.github/workflows/ci.yml`

- [x] Add pull request and `main` push triggers with `contents: read`.
- [x] Add per-ref concurrency with cancellation.
- [x] Install ripgrep for workspace and PTY tests.
- [x] Prepare a placeholder embedded asset for clean checkouts.
- [x] Add `go-quality` with checkout, Go setup/cache, `gofmt`, vet, tests, and race tests.
- [x] Add `frontend-quality` with checkout, Node LTS/npm cache, `npm ci`, lint, formatting, and tests.

### Task 2: CodeRabbit configuration

**Files:**
- Create: `.coderabbit.yaml`

- [x] Configure assertive English automatic reviews for non-draft pull requests.
- [x] Add repository and path-specific Go/TUI, frontend, testing, workflow, and `.okf/` guidance.
- [x] Exclude Wails bindings, artifacts, dependencies, binaries, and lockfiles.

### Task 3: Documentation

**Files:**
- Create: `.okf/architecture/continuous-integration.md`
- Modify: `.okf/README.md`

- [x] Document triggers, gates, omissions, CodeRabbit behavior, and extension guidance.
- [x] Link the automation document from `.okf/README.md`.
- [x] Run the relevant project quality gates.

### Task 4: Final evidence

**Files:**
- Modify only if a gate exposes a defect directly relevant to the automation.

- [x] Run `gofmt -l .` and require empty output.
- [x] Run `go vet ./...`.
- [x] Run `go test ./...`.
- [x] Run `go test -race ./...`.
- [x] Run `npm run lint`, `npm run format:check`, and `npm test` in `frontend/`.
- [x] Inspect `git diff --check` and the final diff.

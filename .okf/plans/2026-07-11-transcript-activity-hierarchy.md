---
updated_at: 2026-07-11
summary: TDD implementation plan for the continuous operational activity rail in the desktop transcript.
---

# Transcript Activity Hierarchy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn adjacent tool events into a compact, continuous, accessible activity rail whose verbose output, errors, and diffs are collapsed by default.

**Architecture:** Keep `TurnItem[]` flat in the Pinia store. Add a pure presentation helper that derives action, target, compact result, semantic variant, and status from each `ToolItem`; group only adjacent tools in `MessageList`; let `ActivityGroup` own rail continuity while `ToolCall` owns disclosure and permission actions. Reserve `summary` and `subagent` in the presentation vocabulary without adding unused store event types.

**Tech Stack:** Vue 3 `<script setup>`, TypeScript, Tailwind CSS utilities, Phosphor icons, Vitest, Vue Test Utils.

---

### Task 1: Derive Stable Activity Presentation

**Files:**
- Create: `frontend/src/lib/activityPresentation.ts`
- Create: `frontend/src/lib/activityPresentation.test.ts`

- [ ] **Step 1: Write failing summary tests**

Create table-driven tests for `activityPresentation(item)` covering:

```ts
expect(activityPresentation(tool({ name: 'read', input: { path: 'internal/auth.go' } }))).toMatchObject({
  kind: 'tool',
  action: 'read',
  target: 'internal/auth.go',
  status: 'success',
})

expect(activityPresentation(tool({
  name: 'edit',
  input: { path: 'internal/auth.go' },
  diff: '@@ -1 +1,2 @@\n-old\n+new\n+extra',
})).compactResult).toBe('+2 -1')

expect(activityPresentation(tool({
  name: 'bash',
  input: { command: 'go test ./internal/auth' },
  status: 'pending',
}))).toMatchObject({ kind: 'permission', target: 'go test ./internal/auth' })
```

Also cover grep pattern/path, glob pattern, skill name, failed error excerpts,
unknown scalar input fallback, multiline target normalization, and the exported
`ActivityPresentationKind` accepting future `subagent` and `summary` values.

- [ ] **Step 2: Run tests and verify RED**

Run: `cd frontend && npm test -- --run src/lib/activityPresentation.test.ts`

Expected: FAIL because `activityPresentation.ts` does not exist.

- [ ] **Step 3: Implement minimal pure helper**

Implement:

```ts
export type ActivityPresentationKind =
  | 'tool'
  | 'permission'
  | 'file-change'
  | 'subagent'
  | 'summary'

export interface ActivityPresentation {
  kind: ActivityPresentationKind
  action: string
  target: string
  compactResult: string
  status: ToolStatus
  expandable: boolean
  accessibleLabel: string
}

export function activityPresentation(item: ToolItem): ActivityPresentation
```

Use small internal functions for structured scalar extraction, one-line
normalization, unified-diff counts, and unambiguous grep match counts. Never
modify the original item content.

- [ ] **Step 4: Run helper tests and verify GREEN**

Run: `cd frontend && npm test -- --run src/lib/activityPresentation.test.ts`

Expected: PASS.

### Task 2: Render Collapsed Accessible Activity Rows

**Files:**
- Modify: `frontend/src/components/ToolCall.vue`
- Modify: `frontend/src/components/ToolCall.test.ts`
- Use: `frontend/src/lib/activityPresentation.ts`
- Use: `frontend/src/components/DiffView.vue`

- [ ] **Step 1: Write failing disclosure tests**

Add component tests asserting:

```ts
const wrapper = mount(ToolCall, { props: { item: tool({ output: 'full output' }) } })
expect(wrapper.get('[data-test="activity-summary"]').attributes('aria-expanded')).toBe('false')
expect(wrapper.text()).not.toContain('full output')
await wrapper.get('[data-test="activity-summary"]').trigger('click')
expect(wrapper.text()).toContain('full output')
```

Add cases for an edit row showing `+2 -1` before expansion and `DiffView` after
expansion, failure marker plus excerpt with full error collapsed, running and
success markers, long accessible labels, and independent expansion state.

- [ ] **Step 2: Run focused component tests and verify RED**

Run: `cd frontend && npm test -- --run src/components/ToolCall.test.ts`

Expected: FAIL because current settled tools expose details immediately and do
not have the activity summary disclosure contract.

- [ ] **Step 3: Implement compact row and disclosure**

Refactor `ToolCall.vue` to:

- derive presentation with `activityPresentation(props.item)`;
- render a status marker using Phosphor icons and text-independent iconography;
- make settled rows with output, error, or diff a button with
  `aria-expanded`, an accessible label, and a visible focus ring;
- keep verbose `<pre>` and `DiffView` behind local `expanded` state;
- keep pending command context and approve/deny buttons visible without
  disclosure;
- use restrained neutral, red, and amber accents rather than card backgrounds.

- [ ] **Step 4: Run focused tests and verify GREEN**

Run: `cd frontend && npm test -- --run src/components/ToolCall.test.ts`

Expected: PASS.

### Task 3: Group Adjacent Tools Into a Continuous Rail

**Files:**
- Create: `frontend/src/components/ActivityGroup.vue`
- Create: `frontend/src/components/ActivityGroup.test.ts`
- Modify: `frontend/src/components/MessageList.vue`
- Modify: `frontend/src/components/MessageList.test.ts`

- [ ] **Step 1: Write failing grouping tests**

Add tests proving two adjacent tools render inside one
`[data-test="activity-group"]`, narrative splits two tools into two groups, and
each group renders one decorative rail with no connector outside its bounds.

Use an internal `TranscriptGroup` computed projection:

```ts
type TranscriptGroup =
  | { kind: 'activity'; id: string; items: ToolItem[] }
  | { kind: 'item'; id: string; item: Exclude<TurnItem, ToolItem> }
```

- [ ] **Step 2: Run grouping tests and verify RED**

Run: `cd frontend && npm test -- --run src/components/ActivityGroup.test.ts src/components/MessageList.test.ts`

Expected: FAIL because adjacent tools currently render as independent siblings.

- [ ] **Step 3: Implement activity grouping and rail ownership**

Implement `ActivityGroup.vue` as a relative list with one absolutely positioned,
`aria-hidden="true"` connector behind its child markers. Render one `ToolCall`
per item and forward approve/deny events unchanged.

In `MessageList.vue`, compute adjacent groups without mutating props, render
non-tool items through their existing components, and render activity groups
through `ActivityGroup`.

- [ ] **Step 4: Run grouping tests and verify GREEN**

Run: `cd frontend && npm test -- --run src/components/ActivityGroup.test.ts src/components/MessageList.test.ts`

Expected: PASS, including existing scrolling and permission forwarding tests.

### Task 4: Triangulate Semantics and Responsive Behavior

**Files:**
- Modify: `frontend/src/lib/activityPresentation.test.ts`
- Modify: `frontend/src/components/ToolCall.test.ts`
- Modify: `frontend/src/components/ActivityGroup.test.ts`

- [ ] **Step 1: Add edge-case tests**

Add cases for empty successful output, malformed/empty input, header-only diff,
grep output that cannot be counted safely, an unknown tool with no scalar input,
a one-item activity group, three-item mixed-status group, and pending permission
buttons inside the group.

- [ ] **Step 2: Run all changed frontend tests**

Run: `cd frontend && npm test -- --run src/lib/activityPresentation.test.ts src/components/ToolCall.test.ts src/components/ActivityGroup.test.ts src/components/MessageList.test.ts`

Expected: PASS; no hardcoded summary or first/last-row assumption survives the
additional cases.

- [ ] **Step 3: Refine only where tests expose gaps**

Keep parsing conservative, preserve one-line truncation in CSS, and avoid adding
backend/store variants for future summary or subagent rows.

### Task 5: Validate Product Quality and Documentation

**Files:**
- Modify if behavior changed during implementation: `.okf/specs/2026-07-11-transcript-activity-hierarchy.md`
- Modify: `.okf/README.md` only if document navigation changes

- [ ] **Step 1: Run frontend quality gates**

Run: `cd frontend && npm test -- --run && npm run build`

Expected: all Vitest tests pass and Vue TypeScript/Vite production build passes.

- [ ] **Step 2: Run repository quality gates**

Run: `gofmt -l . && go vet ./... && go test ./...`

Expected: `gofmt -l .` prints nothing; vet and the full Go suite pass.

- [ ] **Step 3: Inspect the rendered hierarchy**

Run the desktop frontend against representative fixtures and inspect running,
success, failure, diff, permission, single-row, multi-row, and narrative-split
states. Confirm marker alignment, rail termination, truncation, focus styling,
and expanded-content alignment at narrow and wide transcript widths.

- [ ] **Step 4: Record TDD evidence and commit implementation**

Commit source, tests, and any documentation refinements with the required
PostHog Code trailers.

- [ ] **Step 5: Push and open pull request**

Push `posthog-code/transcript-hierarchy` and open a PR describing behavior,
tests, visual verification constraints, and the deferred final-summary variant.
Append the required PostHog Code PR footer.


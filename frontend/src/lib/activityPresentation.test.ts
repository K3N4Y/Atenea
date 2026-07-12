import { describe, expect, it } from 'vitest'
import type { ToolItem } from '../stores/chat'
import {
  activityPresentation,
  type ActivityPresentationKind,
} from './activityPresentation'

function tool(overrides: Partial<ToolItem> = {}): ToolItem {
  return {
    kind: 'tool',
    id: 'tool-1',
    callID: 'call-1',
    name: 'echo',
    input: {},
    status: 'success',
    output: '',
    error: null,
    diff: '',
    ...overrides,
  }
}

describe('activityPresentation', () => {
  it.each([
    ['read', { path: 'internal/auth.go' }, 'internal/auth.go'],
    ['glob', { pattern: 'internal/**/*.go' }, 'internal/**/*.go'],
    ['skill', { name: 'tdd-cycle-evidence' }, 'tdd-cycle-evidence'],
    ['bash', { command: 'go test ./internal/auth' }, 'go test ./internal/auth'],
  ])('derives the %s target from structured input', (name, input, target) => {
    expect(activityPresentation(tool({ name, input })).target).toBe(target)
  })

  it('keeps grep pattern and path together', () => {
    expect(
      activityPresentation(
        tool({
          name: 'grep',
          input: { pattern: 'auth middleware', path: 'internal' },
        }),
      ).target,
    ).toBe('auth middleware · internal')
  })

  it('classifies edit and write as file changes', () => {
    expect(
      activityPresentation(
        tool({ name: 'edit', input: { path: 'internal/auth.go' } }),
      ),
    ).toMatchObject({
      kind: 'file-change',
      action: 'edit',
      target: 'internal/auth.go',
      status: 'success',
    })
  })

  it('counts changed lines without counting diff headers', () => {
    const presentation = activityPresentation(
      tool({
        name: 'edit',
        input: { path: 'internal/auth.go' },
        diff: [
          '--- a/internal/auth.go',
          '+++ b/internal/auth.go',
          '@@ -1,2 +1,3 @@',
          '-old',
          '+new',
          '+extra',
          ' unchanged',
        ].join('\n'),
      }),
    )

    expect(presentation.compactResult).toBe('+2 -1')
    expect(presentation.expandable).toBe(true)
  })

  it('classifies pending tools as permission activity', () => {
    expect(
      activityPresentation(
        tool({
          name: 'bash',
          input: { command: 'go test ./internal/auth' },
          status: 'pending',
        }),
      ),
    ).toMatchObject({
      kind: 'permission',
      target: 'go test ./internal/auth',
      status: 'pending',
      expandable: false,
    })
  })

  it('shows a conservative grep match count', () => {
    expect(
      activityPresentation(tool({ name: 'grep', output: 'one\ntwo\nthree\n' }))
        .compactResult,
    ).toBe('3 matches')
  })

  it('uses the canonical grep result count instead of counting rendered lines', () => {
    expect(
      activityPresentation(
        tool({
          name: 'grep',
          output: 'Found 2 matches\n[foo.go#abc]\n1:first\n2:second',
        }),
      ).compactResult,
    ).toBe('2 matches')
  })

  it('uses singular grammar for one canonical grep match', () => {
    expect(
      activityPresentation(
        tool({ name: 'grep', output: 'Found 1 match\n[foo.go#abc]\n1:first' }),
      ).compactResult,
    ).toBe('1 match')
  })

  it('does not guess a grep count from structured or empty output', () => {
    expect(
      activityPresentation(tool({ name: 'grep', output: '{"matches": 3}' }))
        .compactResult,
    ).toBe('')
    expect(
      activityPresentation(tool({ name: 'grep', output: '' })).compactResult,
    ).toBe('')
  })

  it('normalizes multiline commands to one line', () => {
    expect(
      activityPresentation(
        tool({ name: 'bash', input: { command: 'go test\n./internal/auth' } }),
      ).target,
    ).toBe('go test ./internal/auth')
  })

  it('uses the first useful scalar for unknown tools', () => {
    expect(
      activityPresentation(
        tool({ name: 'custom', input: { enabled: true, query: 'needle' } }),
      ).target,
    ).toBe('true')
  })

  it('shows a one-line error excerpt while keeping the row expandable', () => {
    expect(
      activityPresentation(
        tool({
          status: 'failed',
          error: 'permission denied\nfull stack trace',
        }),
      ),
    ).toMatchObject({
      compactResult: 'permission denied full stack trace',
      expandable: true,
    })
  })

  it('leaves successful empty output without a synthetic result', () => {
    expect(activityPresentation(tool()).compactResult).toBe('')
    expect(activityPresentation(tool()).expandable).toBe(false)
  })

  it('handles malformed input and header-only diffs conservatively', () => {
    expect(
      activityPresentation(tool({ name: 'read', input: null })).target,
    ).toBe('')
    expect(
      activityPresentation(
        tool({ name: 'write', diff: '--- a/file\n+++ b/file\n' }),
      ).compactResult,
    ).toBe('')
  })

  it('counts diff content whose text begins with header marker characters', () => {
    expect(
      activityPresentation(
        tool({
          name: 'write',
          diff: '--- a/file\n+++ b/file\n@@ -1 +1 @@\n---oldValue\n+++newValue\n',
        }),
      ).compactResult,
    ).toBe('+1 -1')
  })

  it('counts hunks across multiple files without counting file headers', () => {
    expect(
      activityPresentation(
        tool({
          name: 'edit',
          diff: [
            'diff --git a/a b/a',
            '--- a/a',
            '+++ b/a',
            '@@ -1 +1 @@',
            '-old',
            '+new',
            'diff --git a/b b/b',
            '--- a/b',
            '+++ b/b',
            '@@ -0,0 +1,2 @@',
            '+first',
            '+second',
          ].join('\n'),
        }),
      ).compactResult,
    ).toBe('+3 -1')
  })

  it('leaves unknown tools without scalar input targetless', () => {
    expect(
      activityPresentation(tool({ name: 'custom', input: { nested: {} } }))
        .target,
    ).toBe('')
  })

  it('reserves future presentation kinds without adding store items', () => {
    const futureKinds: ActivityPresentationKind[] = ['subagent', 'summary']
    expect(futureKinds).toEqual(['subagent', 'summary'])
  })
})

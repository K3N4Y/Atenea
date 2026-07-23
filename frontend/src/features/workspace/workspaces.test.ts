import { describe, expect, it } from 'vitest'
import { knownWorkspaces } from './workspaces'
import type { SessionSummary } from '../chat/types'

const session = (ID: string, Cwd: string): SessionSummary => ({
  ID,
  Title: ID,
  Cwd,
  LastActivity: '2026-07-14T00:00:00Z',
})

describe('knownWorkspaces', () => {
  it('lists distinct folders with the current folder first', () => {
    expect(
      knownWorkspaces(
        [session('a', '/home/u/proj'), session('b', '/home/u/other')],
        '/home/u/proj',
      ),
    ).toEqual([
      { path: '/home/u/proj', label: 'proj' },
      { path: '/home/u/other', label: 'other' },
    ])
  })

  it('includes a current folder without existing sessions', () => {
    expect(knownWorkspaces([], '/home/u/new')).toEqual([
      { path: '/home/u/new', label: 'new' },
    ])
  })

  it('deduplicates folders while preserving backend recency', () => {
    const options = knownWorkspaces(
      [
        session('a', '/home/u/proj'),
        session('b', '/home/u/other'),
        session('c', '/home/u/proj'),
      ],
      '',
    )
    expect(options.map((option) => option.path)).toEqual([
      '/home/u/proj',
      '/home/u/other',
    ])
  })

  it('excludes empty legacy and current folders', () => {
    expect(
      knownWorkspaces([session('a', ''), session('b', '/home/u/proj')], ''),
    ).toEqual([{ path: '/home/u/proj', label: 'proj' }])
  })
})

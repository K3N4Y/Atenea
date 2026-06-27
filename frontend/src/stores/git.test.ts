// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

const GitStatus = vi.fn()
const Commit = vi.fn()

vi.mock('../../wailsjs/go/main/App', () => ({
  GitStatus: () => GitStatus(),
  GenerateCommitMessage: vi.fn(),
  Commit: (msg: string) => Commit(msg),
}))

import { useGitStore } from './git'

beforeEach(() => {
  setActivePinia(createPinia())
  GitStatus.mockReset()
  Commit.mockReset()
  GitStatus.mockResolvedValue({ staged: [], untracked: [] })
  Commit.mockResolvedValue(undefined)
})

describe('git store', () => {
  it('loadStatus trae el estado del backend', async () => {
    GitStatus.mockResolvedValue({
      staged: [{ path: 'a.go', status: 'M' }],
      untracked: [],
    })
    const git = useGitStore()
    await git.loadStatus()
    expect(git.status?.staged).toHaveLength(1)
  })

  // El seam de la devtool: con estado canned inyectado, las acciones NO tocan el
  // repo real (que con `wails dev` es este mismo proyecto).
  it('setCanned hace que loadStatus y commit no llamen al backend', async () => {
    const git = useGitStore()
    git.setCanned({
      staged: [{ path: 'demo.go', status: 'A' }],
      untracked: [],
    })

    await git.loadStatus()
    expect(GitStatus).not.toHaveBeenCalled()
    expect(git.status?.staged[0].path).toBe('demo.go')

    git.message = 'no deberia commitear'
    await git.commit()
    expect(Commit).not.toHaveBeenCalled()
  })
})

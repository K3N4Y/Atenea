// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

const GitStatus = vi.fn()
const Commit = vi.fn()
const InitRepo = vi.fn()

vi.mock('../../wailsjs/go/main/App', () => ({
  GitStatus: () => GitStatus(),
  GenerateCommitMessage: vi.fn(),
  Commit: (msg: string) => Commit(msg),
  InitRepo: () => InitRepo(),
}))

import { useGitStore } from './git'

beforeEach(() => {
  setActivePinia(createPinia())
  GitStatus.mockReset()
  Commit.mockReset()
  InitRepo.mockReset()
  GitStatus.mockResolvedValue({ isRepo: true, staged: [], untracked: [] })
  Commit.mockResolvedValue(undefined)
  InitRepo.mockResolvedValue(undefined)
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

  it('initRepo inicializa el repo y recarga el estado', async () => {
    GitStatus.mockResolvedValueOnce({
      isRepo: false,
      staged: [],
      untracked: [],
    })
    const git = useGitStore()
    await git.loadStatus()
    expect(git.status?.isRepo).toBe(false)

    // Tras iniciar, el siguiente GitStatus ya reporta un repo.
    GitStatus.mockResolvedValue({ isRepo: true, staged: [], untracked: [] })
    await git.initRepo()
    expect(InitRepo).toHaveBeenCalled()
    expect(git.status?.isRepo).toBe(true)
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

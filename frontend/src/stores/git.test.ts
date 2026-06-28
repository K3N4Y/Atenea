// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

const GitStatus = vi.fn()
const Commit = vi.fn()
const InitRepo = vi.fn()
const FileDiff = vi.fn()

vi.mock('../../wailsjs/go/main/App', () => ({
  GitStatus: () => GitStatus(),
  GenerateCommitMessage: vi.fn(),
  Commit: (msg: string) => Commit(msg),
  InitRepo: () => InitRepo(),
  FileDiff: (p: string) => FileDiff(p),
}))

import { useGitStore } from './git'

beforeEach(() => {
  setActivePinia(createPinia())
  GitStatus.mockReset()
  Commit.mockReset()
  InitRepo.mockReset()
  FileDiff.mockReset()
  GitStatus.mockResolvedValue({ isRepo: true, staged: [], untracked: [] })
  Commit.mockResolvedValue(undefined)
  InitRepo.mockResolvedValue(undefined)
  FileDiff.mockResolvedValue('--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n')
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

  it('openDiff trae el diff del archivo y guarda path y contenido', async () => {
    FileDiff.mockResolvedValue(
      '--- a/app.go\n+++ b/app.go\n@@ -1 +1 @@\n-x\n+y\n',
    )
    const git = useGitStore()
    await git.openDiff('app.go')
    expect(FileDiff).toHaveBeenCalledWith('app.go')
    expect(git.diffPath).toBe('app.go')
    expect(git.diff).toContain('+y')
  })

  it('closeDiff limpia el path y el diff', async () => {
    const git = useGitStore()
    await git.openDiff('app.go')
    git.closeDiff()
    expect(git.diffPath).toBe('')
    expect(git.diff).toBe('')
  })

  it('openDiff que falla deja la pantalla cerrada y registra el error', async () => {
    FileDiff.mockRejectedValue(new Error('no such file'))
    const git = useGitStore()
    await git.openDiff('nope.go')
    expect(git.diffPath).toBe('')
    expect(git.error).toContain('no such file')
  })

  it('en canned, openDiff arma un diff sin llamar al backend', async () => {
    const git = useGitStore()
    git.setCanned({ staged: [{ path: 'demo.go', status: 'M' }], untracked: [] })
    await git.openDiff('demo.go')
    expect(FileDiff).not.toHaveBeenCalled()
    expect(git.diffPath).toBe('demo.go')
    expect(git.diff).toContain('demo.go')
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

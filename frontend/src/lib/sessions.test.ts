import { describe, it, expect } from 'vitest'
import { groupSessionsByFolder, knownWorkspaces } from './sessions'
import type { SessionSummary } from '../stores/chat'

const s = (ID: string, Cwd: string): SessionSummary => ({
  ID,
  Title: ID,
  Cwd,
  LastActivity: '2026-07-14T00:00:00Z',
})

// La sidebar agrupa los chats por carpeta de proyecto preservando la recencia.
describe('groupSessionsByFolder', () => {
  it('agrupa por Cwd preservando el orden de entrada entre y dentro de grupos', () => {
    const groups = groupSessionsByFolder([
      s('a', '/home/u/proj'),
      s('b', '/home/u/otro'),
      s('c', '/home/u/proj'),
    ])
    expect(groups.map((g) => g.cwd)).toEqual(['/home/u/proj', '/home/u/otro'])
    expect(groups[0].sessions.map((x) => x.ID)).toEqual(['a', 'c'])
    expect(groups[1].sessions.map((x) => x.ID)).toEqual(['b'])
  })

  it('etiqueta cada grupo con el nombre de la carpeta (basename)', () => {
    const groups = groupSessionsByFolder([s('a', '/home/u/atenea')])
    expect(groups[0].label).toBe('atenea')
  })

  it('agrupa las sesiones viejas sin Cwd bajo una etiqueta de respaldo', () => {
    const groups = groupSessionsByFolder([s('a', '')])
    expect(groups[0].cwd).toBe('')
    expect(groups[0].label).toBe('Sin carpeta')
  })

  it('lista vacia devuelve sin grupos', () => {
    expect(groupSessionsByFolder([])).toEqual([])
  })
})

// El selector de carpeta del chat nuevo ofrece las carpetas que ya tienen chats,
// mas la vigente, para elegir donde trabajara el agente sin abrir el dialogo nativo.
describe('knownWorkspaces', () => {
  it('lista carpetas distintas con la vigente primero y etiqueta por basename', () => {
    const opts = knownWorkspaces(
      [s('a', '/home/u/proj'), s('b', '/home/u/otro')],
      '/home/u/proj',
    )
    expect(opts).toEqual([
      { path: '/home/u/proj', label: 'proj' },
      { path: '/home/u/otro', label: 'otro' },
    ])
  })

  it('incluye la carpeta vigente aunque todavia no tenga chats', () => {
    const opts = knownWorkspaces([], '/home/u/nueva')
    expect(opts).toEqual([{ path: '/home/u/nueva', label: 'nueva' }])
  })

  it('deduplica carpetas repetidas preservando la recencia del backend', () => {
    const opts = knownWorkspaces(
      [s('a', '/home/u/proj'), s('b', '/home/u/otro'), s('c', '/home/u/proj')],
      '',
    )
    expect(opts.map((o) => o.path)).toEqual(['/home/u/proj', '/home/u/otro'])
  })

  it('excluye los chats viejos sin carpeta (Cwd vacio) y la vigente vacia', () => {
    const opts = knownWorkspaces([s('a', ''), s('b', '/home/u/proj')], '')
    expect(opts).toEqual([{ path: '/home/u/proj', label: 'proj' }])
  })
})

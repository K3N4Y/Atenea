import { describe, it, expect } from 'vitest'
import { groupSessionsByFolder } from './sessions'
import type { SessionSummary } from '../../stores/chat'

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

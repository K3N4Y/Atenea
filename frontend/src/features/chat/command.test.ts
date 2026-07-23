import { describe, it, expect } from 'vitest'
import {
  detectCommand,
  filterCommands,
  applyCommand,
  type Command,
} from './command'

describe('detectCommand', () => {
  it('"/" al inicio (query vacia): activo para abrir el menu completo', () => {
    const m = detectCommand('/', 1)
    expect(m).toEqual({ active: true, query: '', start: 0, end: 1 })
  })

  it('escribiendo el nombre: activo con la query', () => {
    const m = detectCommand('/rev', 4)
    expect(m).toEqual({ active: true, query: 'rev', start: 0, end: 4 })
  })

  it('caret a mitad del nombre: query hasta el caret', () => {
    const m = detectCommand('/commit', 4)
    expect(m.active).toBe(true)
    expect(m.query).toBe('com')
  })

  it('un espacio tras el nombre cierra el menu (se escriben args)', () => {
    expect(detectCommand('/commit ', 8).active).toBe(false)
    expect(detectCommand('/commit msg', 11).active).toBe(false)
  })

  it('"/" que no inicia el texto: inactivo (el comando es todo el mensaje)', () => {
    expect(detectCommand('hola /commit', 12).active).toBe(false)
  })

  it('texto sin "/" inicial: inactivo', () => {
    expect(detectCommand('hola', 4).active).toBe(false)
  })

  it('texto vacio: inactivo', () => {
    expect(detectCommand('', 0).active).toBe(false)
  })
})

describe('filterCommands', () => {
  const commands: Command[] = [
    { name: 'commit', description: 'arma el mensaje y commitea' },
    { name: 'code-review', description: 'Revision de codigo' },
    { name: 'deep-research', description: 'investigacion profunda' },
  ]

  it('query vacia: devuelve la cabeza de la lista hasta el limite', () => {
    expect(filterCommands(commands, '', 2)).toHaveLength(2)
  })

  it('filtra por nombre (case-insensitive)', () => {
    const got = filterCommands(commands, 'COMMIT')
    expect(got).toHaveLength(1)
    expect(got[0].name).toBe('commit')
  })

  it('el prefijo del nombre rankea antes que la subcadena', () => {
    const list: Command[] = [
      { name: 'xcommit', description: '' },
      { name: 'commit', description: '' },
    ]
    expect(filterCommands(list, 'commit')[0].name).toBe('commit')
  })

  it('tambien matchea por descripcion cuando el nombre no coincide', () => {
    const got = filterCommands(commands, 'revision')
    expect(got).toHaveLength(1)
    expect(got[0].name).toBe('code-review')
  })

  it('sin coincidencias: lista vacia', () => {
    expect(filterCommands(commands, 'zzz')).toEqual([])
  })

  it('respeta el limite', () => {
    expect(filterCommands(commands, '', 1)).toHaveLength(1)
  })
})

describe('applyCommand', () => {
  it('reemplaza el token por "/nombre " y deja el caret tras el espacio', () => {
    const m = detectCommand('/td', 3)
    const out = applyCommand('/co', m, 'code-review')
    expect(out.text).toBe('/code-review ')
    expect(out.caret).toBe(out.text.length)
  })
})

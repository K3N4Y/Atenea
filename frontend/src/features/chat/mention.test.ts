import { describe, it, expect } from 'vitest'
import { detectMention, filterFiles, applyMention } from './mention'

describe('detectMention', () => {
  it('@ al inicio con texto: token activo con la query', () => {
    const m = detectMention('@int', 4)
    expect(m).toEqual({ active: true, query: 'int', start: 0, end: 4 })
  })

  it('@ recien tecleado (query vacia): activo para abrir el menu completo', () => {
    const m = detectMention('hola @', 6)
    expect(m.active).toBe(true)
    expect(m.query).toBe('')
    expect(m.start).toBe(5)
  })

  it('@ precedido por espacio: activo', () => {
    const m = detectMention('mira @comp', 10)
    expect(m).toEqual({ active: true, query: 'comp', start: 5, end: 10 })
  })

  it('rutas con barra: la query conserva el path parcial', () => {
    const m = detectMention('@internal/tool/gl', 17)
    expect(m.active).toBe(true)
    expect(m.query).toBe('internal/tool/gl')
  })

  it('tipo email (a@b): NO dispara (el @ no inicia palabra)', () => {
    expect(detectMention('correo a@b', 10).active).toBe(false)
  })

  it('espacio entre @ y el caret: inactivo (la mencion termino)', () => {
    expect(detectMention('@foo ', 5).active).toBe(false)
  })

  it('sin @: inactivo', () => {
    expect(detectMention('hola mundo', 10).active).toBe(false)
  })

  it('multilinea: el @ en una linea nueva dispara', () => {
    const m = detectMention('linea1\n@foo', 11)
    expect(m.active).toBe(true)
    expect(m.query).toBe('foo')
    expect(m.start).toBe(7)
  })

  it('usa el @ mas cercano al caret', () => {
    const m = detectMention('@foo @bar', 9)
    expect(m.query).toBe('bar')
    expect(m.start).toBe(5)
  })
})

describe('filterFiles', () => {
  const files = [
    'app.go',
    'internal/tool/glob.go',
    'internal/tool/grep.go',
    'frontend/src/features/chat/ChatComposer.vue',
  ]

  it('query vacia: devuelve la cabeza de la lista hasta el limite', () => {
    expect(filterFiles(files, '', 2)).toEqual([
      'app.go',
      'internal/tool/glob.go',
    ])
  })

  it('filtra por subcadena (case-insensitive)', () => {
    expect(filterFiles(files, 'glob')).toEqual(['internal/tool/glob.go'])
  })

  it('el match por nombre de archivo rankea antes que el match por ruta', () => {
    const list = ['tool/composer.go', 'frontend/ChatComposer.vue']
    // "compose" matchea ambos; el basename que lo contiene gana al match en ruta.
    const got = filterFiles(list, 'composer')
    expect(got[0]).toBe('tool/composer.go')
  })

  it('sin coincidencias: lista vacia', () => {
    expect(filterFiles(files, 'zzz')).toEqual([])
  })

  it('respeta el limite', () => {
    expect(filterFiles(files, 'go', 1)).toHaveLength(1)
  })
})

describe('applyMention', () => {
  it('reemplaza el token por @ruta y un espacio, y posiciona el caret tras el', () => {
    const m = detectMention('@glo', 4)
    const out = applyMention('@glo', m, 'internal/tool/glob.go')
    expect(out.text).toBe('@internal/tool/glob.go ')
    expect(out.caret).toBe(out.text.length)
  })

  it('conserva el texto alrededor del token', () => {
    const text = 'lee @glo y dime'
    const m = detectMention(text, 8) // caret tras "@glo"
    const out = applyMention(text, m, 'glob.go')
    expect(out.text).toBe('lee @glob.go  y dime')
    expect(out.caret).toBe('lee @glob.go '.length)
  })
})

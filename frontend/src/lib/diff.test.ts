import { describe, it, expect } from 'vitest'
import { parseDiff, pathFromDiff, langForPath, buildSideBySide } from './diff'

// Un diff unificado tipico que produce el backend (go-difflib): headers de archivo
// con a/ b/, un header de hunk @@ y el cuerpo con prefijos un caracter.
const SAMPLE = ['--- a/foo.go', '+++ b/foo.go', '@@ -1,3 +1,3 @@', ' a', '-b', '+B', ' c', ''].join('\n')

describe('parseDiff', () => {
  it('clasifica meta, hunk, context, add y del', () => {
    const lines = parseDiff(SAMPLE)
    expect(lines.map((l) => l.type)).toEqual([
      'meta',
      'meta',
      'hunk',
      'context',
      'del',
      'add',
      'context',
    ])
  })

  it('quita exactamente un caracter de prefijo del texto', () => {
    const lines = parseDiff(SAMPLE)
    const del = lines.find((l) => l.type === 'del')
    const add = lines.find((l) => l.type === 'add')
    expect(del?.text).toBe('b')
    expect(add?.text).toBe('B')
  })

  it('una linea borrada que empieza por - conserva su contenido (--foo -> -foo)', () => {
    const raw = ['--- a/x', '+++ b/x', '@@ -1 +0,0 @@', '--foo', ''].join('\n')
    const lines = parseDiff(raw)
    const del = lines.find((l) => l.type === 'del')
    expect(del?.text).toBe('-foo')
  })

  it('una linea de contexto que contiene @@ no es un header de hunk', () => {
    const raw = ['--- a/x', '+++ b/x', '@@ -1,2 +1,2 @@', ' @@ no soy header', '-old', '+new', ''].join(
      '\n',
    )
    const types = parseDiff(raw).map((l) => l.type)
    // solo UN hunk; la linea " @@..." es context
    expect(types.filter((t) => t === 'hunk')).toHaveLength(1)
    expect(types).toContain('context')
  })

  it('archivo nuevo: todo adicion', () => {
    const raw = ['--- a/n.txt', '+++ b/n.txt', '@@ -0,0 +1,2 @@', '+x', '+y', ''].join('\n')
    const types = parseDiff(raw).map((l) => l.type)
    expect(types.filter((t) => t === 'add')).toHaveLength(2)
    expect(types).not.toContain('del')
  })

  it('raw vacio devuelve lista vacia', () => {
    expect(parseDiff('')).toEqual([])
  })
})

describe('buildSideBySide', () => {
  it('una linea de contexto va a ambos lados con sus numeros de linea', () => {
    const rows = buildSideBySide(SAMPLE).filter((r) => r.hunk === null)
    const ctx = rows[0]
    expect(ctx.left).toEqual({ kind: 'context', text: 'a', num: 1 })
    expect(ctx.right).toEqual({ kind: 'context', text: 'a', num: 1 })
  })

  it('un del y un add se emparejan en una fila: del a la izquierda, add a la derecha', () => {
    const rows = buildSideBySide(SAMPLE).filter((r) => r.hunk === null)
    const change = rows[1]
    expect(change.left).toEqual({ kind: 'del', text: 'b', num: 2 })
    expect(change.right).toEqual({ kind: 'add', text: 'B', num: 2 })
  })

  it('el header de hunk es una fila propia (marca el rango @@)', () => {
    const rows = buildSideBySide(SAMPLE)
    expect(rows[0].hunk).toBe('@@ -1,3 +1,3 @@')
  })

  it('mas dels que adds: la fila sobrante deja el lado derecho vacio', () => {
    const raw = ['--- a/x', '+++ b/x', '@@ -1,2 +1,1 @@', '-a', '-b', '+C', ''].join('\n')
    const rows = buildSideBySide(raw).filter((r) => r.hunk === null)
    expect(rows[0].left.kind).toBe('del')
    expect(rows[0].right.kind).toBe('add')
    expect(rows[1].left.kind).toBe('del')
    expect(rows[1].right).toEqual({ kind: 'empty', text: '', num: null })
  })

  it('archivo nuevo (solo adds): cada fila deja el lado izquierdo vacio', () => {
    const raw = ['--- /dev/null', '+++ b/n.txt', '@@ -0,0 +1,2 @@', '+x', '+y', ''].join('\n')
    const rows = buildSideBySide(raw).filter((r) => r.hunk === null)
    expect(rows).toHaveLength(2)
    expect(rows[0].left).toEqual({ kind: 'empty', text: '', num: null })
    expect(rows[0].right).toEqual({ kind: 'add', text: 'x', num: 1 })
    expect(rows[1].right).toEqual({ kind: 'add', text: 'y', num: 2 })
  })

  it('multiples hunks reinician la numeracion segun cada cabecera', () => {
    const raw = [
      '--- a/x',
      '+++ b/x',
      '@@ -1,1 +1,1 @@',
      ' a',
      '@@ -10,1 +20,1 @@',
      ' j',
      '',
    ].join('\n')
    const ctx = buildSideBySide(raw).filter((r) => r.hunk === null)
    expect(ctx[0].left.num).toBe(1)
    expect(ctx[1].left.num).toBe(10)
    expect(ctx[1].right.num).toBe(20)
  })

  it('raw vacio devuelve lista vacia', () => {
    expect(buildSideBySide('')).toEqual([])
  })
})

describe('pathFromDiff', () => {
  it('saca la ruta del header +++ b/<path>', () => {
    expect(pathFromDiff(SAMPLE)).toBe('foo.go')
  })

  it('sin header devuelve cadena vacia', () => {
    expect(pathFromDiff('')).toBe('')
  })
})

describe('langForPath', () => {
  it('mapea extensiones conocidas a lenguajes hljs', () => {
    expect(langForPath('foo.go')).toBe('go')
    expect(langForPath('a/b.ts')).toBe('typescript')
    expect(langForPath('main.py')).toBe('python')
  })

  it('extension desconocida o sin extension cae en plaintext', () => {
    expect(langForPath('Makefile')).toBe('plaintext')
    expect(langForPath('x.unknownext')).toBe('plaintext')
    expect(langForPath('')).toBe('plaintext')
  })
})

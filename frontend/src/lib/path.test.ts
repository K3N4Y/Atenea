import { describe, it, expect } from 'vitest'
import { basename } from './path'

// Tool Read muestra solo el nombre del archivo, nunca la ruta (identidad §10).
describe('basename', () => {
  it('extrae el nombre de una ruta absoluta', () => {
    expect(basename('/home/a/b/c.go')).toBe('c.go')
  })

  it('devuelve el nombre tal cual si ya es un nombre', () => {
    expect(basename('file.ts')).toBe('file.ts')
  })

  it('ignora la barra final', () => {
    expect(basename('/x/y/')).toBe('y')
  })

  it('soporta separadores de Windows', () => {
    expect(basename('a\\b\\c.txt')).toBe('c.txt')
  })

  it('cadena vacia o nula devuelve cadena vacia', () => {
    expect(basename('')).toBe('')
  })
})

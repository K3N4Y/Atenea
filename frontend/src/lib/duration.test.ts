import { describe, it, expect } from 'vitest'
import { formatThinkingDuration } from './duration'

// Reglas del cronometro de pensamiento (identidad §9).
describe('formatThinkingDuration', () => {
  it('menos de 200ms es "briefly"', () => {
    expect(formatThinkingDuration(0)).toBe('briefly')
    expect(formatThinkingDuration(199)).toBe('briefly')
  })

  it('entre 200ms y 999ms muestra los milisegundos', () => {
    expect(formatThinkingDuration(200)).toBe('200ms')
    expect(formatThinkingDuration(450)).toBe('450ms')
    expect(formatThinkingDuration(999)).toBe('999ms')
  })

  it('a partir de 1s usa formato progresivo en segundos', () => {
    expect(formatThinkingDuration(1000)).toBe('1s')
    expect(formatThinkingDuration(5000)).toBe('5s')
    expect(formatThinkingDuration(45000)).toBe('45s')
  })

  it('agrega minutos y horas cuando corresponde', () => {
    expect(formatThinkingDuration(65000)).toBe('1m 5s')
    expect(formatThinkingDuration(185000)).toBe('3m 5s')
    expect(formatThinkingDuration(4513000)).toBe('1h 15m 13s')
    expect(formatThinkingDuration(3600000)).toBe('1h 0m 0s')
  })

  it('valores invalidos caen a "briefly"', () => {
    expect(formatThinkingDuration(Number.NaN)).toBe('briefly')
    expect(formatThinkingDuration(-10)).toBe('briefly')
  })
})

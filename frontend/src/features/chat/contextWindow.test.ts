import { describe, it, expect } from 'vitest'
import {
  contextWindowFor,
  contextPercent,
  formatTokens,
  DEFAULT_CONTEXT_WINDOW,
} from './contextWindow'

// La ventana de contexto por modelo decide la escala de la barra de uso. Un
// modelo conocido trae su tamano real; uno desconocido cae al default para no
// pintar porcentajes absurdos.
describe('contextWindowFor', () => {
  it('un modelo conocido devuelve su ventana real', () => {
    expect(contextWindowFor('anthropic/claude-opus-4.8')).toBe(200000)
  })

  it('un modelo desconocido cae al default', () => {
    expect(contextWindowFor('totally/unknown-model')).toBe(
      DEFAULT_CONTEXT_WINDOW,
    )
  })

  it('el string vacio cae al default', () => {
    expect(contextWindowFor('')).toBe(DEFAULT_CONTEXT_WINDOW)
  })

  // Un modelo de ventana grande devuelve su tamano real, no el default ni un
  // valor unico hardcodeado: el mapa por modelo escala distinto segun el modelo.
  it('un modelo de ventana grande devuelve su tamano real', () => {
    expect(contextWindowFor('google/gemini-2.5-pro')).toBe(1048576)
  })
})

// El porcentaje de contexto usado escala los tokens contra la ventana del modelo.
describe('contextPercent', () => {
  it('la mitad de la ventana es 50%', () => {
    expect(contextPercent(100000, 'anthropic/claude-opus-4.8')).toBe(50)
  })

  it('cero tokens es 0%', () => {
    expect(contextPercent(0, 'anthropic/claude-opus-4.8')).toBe(0)
  })

  // Tokens por encima de la ventana se acotan a 100%: la barra nunca se desborda.
  it('tokens por encima de la ventana se acotan a 100%', () => {
    expect(contextPercent(300000, 'anthropic/claude-opus-4.8')).toBe(100)
  })

  // Tokens no finitos (NaN) caen a 0%: no se pinta un porcentaje basura.
  it('tokens no finitos (NaN) son 0%', () => {
    expect(contextPercent(Number.NaN, 'anthropic/claude-opus-4.8')).toBe(0)
  })
})

// Formato compacto de tokens para la UI: miles como "k".
describe('formatTokens', () => {
  it('cero se muestra tal cual', () => {
    expect(formatTokens(0)).toBe('0')
  })

  it('menos de mil se muestra exacto', () => {
    expect(formatTokens(999)).toBe('999')
  })

  it('miles con decimal usan el sufijo k', () => {
    expect(formatTokens(1500)).toBe('1.5k')
  })

  it('miles redondos no arrastran decimal', () => {
    expect(formatTokens(200000)).toBe('200k')
  })

  // Una fraccion se redondea a un solo decimal (1234 -> "1.2k"), no a la cifra
  // cruda ni a varios decimales.
  it('miles con fraccion se redondean a un decimal', () => {
    expect(formatTokens(1234)).toBe('1.2k')
  })

  // Justo mil es el limite del sufijo k y, por redondo, no arrastra decimal.
  it('mil exacto es "1k"', () => {
    expect(formatTokens(1000)).toBe('1k')
  })
})

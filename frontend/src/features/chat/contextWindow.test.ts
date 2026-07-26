import { describe, it, expect } from 'vitest'
import { contextPercent, formatTokens } from './contextWindow'

// El porcentaje de contexto usado escala los tokens contra la ventana que declara
// el adapter activo y que el backend entrega con la seleccion. Aqui ya no hay tabla
// de modelos: la ventana llega como numero.
describe('contextPercent', () => {
  it('la mitad de la ventana es 50%', () => {
    expect(contextPercent(100000, 200000)).toBe(50)
  })

  it('cero tokens es 0%', () => {
    expect(contextPercent(0, 200000)).toBe(0)
  })

  // Tokens por encima de la ventana se acotan a 100%: la barra nunca se desborda.
  it('tokens por encima de la ventana se acotan a 100%', () => {
    expect(contextPercent(300000, 200000)).toBe(100)
  })

  // Tokens no finitos (NaN) caen a 0%: no se pinta un porcentaje basura.
  it('tokens no finitos (NaN) son 0%', () => {
    expect(contextPercent(Number.NaN, 200000)).toBe(0)
  })

  // Una ventana que nadie declara llega como 0: no hay porcentaje posible, y
  // devolver 0 evita el NaN que una division por cero pintaria en la barra.
  it('una ventana sin declarar es 0%', () => {
    expect(contextPercent(100000, 0)).toBe(0)
  })

  it('una ventana negativa es 0%', () => {
    expect(contextPercent(100000, -1)).toBe(0)
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

import { describe, it, expect } from 'vitest'
import { revealStep } from './reveal'

// revealStep es el nucleo puro del "stream visual": dado cuanto texto falta por
// revelar y cuanto tiempo paso desde el frame anterior, decide cuantos chars
// mostrar en este frame. Cadencia adaptativa: base ~5ms/char (~200 cps) pero
// acelera con el backlog para no quedar muy atras del backend.
describe('revealStep', () => {
  it('no revela nada cuando no falta texto', () => {
    expect(revealStep(0, 16)).toBe(0)
    expect(revealStep(-3, 16)).toBe(0)
  })

  it('mantiene un ritmo base parejo con backlog chico (~4 chars por frame de 16ms)', () => {
    // base = 16/5 = 3.2 -> ceil 4; catchUp = 10/8 = 1.25 (no manda)
    expect(revealStep(10, 16)).toBe(4)
  })

  it('acelera para drenar un backlog grande en ~8 frames', () => {
    // catchUp = 800/8 = 100 domina sobre la base
    expect(revealStep(800, 16)).toBe(100)
    // a ese paso el backlog se vacia en CATCH_UP_FRAMES frames
    expect(Math.ceil(800 / revealStep(800, 16))).toBe(8)
  })

  it('nunca revela mas de lo que falta', () => {
    // base = 3.2 -> 4, pero solo quedan 2
    expect(revealStep(2, 16)).toBe(2)
  })

  it('siempre progresa al menos 1 char cuando queda texto, aun con dt cero o negativo', () => {
    expect(revealStep(5, 0)).toBeGreaterThanOrEqual(1)
    expect(revealStep(5, -50)).toBeGreaterThanOrEqual(1)
  })
})

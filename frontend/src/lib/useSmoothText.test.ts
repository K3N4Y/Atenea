import { describe, it, expect } from 'vitest'
import { ref, effectScope } from 'vue'
import { useSmoothText, type Scheduler } from './useSmoothText'

// Reloj manual: reemplaza requestAnimationFrame/performance.now para manejar el
// tiempo de forma determinista. `tick(dt)` avanza el reloj y dispara el frame
// pendiente con el timestamp nuevo, igual que un rAF real.
function manualClock() {
  let time = 0
  let pending: ((t: number) => void) | null = null
  const scheduler: Scheduler = {
    now: () => time,
    schedule: (cb) => {
      pending = cb
      return 1
    },
    cancel: () => {
      pending = null
    },
  }
  return {
    scheduler,
    tick(dt: number) {
      time += dt
      const cb = pending
      pending = null
      cb?.(time)
    },
  }
}

describe('useSmoothText', () => {
  it('un mensaje ya completo al montar se muestra entero, sin replay', () => {
    const scope = effectScope()
    const out = scope.run(() =>
      useSmoothText(
        () => 'hola',
        () => false,
        manualClock().scheduler,
      ),
    )!

    expect(out.visible.value).toBe('hola')
    expect(out.done.value).toBe(true)
    scope.stop()
  })

  it('escribe el texto de a poco a medida que el reloj avanza', () => {
    const text = ref('')
    const producing = ref(true)
    const clock = manualClock()
    const scope = effectScope()
    const out = scope.run(() => useSmoothText(() => text.value, () => producing.value, clock.scheduler))!

    expect(out.visible.value).toBe('') // producing al montar: arranca vacio

    text.value = 'Hola mundo' // 10 chars
    expect(out.visible.value).toBe('') // todavia no corrio ningun frame

    clock.tick(16)
    expect(out.visible.value.length).toBeGreaterThan(0)
    expect(out.visible.value.length).toBeLessThan(10)
    expect('Hola mundo'.startsWith(out.visible.value)).toBe(true)

    clock.tick(16)
    clock.tick(16)
    expect(out.visible.value).toBe('Hola mundo')
    scope.stop()
  })

  it('done espera a terminar de escribir aunque el backend ya haya terminado', () => {
    const text = ref('')
    const producing = ref(true)
    const clock = manualClock()
    const scope = effectScope()
    const out = scope.run(() => useSmoothText(() => text.value, () => producing.value, clock.scheduler))!

    text.value = 'abcdefghij' // 10 chars
    clock.tick(16) // revela algunos, no todos
    expect(out.visible.value.length).toBeLessThan(10)

    producing.value = false // backend termino (Text.Ended) pero falta drenar
    expect(out.done.value).toBe(false)

    clock.tick(16)
    clock.tick(16)
    expect(out.visible.value).toBe('abcdefghij')
    expect(out.done.value).toBe(true)
    scope.stop()
  })
})

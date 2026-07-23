import { ref, computed, watch, onScopeDispose, type ComputedRef } from 'vue'
import { revealStep } from './reveal'

// useSmoothText es el "stream visual": dado el texto completo que acumula el
// store (`item.text`, que crece con cada Text.Delta) y si el backend sigue
// produciendo (`item.streaming`), revela los caracteres de a poco para que se
// "escriban" suave en vez de aparecer a saltos (ref. upstash.com/blog/
// smooth-streaming). La logica de red (el store) no se toca; esto es solo capa
// de presentacion.

// Scheduler abstrae rAF/performance.now para poder inyectar un reloj manual en
// los tests y manejar el tiempo de forma determinista.
export interface Scheduler {
  now(): number
  schedule(cb: (t: number) => void): unknown
  cancel(handle: unknown): void
}

const rafScheduler: Scheduler = {
  now: () => performance.now(),
  schedule: (cb) => requestAnimationFrame(cb),
  cancel: (h) => cancelAnimationFrame(h as number),
}

export interface SmoothText {
  visible: ComputedRef<string>
  done: ComputedRef<boolean>
}

export function useSmoothText(
  fullText: () => string,
  producing: () => boolean,
  scheduler: Scheduler = rafScheduler,
): SmoothText {
  // Un mensaje que llega ya completo (rehidratado o no-streaming) se muestra
  // entero al instante; uno en vivo arranca en 0 y se va escribiendo.
  const revealed = ref(producing() ? 0 : fullText().length)
  const visible = computed(() => fullText().slice(0, revealed.value))
  // done: el backend termino Y ya revelamos todo. Hasta entonces seguimos
  // mostrando el texto plano con caret; recien aca el componente hace swap a
  // Markdown / colapsa el thinking.
  const done = computed(
    () => !producing() && revealed.value >= fullText().length,
  )

  let handle: unknown = null
  let last = 0

  function frame(t: number): void {
    const remaining = fullText().length - revealed.value
    const step = revealStep(remaining, t - last)
    last = t
    if (step > 0) revealed.value += step
    handle =
      revealed.value < fullText().length ? scheduler.schedule(frame) : null
  }

  // Arranca el loop si hay texto pendiente y no esta corriendo. No hace
  // busy-spin: cuando alcanza el final se detiene y el watch lo reanuda al
  // llegar mas texto.
  function ensureRunning(): void {
    if (handle != null || revealed.value >= fullText().length) return
    last = scheduler.now()
    handle = scheduler.schedule(frame)
  }

  watch([() => fullText().length, producing], ensureRunning, { flush: 'sync' })
  ensureRunning()

  onScopeDispose(() => {
    if (handle != null) scheduler.cancel(handle)
  })

  return { visible, done }
}

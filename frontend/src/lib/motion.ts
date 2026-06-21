// Respeta la preferencia del sistema de reducir movimiento (accesibilidad). En
// entornos sin matchMedia (tests en node/jsdom) devuelve false de forma segura.
export function prefersReducedMotion(): boolean {
  return (
    typeof window !== 'undefined' &&
    typeof window.matchMedia === 'function' &&
    window.matchMedia('(prefers-reduced-motion: reduce)').matches
  )
}

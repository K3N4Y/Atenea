// Nucleo puro del "stream visual" (smooth streaming, ref. upstash.com/blog/
// smooth-streaming): desacopla el ritmo de red del ritmo de lectura. El store
// acumula el texto completo a medida que llegan los Text.Delta; aca decidimos
// cuantos caracteres revelar en cada frame para que el texto se "escriba" suave
// en vez de aparecer a saltos.

// Ritmo base: ~5ms por caracter (~200 cps), igual que el articulo de Upstash.
const MS_PER_CHAR = 5
// Tope de retraso: ante un backlog grande aceleramos para drenarlo en a lo sumo
// este numero de frames, asi el texto visible nunca queda muchos frames atras
// del backend (con modelos rapidos evita que se quede segundos por detras).
const CATCH_UP_FRAMES = 8

// revealStep devuelve cuantos caracteres revelar en este frame.
// - remaining: caracteres que faltan por mostrar (largo total - ya revelado).
// - elapsedMs: tiempo desde el frame anterior.
export function revealStep(remaining: number, elapsedMs: number): number {
  if (remaining <= 0) return 0
  const base = elapsedMs / MS_PER_CHAR
  const catchUp = remaining / CATCH_UP_FRAMES
  const step = Math.ceil(Math.max(base, catchUp))
  return Math.min(remaining, Math.max(1, step))
}

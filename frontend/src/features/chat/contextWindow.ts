// Escala del contexto usado. La ventana NO se decide aca: la declara el adapter
// que sirve el modelo y el backend la entrega con la seleccion activa
// (ActiveProvider.contextWindow). Una tabla mantenida a mano en el frontend era
// una cuarta copia de la misma respuesta, y mentia para todo modelo que no
// estuviera en ella.
//
// Una ventana en 0 significa "nadie la declara": entonces no hay porcentaje que
// mostrar, y eso es mas honesto que escalar contra un numero inventado.

// contextPercent escala los tokens contra la ventana dada: porcentaje 0..100,
// redondeado y acotado. Una ventana no positiva o tokens no finitos dan 0.
export function contextPercent(tokens: number, window: number): number {
  if (window <= 0 || !Number.isFinite(tokens)) return 0
  const pct = Math.round((tokens / window) * 100)
  return Math.min(100, Math.max(0, pct))
}

// formatTokens da un formato compacto: menos de mil tal cual; mil o mas en "k",
// sin decimal si es redondo (200000 -> "200k") y con un decimal si no (1500 -> "1.5k").
export function formatTokens(n: number): string {
  if (n < 1000) return String(n)
  const k = n / 1000
  return Number.isInteger(k) ? `${k}k` : `${k.toFixed(1)}k`
}

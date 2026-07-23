// Formato del cronometro de pensamiento (identidad §9):
// - <200ms: "briefly"
// - 200..999ms: milisegundos exactos ("450ms")
// - >=1s: formato progresivo en h/m/s ("3m 5s", "1h 15m 13s")
export function formatThinkingDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 200) return 'briefly'
  if (ms < 1000) return `${Math.round(ms)}ms`

  const total = Math.floor(ms / 1000)
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60

  if (h > 0) return `${h}h ${m}m ${s}s`
  if (m > 0) return `${m}m ${s}s`
  return `${s}s`
}

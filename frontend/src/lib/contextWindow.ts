// Ventana de contexto por modelo: decide la escala de la barra de uso. La app
// apunta a la familia Claude via OpenRouter, asi que el default es conservador.
export const DEFAULT_CONTEXT_WINDOW = 200_000

// Mapa mantenido a mano de id de modelo OpenRouter -> tamano de ventana de
// contexto (tokens). Se extiende a medida que se usan modelos nuevos; un modelo
// que no este aqui cae al default.
const WINDOWS: Record<string, number> = {
  'anthropic/claude-opus-4.8': 200_000,
  'anthropic/claude-sonnet-4.5': 200_000,
  'anthropic/claude-3.5-sonnet': 200_000,
  'openai/gpt-4o': 128_000,
  'google/gemini-2.5-pro': 1_048_576,
}

// contextWindowFor devuelve la ventana del modelo conocido o el default.
export function contextWindowFor(model: string): number {
  return WINDOWS[model] ?? DEFAULT_CONTEXT_WINDOW
}

// contextPercent escala los tokens contra la ventana del modelo: porcentaje
// 0..100, redondeado y acotado. Una ventana no positiva o tokens no finitos dan 0.
export function contextPercent(tokens: number, model: string): number {
  const window = contextWindowFor(model)
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

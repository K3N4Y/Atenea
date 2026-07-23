import { basename } from '../../lib/path'

// @-menciones de archivos en el composer (logica pura, sin DOM). detectMention
// lee el token bajo el caret; filterFiles ordena candidatos; applyMention inserta
// la ruta elegida. ChatComposer las orquesta con el textarea y el menu.

export interface MentionQuery {
  // active = hay un token @ vigente bajo el caret que debe abrir el menu.
  active: boolean
  // query = texto entre el @ y el caret (lo que se usa para filtrar).
  query: string
  // start = indice del @; end = posicion del caret (fin exclusivo del token).
  start: number
  end: number
}

const INACTIVE: MentionQuery = { active: false, query: '', start: -1, end: -1 }

// detectMention busca un token @ que termina en el caret. Es activo cuando hay
// un @ antes del caret sin espacios en medio y el @ inicia palabra (inicio del
// texto o precedido por espacio), para que un email como a@b no dispare. La
// query es el texto entre el @ y el caret; conserva las barras de una ruta.
export function detectMention(text: string, caret: number): MentionQuery {
  if (caret < 0 || caret > text.length) return INACTIVE
  let i = caret - 1
  while (i >= 0) {
    const ch = text[i]
    if (ch === '@') break
    if (/\s/.test(ch)) return INACTIVE
    i--
  }
  if (i < 0 || text[i] !== '@') return INACTIVE
  const before = i > 0 ? text[i - 1] : ''
  if (before && !/\s/.test(before)) return INACTIVE
  return { active: true, query: text.slice(i + 1, caret), start: i, end: caret }
}

// filterFiles ordena rutas contra una query (sin distinguir mayusculas). Query
// vacia devuelve la cabeza de la lista. Si no, conserva las rutas que contienen
// la query, rankeando el match por nombre de archivo (y por prefijo) antes que
// el match en una ruta mas profunda; desempata por ruta mas corta. Acota a limit.
export function filterFiles(
  files: string[],
  query: string,
  limit = 10,
): string[] {
  if (limit <= 0) return []
  const q = query.toLowerCase()
  if (!q) return files.slice(0, limit)
  const scored: { path: string; score: number }[] = []
  for (const path of files) {
    const lower = path.toLowerCase()
    const base = basename(lower)
    let score: number
    if (base.startsWith(q)) score = 0
    else if (base.includes(q)) score = 1
    else if (lower.includes(q)) score = 2
    else continue
    scored.push({ path, score })
  }
  scored.sort((a, b) => a.score - b.score || a.path.length - b.path.length)
  return scored.slice(0, limit).map((s) => s.path)
}

// applyMention reemplaza el token @ vigente por "@<ruta> " (con espacio final) y
// devuelve el texto nuevo y el caret justo despues, listo para seguir escribiendo.
export function applyMention(
  text: string,
  m: MentionQuery,
  path: string,
): { text: string; caret: number } {
  const insert = `@${path} `
  const next = text.slice(0, m.start) + insert + text.slice(m.end)
  return { text: next, caret: m.start + insert.length }
}

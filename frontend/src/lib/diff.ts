// Parser de diffs unificados (los que arma el backend con go-difflib) para
// renderizar edit/write con colores. No computa diffs: solo clasifica las lineas
// del texto ya producido por Go.

export type DiffLineType = 'meta' | 'hunk' | 'context' | 'add' | 'del'

export interface DiffLine {
  type: DiffLineType
  text: string
}

// parseDiff clasifica cada linea del diff. Maquina de estados por orden de
// documento: los headers de archivo (--- / +++) solo aparecen ANTES del primer
// hunk (@@). Dentro de un hunk cada linea se clasifica por su unico caracter de
// prefijo (' '/'+'/'-'), nunca por startsWith de dos caracteres: asi una linea
// borrada "--foo" (prefijo '-' + contenido "-foo") no se confunde con un header.
export function parseDiff(raw: string): DiffLine[] {
  if (!raw) return []

  const lines = raw.split('\n')
  if (lines.length && lines[lines.length - 1] === '') lines.pop() // "\n" final

  const out: DiffLine[] = []
  let inHunk = false
  for (const line of lines) {
    if (line.startsWith('@@')) {
      out.push({ type: 'hunk', text: line })
      inHunk = true
      continue
    }
    if (!inHunk) {
      out.push({ type: 'meta', text: line })
      continue
    }
    const prefix = line[0]
    if (prefix === '+') out.push({ type: 'add', text: line.slice(1) })
    else if (prefix === '-') out.push({ type: 'del', text: line.slice(1) })
    else out.push({ type: 'context', text: line.slice(1) }) // ' ' o linea vacia
  }
  return out
}

// Modelo side-by-side (estilo VSCode) derivado del MISMO diff unificado. Cada
// fila tiene dos celdas (vieja | nueva); una celda 'empty' rellena cuando un lado
// tiene mas lineas que el otro. Las filas de hunk (`hunk` != null) separan rangos.
export type DiffCellKind = 'context' | 'add' | 'del' | 'empty'

export interface DiffCell {
  kind: DiffCellKind
  text: string
  num: number | null // numero de linea (1-based) o null en celdas vacias
}

export interface DiffRow {
  left: DiffCell
  right: DiffCell
  hunk: string | null // texto del header @@ cuando la fila es un separador de hunk
}

function emptyCell(): DiffCell {
  return { kind: 'empty', text: '', num: null }
}

// buildSideBySide arma las filas de la vista a dos columnas a partir del diff
// unificado. Acumula los bloques de cambios (dels seguidos de adds) y al cerrarlos
// (al llegar una linea de contexto, un nuevo hunk o el final) empareja del[i] con
// add[i]; el lado mas corto queda con celdas vacias. Los numeros de linea salen
// del header @@ -viejo +nuevo y avanzan por lado.
export function buildSideBySide(raw: string): DiffRow[] {
  if (!raw) return []
  const lines = raw.split('\n')
  if (lines.length && lines[lines.length - 1] === '') lines.pop()

  const rows: DiffRow[] = []
  let oldNum = 0
  let newNum = 0
  let inHunk = false
  let dels: DiffCell[] = []
  let adds: DiffCell[] = []

  const flush = () => {
    const n = Math.max(dels.length, adds.length)
    for (let i = 0; i < n; i++) {
      rows.push({
        left: dels[i] ?? emptyCell(),
        right: adds[i] ?? emptyCell(),
        hunk: null,
      })
    }
    dels = []
    adds = []
  }

  for (const line of lines) {
    if (line.startsWith('@@')) {
      flush()
      const m = /@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(line)
      oldNum = m ? parseInt(m[1], 10) : 1
      newNum = m ? parseInt(m[2], 10) : 1
      rows.push({ left: emptyCell(), right: emptyCell(), hunk: line })
      inHunk = true
      continue
    }
    if (!inHunk) continue // headers --- / +++ : el archivo va en la cabecera de la pantalla
    const prefix = line[0]
    const text = line.slice(1)
    if (prefix === '+') {
      adds.push({ kind: 'add', text, num: newNum++ })
    } else if (prefix === '-') {
      dels.push({ kind: 'del', text, num: oldNum++ })
    } else {
      // context: cierra el bloque de cambios pendiente y alinea ambos lados
      flush()
      rows.push({
        left: { kind: 'context', text, num: oldNum++ },
        right: { kind: 'context', text, num: newNum++ },
        hunk: null,
      })
    }
  }
  flush()
  return rows
}

// pathFromDiff saca la ruta del header "+++ b/<path>" (cae al "--- a/<path>" si
// hiciera falta). El backend no pone fechas, asi que no hay sufijo de tab.
export function pathFromDiff(raw: string): string {
  for (const line of raw.split('\n')) {
    if (line.startsWith('+++ ')) return stripABPrefix(line.slice(4))
    if (line.startsWith('--- ')) return stripABPrefix(line.slice(4))
  }
  return ''
}

function stripABPrefix(p: string): string {
  if (p.startsWith('a/') || p.startsWith('b/')) return p.slice(2)
  return p
}

// langForPath mapea la extension a un lenguaje de highlight.js. Lo desconocido cae
// en 'plaintext' (lenguaje valido de hljs que solo escapa). DiffView vuelve a
// validar con hljs.getLanguage antes de resaltar.
const EXT_LANG: Record<string, string> = {
  go: 'go',
  ts: 'typescript',
  tsx: 'typescript',
  js: 'javascript',
  jsx: 'javascript',
  mjs: 'javascript',
  cjs: 'javascript',
  py: 'python',
  rs: 'rust',
  java: 'java',
  c: 'c',
  h: 'c',
  cpp: 'cpp',
  cc: 'cpp',
  cs: 'csharp',
  rb: 'ruby',
  php: 'php',
  swift: 'swift',
  kt: 'kotlin',
  json: 'json',
  yaml: 'yaml',
  yml: 'yaml',
  toml: 'ini',
  ini: 'ini',
  md: 'markdown',
  html: 'xml',
  xml: 'xml',
  css: 'css',
  scss: 'scss',
  sh: 'bash',
  bash: 'bash',
  sql: 'sql',
}

export function langForPath(path: string): string {
  const name = path.split(/[/\\]/).pop() ?? ''
  const dot = name.lastIndexOf('.')
  if (dot <= 0) return 'plaintext' // sin extension o dotfile
  return EXT_LANG[name.slice(dot + 1).toLowerCase()] ?? 'plaintext'
}

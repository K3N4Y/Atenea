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

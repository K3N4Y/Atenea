// Devuelve solo el nombre del archivo de una ruta, nunca la ruta completa
// (identidad §10). Soporta separadores POSIX y Windows.
export function basename(path: string): string {
  if (!path) return ''
  const parts = path.split(/[/\\]/).filter(Boolean)
  return parts.length ? parts[parts.length - 1] : ''
}

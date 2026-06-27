import { basename } from './path'
import type { SessionSummary } from '../stores/chat'

// Un grupo de chats de la sidebar: todos los de una misma carpeta de trabajo.
export interface SessionGroup {
  cwd: string // ruta completa de la carpeta ('' = sesiones viejas sin carpeta)
  label: string // nombre corto para el encabezado (basename de cwd)
  sessions: SessionSummary[]
}

// groupSessionsByFolder agrupa los chats por su carpeta de trabajo (Cwd),
// preservando el orden de entrada (el backend ya los da por recencia) tanto entre
// grupos —por la primera aparicion de cada carpeta— como dentro de cada grupo. La
// etiqueta es el basename de la ruta; '' (sesiones anteriores a la captura de
// carpeta) cae a 'Sin carpeta'.
export function groupSessionsByFolder(
  sessions: SessionSummary[],
): SessionGroup[] {
  const groups: SessionGroup[] = []
  const byCwd = new Map<string, SessionGroup>()
  for (const session of sessions) {
    const cwd = session.Cwd ?? ''
    let group = byCwd.get(cwd)
    if (!group) {
      group = { cwd, label: basename(cwd) || 'Sin carpeta', sessions: [] }
      byCwd.set(cwd, group)
      groups.push(group)
    }
    group.sessions.push(session)
  }
  return groups
}

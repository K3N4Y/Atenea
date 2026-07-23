import { basename } from '../../lib/path'
import type { SessionSummary } from '../../stores/chat'

export interface WorkspaceOption {
  path: string
  label: string
}

// Lists known working folders with the current folder first, preserving the
// backend's session-recency order and removing duplicates and empty paths.
export function knownWorkspaces(
  sessions: SessionSummary[],
  current: string,
): WorkspaceOption[] {
  const options: WorkspaceOption[] = []
  const seen = new Set<string>()
  const add = (path: string) => {
    if (!path || seen.has(path)) return
    seen.add(path)
    options.push({ path, label: basename(path) || path })
  }

  add(current)
  for (const session of sessions) add(session.Cwd ?? '')
  return options
}

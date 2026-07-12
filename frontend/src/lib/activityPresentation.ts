import type { ToolItem, ToolStatus } from '../stores/chat'

export type ActivityPresentationKind =
  | 'tool'
  | 'permission'
  | 'file-change'
  | 'subagent'
  | 'summary'

export interface ActivityPresentation {
  kind: ActivityPresentationKind
  action: string
  target: string
  compactResult: string
  status: ToolStatus
  expandable: boolean
  accessibleLabel: string
}

function oneLine(value: string): string {
  return value.replace(/\s+/g, ' ').trim()
}

function excerpt(value: string, maxLength = 160): string {
  const normalized = oneLine(value)
  return normalized.length > maxLength
    ? `${normalized.slice(0, maxLength - 1).trimEnd()}…`
    : normalized
}

function inputObject(input: unknown): Record<string, unknown> {
  return input && typeof input === 'object'
    ? (input as Record<string, unknown>)
    : {}
}

function stringValue(
  input: Record<string, unknown>,
  ...keys: string[]
): string {
  for (const key of keys) {
    const value = input[key]
    if (typeof value === 'string' && value.trim()) return oneLine(value)
  }
  return ''
}

function firstScalar(input: Record<string, unknown>): string {
  for (const value of Object.values(input)) {
    if (
      typeof value === 'string' ||
      typeof value === 'number' ||
      typeof value === 'boolean'
    ) {
      return oneLine(String(value))
    }
  }
  return ''
}

function targetFor(item: ToolItem): string {
  const input = inputObject(item.input)

  switch (item.name) {
    case 'read':
    case 'edit':
    case 'write':
      return stringValue(input, 'path', 'file_path', 'filename', 'file')
    case 'grep': {
      const pattern = stringValue(input, 'pattern', 'query')
      const path = stringValue(input, 'path')
      return pattern && path ? `${pattern} · ${path}` : pattern || path
    }
    case 'glob':
      return stringValue(input, 'pattern')
    case 'bash':
      return stringValue(input, 'command')
    case 'skill':
      return stringValue(input, 'name')
    default:
      return firstScalar(input)
  }
}

function diffSummary(diff: string): string {
  let additions = 0
  let deletions = 0
  let inHunk = false

  for (const line of diff.split('\n')) {
    if (line.startsWith('@@')) {
      inHunk = true
      continue
    }
    if (line.startsWith('diff --git ')) {
      inHunk = false
      continue
    }
    if (!inHunk) continue
    if (line.startsWith('+')) additions++
    if (line.startsWith('-')) deletions++
  }

  return additions || deletions ? `+${additions} -${deletions}` : ''
}

function grepSummary(output: string): string {
  const trimmed = output.trim()
  if (!trimmed || trimmed.startsWith('{') || trimmed.startsWith('[')) return ''
  const canonicalCount = /^Found (\d+) match(?:es)?\b/.exec(trimmed)
  if (canonicalCount) {
    const count = Number(canonicalCount[1])
    return `${count} ${count === 1 ? 'match' : 'matches'}`
  }
  const lines = trimmed.split('\n').filter((line) => line.trim())
  return lines.length
    ? `${lines.length} ${lines.length === 1 ? 'match' : 'matches'}`
    : ''
}

function compactResultFor(item: ToolItem): string {
  if (item.status === 'failed' && item.error) return excerpt(item.error)
  if (item.diff) return diffSummary(item.diff)
  if (item.name === 'grep') return grepSummary(item.output)
  return ''
}

function kindFor(item: ToolItem): ActivityPresentationKind {
  if (item.status === 'pending') return 'permission'
  if (item.name === 'edit' || item.name === 'write') return 'file-change'
  return 'tool'
}

function statusLabel(status: ToolStatus): string {
  switch (status) {
    case 'pending':
      return 'permission required'
    case 'running':
      return 'running'
    case 'success':
      return 'succeeded'
    case 'failed':
      return 'failed'
  }
}

export function activityPresentation(item: ToolItem): ActivityPresentation {
  const target = targetFor(item)
  const compactResult = compactResultFor(item)
  const accessibleLabel = [
    item.name,
    target,
    statusLabel(item.status),
    compactResult,
  ]
    .filter(Boolean)
    .join(', ')

  return {
    kind: kindFor(item),
    action: item.name,
    target,
    compactResult,
    status: item.status,
    expandable: Boolean(item.diff || item.error || item.output),
    accessibleLabel,
  }
}

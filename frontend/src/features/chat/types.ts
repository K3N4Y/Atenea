export type ToolStatus = 'pending' | 'running' | 'success' | 'failed'

export interface UserItem {
  kind: 'user'
  id: string
  text: string
}

export interface AssistantItem {
  kind: 'assistant'
  id: string
  text: string
  streaming: boolean
}

export interface ReasoningItem {
  kind: 'reasoning'
  id: string
  text: string
  streaming: boolean
  durationMs: number | null
}

export interface ToolItem {
  kind: 'tool'
  id: string
  callID: string
  name: string
  input: unknown
  status: ToolStatus
  output: string
  error: string | null
  diff: string
}

export type TurnItem = UserItem | AssistantItem | ReasoningItem | ToolItem

export interface PlanState {
  callID: string
  title: string
  markdown: string
}

export type TodoStatus = 'pending' | 'in_progress' | 'completed'

export interface TodoItem {
  content: string
  status: TodoStatus
}

export interface Usage {
  inputTokens: number
  outputTokens: number
  reasoningTokens: number
  cacheReadTokens: number
  cacheWriteTokens: number
}

// Durable session event shape serialized by the Wails adapter.
export interface SessionEvent {
  Kind?: string
  Text?: string
  Error?: string
  CallID?: string
  ToolName?: string
  Input?: unknown
  Diff?: string
  SessionID?: string
  Message?: { Role?: string; Text?: string }
  Usage?: {
    InputTokens: number
    OutputTokens: number
    ReasoningTokens: number
    CacheReadTokens: number
    CacheWriteTokens: number
  }
}

export interface SessionSummary {
  ID: string
  Title: string
  Cwd: string
  LastActivity: string
}

// Legacy persisted shape used only while migrating old MCP configuration.
export interface MCPServerConfig {
  name: string
  command: string
  args: string[]
}

import { ref, type Ref } from 'vue'
import {
  DeleteSession,
  ListSessions,
  SessionHistory,
  SetWorkspace,
} from '../../../wailsjs/go/main/App'
import type { SessionEvent, SessionSummary } from '../chat/types'

interface SessionStateOptions {
  sessionID: Ref<string>
  workspace: Ref<string>
  resetChat: () => void
  clearLog: () => void
  isSubscribed: () => boolean
  teardown: () => void
  subscribe: () => void
  applyEvent: (event: SessionEvent) => void
  refreshWorkspaceResources: () => Promise<unknown>
}

// Owns session discovery, deletion, and durable-history rehydration while the
// chat store supplies the rendering and subscription effects that belong to its
// live event lifecycle. The backend remains the sole source of truth.
export function createSessionState({
  sessionID,
  workspace,
  resetChat,
  clearLog,
  isSubscribed,
  teardown,
  subscribe,
  applyEvent,
  refreshWorkspaceResources,
}: SessionStateOptions) {
  const sessions = ref<SessionSummary[]>([])

  async function loadSessions(): Promise<void> {
    sessions.value = await ListSessions()
  }

  async function deleteSession(id: string): Promise<void> {
    await DeleteSession(id)
    if (id === sessionID.value) resetChat()
    await loadSessions()
  }

  async function loadSession(id: string): Promise<void> {
    const summary = sessions.value.find((session) => session.ID === id)
    if (summary?.Cwd && summary.Cwd !== workspace.value) {
      await SetWorkspace(summary.Cwd)
      workspace.value = summary.Cwd
      await refreshWorkspaceResources()
    }

    const wasSubscribed = isSubscribed()
    teardown()
    sessionID.value = id
    clearLog()
    if (wasSubscribed) subscribe()

    const history = await SessionHistory(id)
    for (const event of history) applyEvent(event)
  }

  return { sessions, loadSessions, loadSession, deleteSession }
}

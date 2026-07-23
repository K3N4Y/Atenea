import { ref } from 'vue'
import {
  SelectWorkspace,
  SetWorkspace,
  Workspace,
} from '../../../wailsjs/go/main/App'

interface WorkspaceStateOptions {
  resetChat: () => void
  loadSessions: () => Promise<void>
}

// Owns working-folder selection and restoration while allowing the chat Pinia
// store to expose and persist the workspace ref under its existing contract.
// Chat injects the two follow-up effects that belong to its own lifecycle.
export function createWorkspaceState({
  resetChat,
  loadSessions,
}: WorkspaceStateOptions) {
  const workspace = ref('')

  async function loadWorkspace(): Promise<void> {
    try {
      workspace.value = await Workspace()
    } catch {
      workspace.value = ''
    }
  }

  async function selectWorkspace(): Promise<void> {
    const dir = await SelectWorkspace()
    if (dir && dir !== workspace.value) {
      workspace.value = dir
      resetChat()
    }
    await loadSessions()
  }

  async function pickWorkspace(path: string): Promise<void> {
    if (!path || path === workspace.value) return
    await SetWorkspace(path)
    workspace.value = path
    resetChat()
  }

  async function restoreWorkspace(): Promise<void> {
    if (workspace.value) {
      try {
        await SetWorkspace(workspace.value)
        return
      } catch {
        // A stale persisted folder falls back to the backend workspace.
      }
    }
    await loadWorkspace()
  }

  return {
    workspace,
    loadWorkspace,
    selectWorkspace,
    pickWorkspace,
    restoreWorkspace,
  }
}

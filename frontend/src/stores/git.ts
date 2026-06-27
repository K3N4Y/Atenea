import { ref } from 'vue'
import { defineStore } from 'pinia'
import { GitStatus, GenerateCommitMessage, Commit } from '../../wailsjs/go/main/App'

// Estado de la tab Git de las dev tools: los cambios staged/untracked, el mensaje
// del commit y los flags de carga. Las acciones hablan con el backend (bindings
// Wails). No se persiste: es estado efimero derivado del repo.
type GitChange = { path: string; status: string }
type GitStatus = { staged: GitChange[]; untracked: GitChange[] }

export const useGitStore = defineStore('git', () => {
  const status = ref<GitStatus | null>(null)
  const message = ref('')
  const generating = ref(false)
  const committing = ref(false)
  const error = ref('')

  // canned: la devtool (DevEventPanel) inyecto un estado de ejemplo para iterar la
  // UI sin repo ni backend. Mientras este activo, las acciones NO tocan el repo
  // real (que con `wails dev` apunta a este mismo proyecto). Se limpia recargando
  // la app. ponytail: solo-dev; en produccion siempre es false.
  const canned = ref(false)

  async function loadStatus() {
    if (canned.value) return
    try {
      status.value = await GitStatus()
    } catch (e) {
      error.value = String(e)
    }
  }

  async function generate() {
    if (canned.value) {
      message.value = 'feat: mensaje de ejemplo'
      return
    }
    generating.value = true
    error.value = ''
    try {
      message.value = await GenerateCommitMessage()
    } catch (e) {
      error.value = String(e)
    } finally {
      generating.value = false
    }
  }

  async function commit() {
    if (canned.value) {
      message.value = ''
      return
    }
    committing.value = true
    error.value = ''
    try {
      await Commit(message.value)
      message.value = ''
      await loadStatus()
    } catch (e) {
      error.value = String(e)
    } finally {
      committing.value = false
    }
  }

  // setCanned inyecta un estado de git de ejemplo (devtool): marca canned para que
  // loadStatus/commit no pisen el ejemplo ni toquen el repo real. err simula el
  // estado de error del panel.
  function setCanned(s: GitStatus | null, err = '') {
    canned.value = true
    status.value = s
    error.value = err
    message.value = ''
  }

  return {
    status,
    message,
    generating,
    committing,
    error,
    loadStatus,
    generate,
    commit,
    setCanned,
  }
})

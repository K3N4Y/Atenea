import { ref } from 'vue'
import { defineStore } from 'pinia'
import {
  GitStatus,
  GenerateCommitMessage,
  Commit,
  InitRepo,
  FileDiff,
} from '../../wailsjs/go/main/App'

// Estado de la tab Git de las dev tools: los cambios staged/untracked, el mensaje
// del commit y los flags de carga. Las acciones hablan con el backend (bindings
// Wails). No se persiste: es estado efimero derivado del repo.
type GitChange = { path: string; status: string }
type GitStatus = {
  isRepo: boolean
  staged: GitChange[]
  unstaged: GitChange[]
  untracked: GitChange[]
}

export const useGitStore = defineStore('git', () => {
  const status = ref<GitStatus | null>(null)
  const message = ref('')
  const generating = ref(false)
  const committing = ref(false)
  const initializing = ref(false)
  const error = ref('')

  // canned: la devtool (DevEventPanel) inyecto un estado de ejemplo para iterar la
  // UI sin repo ni backend. Mientras este activo, las acciones NO tocan el repo
  // real (que con `wails dev` apunta a este mismo proyecto). Se limpia recargando
  // la app. ponytail: solo-dev; en produccion siempre es false.
  const canned = ref(false)

  // Pantalla de diff: el archivo abierto (diffPath != '' => pantalla visible) y su
  // diff unificado. La vista monta DiffScreen cuando diffPath no esta vacio.
  const diffPath = ref('')
  const diff = ref('')

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

  // initRepo inicializa un repo git en el proyecto (boton del panel cuando no hay
  // repo) y recarga el estado. En canned simula que el proyecto ya es un repo
  // vacio, para iterar la UI sin backend.
  async function initRepo() {
    if (canned.value) {
      status.value = { isRepo: true, staged: [], unstaged: [], untracked: [] }
      error.value = ''
      return
    }
    initializing.value = true
    error.value = ''
    try {
      await InitRepo()
      await loadStatus()
    } catch (e) {
      error.value = String(e)
    } finally {
      initializing.value = false
    }
  }

  // openDiff trae el diff del archivo y, si llega, abre la pantalla (diffPath). El
  // diff local es rapido, asi que esperamos el resultado y abrimos con el contenido
  // ya cargado (sin estado de carga). Un fallo NO abre la pantalla: deja el error a
  // la vista del panel. En canned arma un diff de ejemplo sin tocar el backend.
  async function openDiff(path: string) {
    if (canned.value) {
      diff.value = cannedDiffFor(path)
      diffPath.value = path
      return
    }
    error.value = ''
    try {
      diff.value = await FileDiff(path)
      diffPath.value = path
    } catch (e) {
      error.value = String(e)
    }
  }

  // closeDiff cierra la pantalla de diff (boton/Escape de DiffScreen).
  function closeDiff() {
    diffPath.value = ''
    diff.value = ''
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
    initializing,
    error,
    diffPath,
    diff,
    loadStatus,
    generate,
    commit,
    initRepo,
    openDiff,
    closeDiff,
    setCanned,
  }
})

// cannedDiffFor arma un diff de ejemplo para el modo canned (devtool): permite
// iterar la pantalla DiffScreen sin repo ni backend. ponytail: solo-dev.
function cannedDiffFor(path: string): string {
  return [
    `--- a/${path}`,
    `+++ b/${path}`,
    '@@ -1,4 +1,4 @@',
    ' contexto sin cambios',
    '-linea vieja',
    '+linea nueva',
    ' otra linea',
    '+linea agregada',
    '',
  ].join('\n')
}

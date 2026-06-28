// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import piniaPluginPersistedstate from 'pinia-plugin-persistedstate'

// La carpeta de trabajo del chat nuevo debe sobrevivir al cierre de la app: si el
// ultimo chat fue en /kanban, al reabrir un chat nuevo sigue apuntando a /kanban y
// no a la carpeta por defecto. Solo se persiste `workspace` (el historial es del
// backend); el resto del store es estado vivo que no debe ir a localStorage.
//
// Como en ui.test.ts, los plugins de pinia solo corren con pinia instalado en una
// app Vue, asi que montamos una app minima para activar la persistencia.
vi.mock('../../wailsjs/go/main/App', () => ({
  SendPrompt: vi.fn(() => Promise.resolve()),
  SendPlanPrompt: vi.fn(() => Promise.resolve()),
  AcceptPlan: vi.fn(() => Promise.resolve()),
  Stop: vi.fn(),
  ResolveToolPermission: vi.fn(),
  ListSessions: vi.fn(() => Promise.resolve([])),
  SessionHistory: vi.fn(() => Promise.resolve([])),
  DeleteSession: vi.fn(() => Promise.resolve()),
  Model: vi.fn(() => Promise.resolve('m')),
  ListProjectFiles: vi.fn(() => Promise.resolve([])),
  ListCommands: vi.fn(() => Promise.resolve([])),
  Workspace: vi.fn(() => Promise.resolve('/home/u/a')),
  SetWorkspace: vi.fn(() => Promise.resolve()),
  SelectWorkspace: vi.fn(() => Promise.resolve('')),
}))
vi.mock('../../wailsjs/runtime/runtime', () => ({
  EventsOn: vi.fn(() => () => {}),
}))

import { useChatStore } from './chat'

function installPinia() {
  const app = createApp({ render: () => null })
  const pinia = createPinia()
  pinia.use(piniaPluginPersistedstate)
  app.use(pinia)
  setActivePinia(pinia)
}

beforeEach(() => {
  localStorage.clear()
  vi.clearAllMocks()
})

describe('chat store: persistencia de la carpeta entre reinicios', () => {
  it('rehidrata workspace desde localStorage al iniciar', () => {
    localStorage.setItem(
      'chat',
      JSON.stringify({ workspace: '/home/u/kanban' }),
    )
    installPinia()

    const store = useChatStore()

    expect(store.workspace).toBe('/home/u/kanban')
  })

  it('persiste solo workspace en localStorage al cambiar de carpeta', async () => {
    installPinia()
    const store = useChatStore()

    await store.pickWorkspace('/home/u/kanban')
    await nextTick()

    const stored = JSON.parse(localStorage.getItem('chat') as string)
    expect(stored.workspace).toBe('/home/u/kanban')
    // Solo workspace: ni el log ni los punteros de streaming van a localStorage.
    expect(Object.keys(stored)).toEqual(['workspace'])
  })
})

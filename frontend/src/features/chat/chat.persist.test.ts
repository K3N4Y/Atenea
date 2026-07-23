// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import piniaPluginPersistedstate from 'pinia-plugin-persistedstate'

// La configuracion del chat debe sobrevivir al cierre de la app: la carpeta, el
// provider se restauran desde localStorage. El historial y MCP pertenecen a sus
// respectivos modulos y fuentes de verdad.
//
// Como en ui.test.ts, los plugins de pinia solo corren con pinia instalado en una
// app Vue, asi que montamos una app minima para activar la persistencia.
vi.mock('../../../wailsjs/go/main/App', () => ({
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
  SetProvider: vi.fn(() => Promise.resolve()),
  ProviderConfig: vi.fn(() =>
    Promise.resolve({ kind: '', baseURL: '', model: '' }),
  ),
  ListModels: vi.fn(() => Promise.resolve([])),
}))
vi.mock('../../../wailsjs/runtime/runtime', () => ({
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

describe('chat store: persistencia entre reinicios', () => {
  it('rehidrata workspace desde localStorage al iniciar', () => {
    localStorage.setItem(
      'chat',
      JSON.stringify({ workspace: '/home/u/kanban' }),
    )
    installPinia()

    const store = useChatStore()

    expect(store.workspace).toBe('/home/u/kanban')
  })

  it('persiste workspace y provider, pero no configuraciones MCP', async () => {
    installPinia()
    const store = useChatStore()

    await store.pickWorkspace('/home/u/kanban')
    await store.setProvider('local', 'http://localhost:1234/v1', 'qwen')
    await nextTick()

    const stored = JSON.parse(localStorage.getItem('chat') as string)
    expect(stored.workspace).toBe('/home/u/kanban')
    expect(stored.providerKind).toBe('local')
    expect(stored.baseURL).toBe('http://localhost:1234/v1')
    expect(stored.model).toBe('qwen')
    // Solo las preferencias durables: ni el log, ni los punteros de streaming, ni
    // el estado de conexion MCP, ni availableModels van a localStorage.
    expect(Object.keys(stored).sort()).toEqual([
      'baseURL',
      'model',
      'providerKind',
      'workspace',
    ])
  })

  it('rehidrata la config del provider desde localStorage al iniciar', () => {
    localStorage.setItem(
      'chat',
      JSON.stringify({
        providerKind: 'local',
        baseURL: 'http://localhost:1234/v1',
        model: 'qwen',
      }),
    )
    installPinia()

    const store = useChatStore()

    expect(store.providerKind).toBe('local')
    expect(store.baseURL).toBe('http://localhost:1234/v1')
    expect(store.model).toBe('qwen')
  })

  it('ignora configuraciones MCP legadas al rehidratar Chat', () => {
    localStorage.setItem(
      'chat',
      JSON.stringify({
        mcpServers: [
          {
            name: 'github',
            command: 'npx',
            args: ['-y', '@modelcontextprotocol/server-github'],
          },
        ],
      }),
    )
    installPinia()

    const store = useChatStore()

    expect('mcpServers' in store).toBe(false)
  })
})

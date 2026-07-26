// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import piniaPluginPersistedstate from 'pinia-plugin-persistedstate'

// La configuracion del chat que debe sobrevivir al cierre de la app es la carpeta
// de trabajo. El provider y el modelo NO: los persiste el backend en el
// providers.json que comparte con la app de terminal, y una segunda copia aqui es
// como las dos llegaban a contradecirse. El historial y MCP pertenecen a sus
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
  ListProjectFiles: vi.fn(() => Promise.resolve([])),
  ListCommands: vi.fn(() => Promise.resolve([])),
  Workspace: vi.fn(() => Promise.resolve('/home/u/a')),
  SetWorkspace: vi.fn(() => Promise.resolve()),
  SelectWorkspace: vi.fn(() => Promise.resolve('')),
  ActiveProvider: vi.fn(() =>
    Promise.resolve({
      providerID: 'anthropic',
      providerName: 'Anthropic',
      model: 'claude-opus-4-8',
      contextWindow: 200000,
    }),
  ),
  ProviderCatalog: vi.fn(() => Promise.resolve([])),
  RefreshModels: vi.fn(() => Promise.resolve([])),
  SelectModel: vi.fn(() => Promise.resolve()),
  ConnectProvider: vi.fn(() => Promise.resolve()),
  DeclareEndpoint: vi.fn(() => Promise.resolve('lm-studio')),
  ForgetProvider: vi.fn(() => Promise.resolve()),
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

  it('persiste la carpeta, y nada del provider ni de MCP', async () => {
    installPinia()
    const store = useChatStore()

    await store.pickWorkspace('/home/u/kanban')
    await store.selectModel('anthropic', 'claude-opus-4-8')
    await nextTick()

    const stored = JSON.parse(localStorage.getItem('chat') as string)
    expect(stored.workspace).toBe('/home/u/kanban')
    // Solo las preferencias durables del cliente: ni el log, ni los punteros de
    // streaming, ni el estado de conexion MCP, ni la seleccion de modelo (esa la
    // guarda el backend) van a localStorage.
    expect(Object.keys(stored).sort()).toEqual(['workspace'])
  })

  // Una seleccion de provider guardada por una version anterior se ignora: la
  // fuente de verdad es el backend, y re-aplicar la copia vieja era justo lo que
  // podia dejar la UI apuntando a un endpoint que ya no existe.
  it('ignora la config del provider legada al rehidratar', () => {
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

    expect('providerKind' in store).toBe(false)
    expect(store.model).toBe('')
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

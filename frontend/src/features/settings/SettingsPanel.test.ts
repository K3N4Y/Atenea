// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'

vi.mock('../../../wailsjs/go/main/App', () => ({
  ListSessions: vi.fn(() => Promise.resolve([])),
  SessionHistory: vi.fn(() => Promise.resolve([])),
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
  ProviderCatalog: vi.fn(() =>
    Promise.resolve([
      {
        id: 'anthropic',
        name: 'Anthropic',
        models: ['claude-opus-4-8', 'claude-sonnet-5'],
        builtIn: true,
        connectable: true,
        connected: false,
      },
    ]),
  ),
  RefreshModels: vi.fn(() => Promise.resolve([])),
  SelectModel: vi.fn(() => Promise.resolve()),
  ConnectProvider: vi.fn(() => Promise.resolve()),
  DeclareEndpoint: vi.fn(() => Promise.resolve('lm-studio')),
  ForgetProvider: vi.fn(() => Promise.resolve()),
  ListModels: vi.fn(() => Promise.resolve([])),
  ListMCPs: vi.fn(() => Promise.resolve([])),
  ConnectMCP: vi.fn(() => Promise.resolve()),
  DisconnectMCP: vi.fn(() => Promise.resolve()),
  SaveMCPConfig: vi.fn(() => Promise.resolve()),
  RemoveMCPConfig: vi.fn(() => Promise.resolve()),
  SendPrompt: vi.fn(() => Promise.resolve()),
  SendPlanPrompt: vi.fn(() => Promise.resolve()),
  AcceptPlan: vi.fn(() => Promise.resolve()),
  Stop: vi.fn(),
  ResolveToolPermission: vi.fn(),
  DeleteSession: vi.fn(() => Promise.resolve()),
}))
vi.mock('../../../wailsjs/runtime/runtime', () => ({
  EventsOn: vi.fn(() => () => {}),
}))

import SettingsPanel from './SettingsPanel.vue'
import ProviderSettings from './ProviderSettings.vue'
import * as App from '../../../wailsjs/go/main/App'

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  vi.mocked(App.ListMCPs).mockResolvedValue([])
})

function mcpsTabOf(wrapper: ReturnType<typeof mount>) {
  const tab = wrapper
    .findAll('[role="tab"]')
    .find((element) => element.text() === 'MCPs')
  if (!tab) throw new Error('the MCPs tab does not exist')
  return tab
}

describe('SettingsPanel', () => {
  it('is a labelled full-screen dialog', () => {
    const wrapper = mount(SettingsPanel)
    const dialog = wrapper.find('[role="dialog"]')
    expect(dialog.attributes('aria-label')).toBeTruthy()
    expect(dialog.classes()).toContain('fixed')
    expect(dialog.classes()).toContain('inset-0')
  })

  it('lists connected MCP servers', async () => {
    vi.mocked(App.ListMCPs).mockResolvedValue([
      {
        name: 'github',
        command: 'npx',
        args: ['-y'],
        connected: true,
        tools: 4,
      },
    ])
    const wrapper = mount(SettingsPanel)
    await flushPromises()
    await mcpsTabOf(wrapper).trigger('click')
    expect(wrapper.text()).toContain('github')
    expect(wrapper.get('[data-mcp-status="github"]').text()).toBe(
      'Connected · 4 tools available',
    )
  })

  it('connects an MCP with one argument per line', async () => {
    const wrapper = mount(SettingsPanel)
    await mcpsTabOf(wrapper).trigger('click')
    await wrapper.find('[data-mcp-name]').setValue('github')
    await wrapper.find('[data-mcp-command]').setValue('npx')
    await wrapper
      .find('[data-mcp-args]')
      .setValue('-y\n@modelcontextprotocol/server-github')
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(App.ConnectMCP).toHaveBeenCalledWith({
      name: 'github',
      type: 'stdio',
      command: 'npx',
      args: ['-y', '@modelcontextprotocol/server-github'],
    })
    // La config confirmada se persiste en el mcp.json global (compartido con
    // la TUI), no en localStorage.
    expect(App.SaveMCPConfig).toHaveBeenCalledWith({
      name: 'github',
      type: 'stdio',
      command: 'npx',
      args: ['-y', '@modelcontextprotocol/server-github'],
    })
  })

  it('keeps an MCP configuration after recreating the settings panel', async () => {
    const wrapper = mount(SettingsPanel)
    await mcpsTabOf(wrapper).trigger('click')
    await wrapper.find('[data-mcp-name]').setValue('github')
    await wrapper.find('[data-mcp-command]').setValue('npx')
    await wrapper
      .find('[data-mcp-args]')
      .setValue('-y\n@modelcontextprotocol/server-github')
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    wrapper.unmount()

    // El backend es la fuente de verdad: tras guardarse, ListMCPs devuelve el
    // server declarado (desconectado) y el panel recreado lo muestra.
    vi.mocked(App.ListMCPs).mockResolvedValue([
      {
        name: 'github',
        command: 'npx',
        args: ['-y', '@modelcontextprotocol/server-github'],
        connected: false,
        tools: 0,
      },
    ])
    const reopened = mount(SettingsPanel)
    await flushPromises()
    await mcpsTabOf(reopened).trigger('click')

    expect(reopened.text()).toContain('github')
  })

  it('removes an MCP server through the backend config', async () => {
    vi.mocked(App.ListMCPs).mockResolvedValue([
      { name: 'github', command: 'npx', args: [], connected: false, tools: 0 },
    ])
    const wrapper = mount(SettingsPanel)
    await flushPromises()
    await mcpsTabOf(wrapper).trigger('click')
    await wrapper.find('[data-remove-mcp="github"]').trigger('click')
    await flushPromises()
    expect(App.RemoveMCPConfig).toHaveBeenCalledWith('github')
  })

  it('disconnects a listed MCP server', async () => {
    vi.mocked(App.ListMCPs).mockResolvedValue([
      { name: 'github', command: 'npx', args: [], connected: true, tools: 1 },
    ])
    const wrapper = mount(SettingsPanel)
    await flushPromises()
    await mcpsTabOf(wrapper).trigger('click')
    await wrapper.find('[data-disconnect-mcp="github"]').trigger('click')
    expect(App.DisconnectMCP).toHaveBeenCalledWith('github')
  })

  it('renders the provider catalog by default and delegates a model choice', async () => {
    const wrapper = mount(SettingsPanel)
    await flushPromises()
    expect(wrapper.findComponent(ProviderSettings).exists()).toBe(true)

    await wrapper
      .find('[data-model-option="anthropic:claude-sonnet-5"]')
      .trigger('click')
    await flushPromises()

    expect(App.SelectModel).toHaveBeenCalledWith('anthropic', 'claude-sonnet-5')
  })

  // Cada accion del selector puede fallar por algo que el usuario puede arreglar,
  // asi que el panel muestra el motivo en vez de dejar la UI sin explicar por que
  // nada cambio.
  it('shows why a provider action failed', async () => {
    vi.mocked(App.SelectModel).mockRejectedValueOnce(
      new Error('no API key for provider "anthropic"'),
    )
    const wrapper = mount(SettingsPanel)
    await flushPromises()

    await wrapper
      .find('[data-model-option="anthropic:claude-opus-4-8"]')
      .trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-provider-error]').text()).toContain('no API key')
  })

  // Agregar un endpoint con un modelo escrito lo deja listo para hablar: declararlo
  // y activarlo son un solo gesto desde el formulario.
  it('declares a local endpoint and activates the model it was given', async () => {
    const wrapper = mount(SettingsPanel)
    await flushPromises()

    await wrapper.find('[data-add-endpoint]').trigger('click')
    await wrapper.find('[data-endpoint-name]').setValue('LM Studio')
    await wrapper.find('[data-preset="lmstudio"]').trigger('click')
    await wrapper.find('[data-endpoint-model]').setValue('qwen')
    await wrapper.find('[data-endpoint-form]').trigger('submit')
    await flushPromises()

    expect(App.DeclareEndpoint).toHaveBeenCalledWith(
      'LM Studio',
      'http://localhost:1234/v1',
      'qwen',
    )
    expect(App.SelectModel).toHaveBeenCalledWith('lm-studio', 'qwen')
    // El formulario se cierra al confirmarse, no al pulsar.
    expect(wrapper.find('[data-endpoint-name]').exists()).toBe(false)
  })

  it('keeps the endpoint form open when declaring fails', async () => {
    vi.mocked(App.DeclareEndpoint).mockRejectedValueOnce(
      new Error('invalid base URL "localhost:1234"'),
    )
    const wrapper = mount(SettingsPanel)
    await flushPromises()

    await wrapper.find('[data-add-endpoint]').trigger('click')
    await wrapper.find('[data-endpoint-name]').setValue('LM Studio')
    await wrapper.find('[data-endpoint-url]').setValue('localhost:1234')
    await wrapper.find('[data-endpoint-form]').trigger('submit')
    await flushPromises()

    expect(wrapper.get('[data-provider-error]').text()).toContain(
      'invalid base URL',
    )
    expect(wrapper.get('[data-endpoint-name]').attributes('value')).toBe(
      undefined,
    )
    expect(wrapper.find('[data-endpoint-url]').exists()).toBe(true)
    expect(App.SelectModel).not.toHaveBeenCalled()
  })

  it('stores an API key for a provider that has none', async () => {
    const wrapper = mount(SettingsPanel)
    await flushPromises()

    await wrapper.find('[data-api-key-input="anthropic"]').setValue('sk-ant')
    await wrapper.find('[data-connect-form="anthropic"]').trigger('submit')
    await flushPromises()

    expect(App.ConnectProvider).toHaveBeenCalledWith('anthropic', 'sk-ant')
  })

  it('probes an endpoint for its models before it is declared', async () => {
    vi.mocked(App.ListModels).mockResolvedValueOnce(['qwen', 'llama'])
    const wrapper = mount(SettingsPanel)
    await flushPromises()

    await wrapper.find('[data-add-endpoint]').trigger('click')
    await wrapper
      .find('[data-endpoint-url]')
      .setValue('http://localhost:1234/v1')
    await wrapper.find('[data-list-models]').trigger('click')
    await flushPromises()

    expect(App.ListModels).toHaveBeenCalledWith('http://localhost:1234/v1')
    await wrapper.find('[data-discovered-model="llama"]').trigger('click')
    await wrapper.find('[data-endpoint-form]').trigger('submit')
    await flushPromises()

    expect(App.DeclareEndpoint).toHaveBeenCalledWith(
      '',
      'http://localhost:1234/v1',
      'llama',
    )
  })

  it('emits close on the close button and Escape', async () => {
    const wrapper = mount(SettingsPanel)
    await wrapper
      .find('button[aria-label="Cerrar configuracion"]')
      .trigger('click')
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(wrapper.emitted('close')?.length).toBe(2)
  })
})

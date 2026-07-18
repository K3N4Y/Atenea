// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'

vi.mock('../../wailsjs/go/main/App', () => ({
  Model: vi.fn(() => Promise.resolve('m')),
  ListSessions: vi.fn(() => Promise.resolve([])),
  SessionHistory: vi.fn(() => Promise.resolve([])),
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
vi.mock('../../wailsjs/runtime/runtime', () => ({
  EventsOn: vi.fn(() => () => {}),
}))

import SettingsPanel from './SettingsPanel.vue'
import ProviderSettings from './ProviderSettings.vue'
import * as App from '../../wailsjs/go/main/App'

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
      command: 'npx',
      args: ['-y', '@modelcontextprotocol/server-github'],
    })
    // La config confirmada se persiste en el mcp.json global (compartido con
    // la TUI), no en localStorage.
    expect(App.SaveMCPConfig).toHaveBeenCalledWith({
      name: 'github',
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

  it('renders provider settings by default and delegates provider updates', async () => {
    const wrapper = mount(SettingsPanel)
    expect(wrapper.findComponent(ProviderSettings).exists()).toBe(true)
    await wrapper.find('[data-provider-option="local"]').trigger('click')
    await wrapper.find('[data-preset="lmstudio"]').trigger('click')
    await wrapper.find('[data-model-input]').setValue('qwen')
    await wrapper.find('[data-apply-provider]').trigger('click')
    expect(App.SetProvider).toHaveBeenCalledWith(
      'local',
      'http://localhost:1234/v1',
      'qwen',
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

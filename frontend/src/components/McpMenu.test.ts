// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, beforeAll } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'

const ListMCPs = vi.fn()
const ConnectMCP = vi.fn()
const DisconnectMCP = vi.fn()

vi.mock('../../wailsjs/go/main/App', () => ({
  ListMCPs: (...a: unknown[]) => ListMCPs(...a),
  ConnectMCP: (...a: unknown[]) => ConnectMCP(...a),
  DisconnectMCP: (...a: unknown[]) => DisconnectMCP(...a),
  SaveMCPConfig: vi.fn(() => Promise.resolve()),
}))

vi.mock('../lib/modal', () => ({
  PrettyModal: class {
    open(id: string) {
      const el = document.getElementById(id)
      if (el instanceof HTMLDialogElement) el.showModal()
    }
    close(id: string) {
      const el = document.getElementById(id)
      if (el instanceof HTMLDialogElement) el.close()
    }
  },
}))

import McpMenu from './McpMenu.vue'

// declared arma la respuesta de ListMCPs para un server declarado en la
// config del backend, conectado o no.
function declared(connected: boolean, tools = 0) {
  return { name: 'github', command: 'npx', args: ['-y'], connected, tools }
}

beforeAll(() => {
  if (typeof HTMLDialogElement !== 'undefined') {
    HTMLDialogElement.prototype.showModal = function showModal() {
      this.setAttribute('open', '')
    }
    HTMLDialogElement.prototype.close = function close() {
      this.removeAttribute('open')
      this.dispatchEvent(new Event('close'))
    }
  }
})

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  ListMCPs.mockResolvedValue([])
  ConnectMCP.mockResolvedValue(undefined)
  DisconnectMCP.mockResolvedValue(undefined)
})

function mountMenu() {
  return mount(McpMenu, { attachTo: document.body })
}

function openMenu(
  wrapper: ReturnType<typeof mountMenu>,
  target: Element = document.body,
) {
  const event = new MouseEvent('click', { bubbles: true })
  Object.defineProperty(event, 'currentTarget', { value: target })
  ;(wrapper.vm as unknown as { open: (e: Event) => void }).open(event)
}

describe('McpMenu', () => {
  it('muestra el mensaje de vacio cuando no hay servers configurados', async () => {
    const wrapper = mountMenu()
    await flushPromises()
    expect(wrapper.text()).toContain('No MCP servers configured.')
  })

  it('lista los servers configurados con su comando', async () => {
    ListMCPs.mockResolvedValue([declared(true, 4)])
    const wrapper = mountMenu()
    await flushPromises()
    expect(wrapper.text()).toContain('github')
    expect(wrapper.text()).toContain('npx -y')
  })

  it('el switch arranca desconectado para un server sin conexion', async () => {
    ListMCPs.mockResolvedValue([declared(false)])
    const wrapper = mountMenu()
    await flushPromises()
    expect(
      wrapper.find('[data-mcp-switch="github"]').attributes('aria-checked'),
    ).toBe('false')
  })

  it('al pulsar el switch de un server desconectado lo conecta', async () => {
    ListMCPs.mockResolvedValue([declared(false)])
    const wrapper = mountMenu()
    await flushPromises()

    await wrapper.find('[data-mcp-switch="github"]').trigger('click')
    await flushPromises()

    expect(ConnectMCP).toHaveBeenCalledWith({
      name: 'github',
      command: 'npx',
      args: ['-y'],
    })
  })

  it('al pulsar el switch de un server conectado lo desconecta', async () => {
    ListMCPs.mockResolvedValue([declared(true, 1)])
    const wrapper = mountMenu()
    await flushPromises()

    await wrapper.find('[data-mcp-switch="github"]').trigger('click')
    await flushPromises()

    expect(DisconnectMCP).toHaveBeenCalledWith('github')
  })

  it('cierra al clickear el backdrop del dialog', async () => {
    const wrapper = mountMenu()
    await flushPromises()
    openMenu(wrapper)
    await wrapper.find('dialog').trigger('click')
    expect(wrapper.emitted('close')?.length).toBe(1)
  })

  it('cierra con Escape (cancel del dialog)', async () => {
    const wrapper = mountMenu()
    await flushPromises()
    openMenu(wrapper)
    await wrapper.find('dialog').trigger('cancel')
    expect(wrapper.emitted('close')?.length).toBe(1)
  })

  it('cierra con el boton explicito', async () => {
    const wrapper = mountMenu()
    await flushPromises()
    openMenu(wrapper)
    await wrapper.find('button[aria-label="Cerrar menu"]').trigger('click')
    expect(wrapper.emitted('close')?.length).toBe(1)
  })

  it('es un dialog etiquetado', () => {
    const wrapper = mountMenu()
    const dialog = wrapper.find('dialog')
    expect(dialog.exists()).toBe(true)
    expect(dialog.attributes('aria-label')).toBe('MCP servers')
    expect(dialog.attributes('id')).toBe('mcp-menu')
  })

  it('muestra un error chico abajo cuando falla conectar', async () => {
    ConnectMCP.mockRejectedValue(new Error('boom'))
    ListMCPs.mockResolvedValue([declared(false)])
    const wrapper = mountMenu()
    await flushPromises()

    await wrapper.find('[data-mcp-switch="github"]').trigger('click')
    await flushPromises()

    const alert = wrapper.find('[role="alert"]')
    expect(alert.exists()).toBe(true)
    expect(alert.text()).toBe('boom')
    // Revertido: el switch vuelve a desconectado.
    expect(
      wrapper.find('[data-mcp-switch="github"]').attributes('aria-checked'),
    ).toBe('false')
  })
})

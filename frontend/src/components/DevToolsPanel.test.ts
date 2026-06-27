// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'

const GitStatus = vi.fn()
const GenerateCommitMessage = vi.fn()
const Commit = vi.fn()
const InitRepo = vi.fn()

vi.mock('../../wailsjs/go/main/App', () => ({
  GitStatus: () => GitStatus(),
  GenerateCommitMessage: () => GenerateCommitMessage(),
  Commit: (msg: string) => Commit(msg),
  InitRepo: () => InitRepo(),
}))

import DevToolsPanel from './DevToolsPanel.vue'

// TerminalPanel monta xterm (GUI, no corre headless); lo stubeamos para probar
// el cableado de la tab sin instanciar un terminal real.
const TerminalStub = { template: '<div data-terminal-stub />' }

function mountPanel() {
  setActivePinia(createPinia())
  return mount(DevToolsPanel, {
    global: { stubs: { TerminalPanel: TerminalStub } },
  })
}

beforeEach(() => {
  GitStatus.mockResolvedValue({
    isRepo: true,
    staged: [{ path: 'a.go', status: 'M' }],
    untracked: [{ path: 'b.go', status: '??' }],
  })
  GenerateCommitMessage.mockResolvedValue('feat: agregar panel')
  Commit.mockResolvedValue(undefined)
  InitRepo.mockResolvedValue(undefined)
})

describe('DevToolsPanel', () => {
  it('lista los cambios staged y untracked al montar', async () => {
    const wrapper = mountPanel()
    await flushPromises()
    expect(wrapper.findAll('[data-staged]')).toHaveLength(1)
    expect(wrapper.find('[data-staged]').text()).toContain('a.go')
    expect(wrapper.findAll('[data-untracked]')).toHaveLength(1)
    expect(wrapper.find('[data-untracked]').text()).toContain('b.go')
  })

  it('genera el mensaje y lo pone en el textarea', async () => {
    const wrapper = mountPanel()
    await flushPromises()
    await wrapper.find('button[aria-label="Generar mensaje"]').trigger('click')
    await flushPromises()
    expect(GenerateCommitMessage).toHaveBeenCalled()
    expect(
      (wrapper.find('textarea').element as HTMLTextAreaElement).value,
    ).toBe('feat: agregar panel')
  })

  it('commitea con el mensaje del textarea y recarga el estado', async () => {
    const wrapper = mountPanel()
    await flushPromises()
    await wrapper.find('textarea').setValue('fix: algo')
    GitStatus.mockClear()
    await wrapper.find('button[aria-label="Crear commit"]').trigger('click')
    await flushPromises()
    expect(Commit).toHaveBeenCalledWith('fix: algo')
    expect(GitStatus).toHaveBeenCalled() // recarga tras commitear
  })

  it('ofrece iniciar repo cuando el proyecto no tiene uno', async () => {
    GitStatus.mockResolvedValue({ isRepo: false, staged: [], untracked: [] })
    const wrapper = mountPanel()
    await flushPromises()

    // Sin repo no se muestra la UI de commit, sino el boton de iniciar.
    expect(wrapper.find('textarea').exists()).toBe(false)
    const initBtn = wrapper.find('button[aria-label="Iniciar repositorio"]')
    expect(initBtn.exists()).toBe(true)

    GitStatus.mockResolvedValue({ isRepo: true, staged: [], untracked: [] })
    await initBtn.trigger('click')
    await flushPromises()
    expect(InitRepo).toHaveBeenCalled()
    expect(GitStatus).toHaveBeenCalled() // recarga tras iniciar
  })

  it('arranca con una tab Git por defecto', async () => {
    const wrapper = mountPanel()
    await flushPromises()
    const tabsBar = wrapper.findAll('[role="tab"]')
    expect(tabsBar).toHaveLength(1)
    expect(tabsBar[0].text()).toContain('Git')
  })

  it('agrega una tab Terminal desde el menu +', async () => {
    const wrapper = mountPanel()
    await flushPromises()
    expect(wrapper.find('[data-terminal-stub]').exists()).toBe(false)

    await wrapper
      .find('button[aria-label="Agregar herramienta"]')
      .trigger('click')
    const termItem = wrapper
      .findAll('[role="menuitem"]')
      .find((b) => b.text().includes('Terminal'))
    expect(termItem).toBeTruthy()
    await termItem!.trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-terminal-stub]').exists()).toBe(true)
    expect(wrapper.findAll('[role="tab"]')).toHaveLength(2) // Git + Terminal
  })

  it('cierra una tab desde su boton de cierre', async () => {
    const wrapper = mountPanel()
    await flushPromises()
    // Agrega una segunda tab (Terminal) y cierrala: vuelve a quedar solo Git.
    await wrapper
      .find('button[aria-label="Agregar herramienta"]')
      .trigger('click')
    await wrapper
      .findAll('[role="menuitem"]')
      .find((b) => b.text().includes('Terminal'))!
      .trigger('click')
    await flushPromises()
    expect(wrapper.findAll('[role="tab"]')).toHaveLength(2)

    const termTab = wrapper
      .findAll('[role="tab"]')
      .find((t) => t.text().includes('Terminal'))!
    await termTab.find('button[aria-label="Cerrar tab"]').trigger('click')
    await flushPromises()
    expect(wrapper.findAll('[role="tab"]')).toHaveLength(1)
    expect(wrapper.find('[data-terminal-stub]').exists()).toBe(false)
  })

  it('emite close al cerrar', async () => {
    const wrapper = mountPanel()
    await flushPromises()
    await wrapper
      .find('button[aria-label="Cerrar herramientas"]')
      .trigger('click')
    expect(wrapper.emitted('close')).toBeTruthy()
  })
})

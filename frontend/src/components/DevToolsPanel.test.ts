// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'

const GitStatus = vi.fn()
const GenerateCommitMessage = vi.fn()
const Commit = vi.fn()

vi.mock('../../wailsjs/go/main/App', () => ({
  GitStatus: () => GitStatus(),
  GenerateCommitMessage: () => GenerateCommitMessage(),
  Commit: (msg: string) => Commit(msg),
}))

import DevToolsPanel from './DevToolsPanel.vue'

function mountPanel() {
  setActivePinia(createPinia())
  return mount(DevToolsPanel)
}

beforeEach(() => {
  GitStatus.mockResolvedValue({
    staged: [{ path: 'a.go', status: 'M' }],
    untracked: [{ path: 'b.go', status: '??' }],
  })
  GenerateCommitMessage.mockResolvedValue('feat: agregar panel')
  Commit.mockResolvedValue(undefined)
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

  it('emite close al cerrar', async () => {
    const wrapper = mountPanel()
    await flushPromises()
    await wrapper.find('button[aria-label="Cerrar herramientas"]').trigger('click')
    expect(wrapper.emitted('close')).toBeTruthy()
  })
})

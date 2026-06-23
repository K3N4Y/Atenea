// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'

// La vista cablea el store de chat al canal de la sesion via la frontera Wails;
// la reemplazamos por fakes para verificar el ciclo de vida de la suscripcion.
const unsubscribe = vi.fn()
vi.mock('../../wailsjs/runtime/runtime', () => ({
  EventsOn: vi.fn(() => unsubscribe),
}))
vi.mock('../../wailsjs/go/main/App', () => ({
  SendPrompt: vi.fn(() => Promise.resolve()),
  Stop: vi.fn(),
}))

import { EventsOn } from '../../wailsjs/runtime/runtime'
import ChatView from './ChatView.vue'

function mountView() {
  const pinia = createPinia()
  setActivePinia(pinia)
  return mount(ChatView, { global: { plugins: [pinia] } })
}

describe('ChatView', () => {
  it('se suscribe a los canales de la sesion al montar', () => {
    vi.clearAllMocks()
    mountView()

    const sessionChannel = vi.mocked(EventsOn).mock.calls[0][0]
    expect(sessionChannel).toMatch(/^session:chat-/)
    expect(EventsOn).toHaveBeenCalledWith(`${sessionChannel}:error`, expect.any(Function))
  })

  it('limpia los listeners al desmontar', () => {
    vi.clearAllMocks()
    const wrapper = mountView()

    wrapper.unmount()

    expect(unsubscribe).toHaveBeenCalled()
  })

  it('el toggle expone aria-expanded y alterna la sidebar', async () => {
    vi.clearAllMocks()
    const wrapper = mountView()
    const toggle = wrapper.find('button[aria-label="Toggle sidebar"]')

    expect(toggle.attributes('aria-expanded')).toBe('true')

    await toggle.trigger('click')

    expect(toggle.attributes('aria-expanded')).toBe('false')
  })

  it('opens settings when clicking the settings button', async () => {
    vi.clearAllMocks()
    const wrapper = mountView()

    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)

    await wrapper.find('button[aria-label="Open settings"]').trigger('click')

    expect(wrapper.find('[role="dialog"]').exists()).toBe(true)
  })

  it('closes settings when close is emitted', async () => {
    vi.clearAllMocks()
    const wrapper = mountView()
    await wrapper.find('button[aria-label="Open settings"]').trigger('click')

    await wrapper.find('button[aria-label="Cerrar configuracion"]').trigger('click')

    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })
})

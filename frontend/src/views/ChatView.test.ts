// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { nextTick } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'

// La vista cablea el store de chat al canal de la sesion via la frontera Wails;
// la reemplazamos por fakes para verificar el ciclo de vida de la suscripcion.
const unsubscribe = vi.fn()
vi.mock('../../wailsjs/runtime/runtime', () => ({
  EventsOn: vi.fn(() => unsubscribe),
}))
vi.mock('../../wailsjs/go/main/App', () => ({
  SendPrompt: vi.fn(() => Promise.resolve()),
  SendPlanPrompt: vi.fn(() => Promise.resolve()),
  AcceptPlan: vi.fn(() => Promise.resolve()),
  Stop: vi.fn(),
  ListSessions: vi.fn(() => Promise.resolve([])),
  SessionHistory: vi.fn(() => Promise.resolve([])),
  ListProjectFiles: vi.fn(() => Promise.resolve([])),
}))

import { EventsOn } from '../../wailsjs/runtime/runtime'
import * as App from '../../wailsjs/go/main/App'
import ChatView from './ChatView.vue'
import AppSidebar from '../components/AppSidebar.vue'
import ChatComposer from '../components/ChatComposer.vue'
import MessageList from '../components/MessageList.vue'
import PlanView from '../components/PlanView.vue'
import PlanCard from '../components/PlanCard.vue'
import { useChatStore } from '../stores/chat'

function presentPlan(chat: ReturnType<typeof useChatStore>) {
  chat.applyEvent({
    Kind: 'Tool.Called',
    ToolName: 'present_plan',
    CallID: 'c1',
    Input: { title: 'T', plan: 'cuerpo' },
  })
}

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
    expect(EventsOn).toHaveBeenCalledWith(
      `${sessionChannel}:error`,
      expect.any(Function),
    )
  })

  it('carga el historial de sesiones al montar para poblar la sidebar', () => {
    vi.clearAllMocks()
    mountView()

    expect(App.ListSessions).toHaveBeenCalled()
  })

  it('carga los archivos del workspace al montar y se los pasa al composer (@-menu)', async () => {
    vi.clearAllMocks()
    vi.mocked(App.ListProjectFiles).mockResolvedValueOnce(['app.go'])
    const wrapper = mountView()

    await flushPromises()

    expect(App.ListProjectFiles).toHaveBeenCalled()
    expect(wrapper.findComponent(ChatComposer).props('files')).toEqual([
      'app.go',
    ])
  })

  it('rutea select-session de la sidebar a loadSession del store', async () => {
    vi.clearAllMocks()
    const wrapper = mountView()
    const chat = useChatStore()
    const spy = vi.spyOn(chat, 'loadSession').mockResolvedValue()

    wrapper.findComponent(AppSidebar).vm.$emit('select-session', 's1')
    await nextTick()

    expect(spy).toHaveBeenCalledWith('s1')
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

    await wrapper
      .find('button[aria-label="Cerrar configuracion"]')
      .trigger('click')

    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it('no muestra ningun aviso de error cuando no hay errorText', () => {
    vi.clearAllMocks()
    const wrapper = mountView()

    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
  })

  it('muestra el errorText del store cuando el proveedor o el stream falla', async () => {
    vi.clearAllMocks()
    const wrapper = mountView()
    const chat = useChatStore()

    chat.applyError('the provider is unavailable')
    await nextTick()

    const alert = wrapper.find('[role="alert"]')
    expect(alert.exists()).toBe(true)
    expect(alert.text()).toContain('the provider is unavailable')
  })

  it('descarta el error al cerrar el aviso y deja de mostrarlo', async () => {
    vi.clearAllMocks()
    const wrapper = mountView()
    const chat = useChatStore()
    chat.applyError('boom')
    await nextTick()

    await wrapper.find('button[aria-label="Dismiss error"]').trigger('click')
    await nextTick()

    expect(chat.errorText).toBeNull()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
  })

  it('no muestra PlanView cuando no hay plan vigente', () => {
    vi.clearAllMocks()
    const wrapper = mountView()

    expect(wrapper.findComponent(PlanView).exists()).toBe(false)
  })

  it('muestra PlanView cuando el agente presenta un plan (present_plan)', async () => {
    vi.clearAllMocks()
    const wrapper = mountView()
    const chat = useChatStore()

    chat.applyEvent({
      Kind: 'Tool.Called',
      ToolName: 'present_plan',
      CallID: 'c1',
      Input: { title: 'T', plan: 'cuerpo' },
    })
    await nextTick()

    expect(wrapper.findComponent(PlanView).exists()).toBe(true)
  })

  it('monta PlanView dentro de <main> (en la columna del chat, no tapando la sidebar)', async () => {
    vi.clearAllMocks()
    const wrapper = mountView()
    const chat = useChatStore()

    chat.applyEvent({
      Kind: 'Tool.Called',
      ToolName: 'present_plan',
      CallID: 'c1',
      Input: { title: 'T', plan: 'cuerpo' },
    })
    await nextTick()

    // El plan vive en la columna del chat: la sidebar (hermana de <main>) queda
    // libre, asi que cambiar de sesion sigue siendo posible con un plan abierto.
    expect(wrapper.get('main').findComponent(PlanView).exists()).toBe(true)
  })

  it('permite cambiar de sesion con un plan abierto (la sidebar sigue operativa)', async () => {
    vi.clearAllMocks()
    const wrapper = mountView()
    const chat = useChatStore()
    const spy = vi.spyOn(chat, 'loadSession').mockResolvedValue()

    presentPlan(chat)
    await nextTick()

    wrapper.findComponent(AppSidebar).vm.$emit('select-session', 's2')
    await nextTick()

    expect(spy).toHaveBeenCalledWith('s2')
  })

  it('al minimizar, el plan pasa a ser una tarjeta dentro de la conversacion (no overlay)', async () => {
    vi.clearAllMocks()
    const wrapper = mountView()
    const chat = useChatStore()

    presentPlan(chat)
    await nextTick()
    // Recien presentado: overlay expandido, sin tarjeta.
    expect(wrapper.findComponent(PlanView).exists()).toBe(true)
    expect(wrapper.findComponent(PlanCard).exists()).toBe(false)

    chat.togglePlanExpanded()
    await nextTick()

    // Minimizado: el overlay desaparece y la tarjeta vive dentro de MessageList.
    expect(wrapper.findComponent(PlanView).exists()).toBe(false)
    expect(
      wrapper.findComponent(MessageList).findComponent(PlanCard).exists(),
    ).toBe(true)
  })

  it('minimizar desde el overlay (PlanView emite minimize) colapsa a la tarjeta', async () => {
    vi.clearAllMocks()
    const wrapper = mountView()
    const chat = useChatStore()

    presentPlan(chat)
    await nextTick()

    wrapper.findComponent(PlanView).vm.$emit('minimize')
    await nextTick()

    expect(wrapper.findComponent(PlanView).exists()).toBe(false)
    expect(wrapper.findComponent(PlanCard).exists()).toBe(true)
  })

  it('expandir desde la tarjeta vuelve a mostrar el overlay del plan', async () => {
    vi.clearAllMocks()
    const wrapper = mountView()
    const chat = useChatStore()

    presentPlan(chat)
    chat.togglePlanExpanded() // minimizar
    await nextTick()
    expect(wrapper.findComponent(PlanCard).exists()).toBe(true)

    wrapper.findComponent(PlanCard).vm.$emit('expand')
    await nextTick()

    expect(wrapper.findComponent(PlanView).exists()).toBe(true)
    expect(wrapper.findComponent(PlanCard).exists()).toBe(false)
  })

  it('cablea el modo del composer: emitir toggle-mode alterna chat.mode', async () => {
    vi.clearAllMocks()
    const wrapper = mountView()
    const chat = useChatStore()
    const composer = wrapper.findComponent(ChatComposer)

    expect(composer.props('mode')).toBe('normal')

    composer.vm.$emit('toggle-mode')
    await nextTick()

    expect(chat.mode).toBe('plan')
    expect(composer.props('mode')).toBe('plan')
  })
})

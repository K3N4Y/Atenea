// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { mount } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'

// El store toca la frontera Wails (bindings y runtime). Igual que en
// chat.test.ts la reemplazamos por fakes para aislar el comportamiento.
vi.mock('../../wailsjs/go/main/App', () => ({
  SendPrompt: vi.fn(() => Promise.resolve()),
  Stop: vi.fn(),
}))
vi.mock('../../wailsjs/runtime/runtime', () => ({
  EventsOn: vi.fn(() => () => {}),
}))

import { useChatStore, type TurnItem } from './chat'

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
})

// mountPick monta un arnes minimo y determinista que pinta en el DOM lo que
// `pick` extrae de cada item del log (sin MessageList.vue: evita GSAP y
// TransitionGroup). Store y wrapper comparten la pinia activa, asi que mutar el
// store reactiva el arnes.
function mountPick(pick: (i: TurnItem) => string) {
  const Harness = defineComponent({
    setup() {
      const store = useChatStore()
      return () => h('div', store.items.map(pick).join(''))
    },
  })
  return { store: useChatStore(), wrapper: mount(Harness) }
}

// Patron de cada test (flush-entre-eventos): tras el evento de inicio drenamos
// con `nextTick` el re-render que agenda el `push` a `items`; sin drenarlo ese
// render pintaria el valor del delta y daria un falso verde. Luego el delta o
// resultado muta el item: solo reactiva si el store capturo el proxy reactivo
// dentro de `items`, no la referencia cruda recien pusheada.
describe('chat store: reactividad del streaming', () => {
  it('re-renderiza el texto del asistente al llegar un Text.Delta en streaming', async () => {
    const { store, wrapper } = mountPick((i) => (i.kind === 'assistant' ? i.text : ''))

    store.applyEvent({ Kind: 'Text.Started' })
    await nextTick()
    expect(wrapper.text()).not.toContain('Hola')

    store.applyEvent({ Kind: 'Text.Delta', Text: 'Hola' })
    await nextTick()
    expect(wrapper.text()).toContain('Hola')
  })

  it('re-renderiza el pensamiento al llegar un Reasoning.Delta en streaming', async () => {
    // Ejercita el camino startReasoning: un fix parcial solo de startAssistant fallaria aqui.
    const { store, wrapper } = mountPick((i) => (i.kind === 'reasoning' ? i.text : ''))

    store.applyEvent({ Kind: 'Reasoning.Started' })
    await nextTick()
    expect(wrapper.text()).not.toContain('pensando')

    store.applyEvent({ Kind: 'Reasoning.Delta', Text: 'pensando' })
    await nextTick()
    expect(wrapper.text()).toContain('pensando')
  })

  it('re-renderiza la tool al resolverse con Tool.Success', async () => {
    // Ejercita la rama Tool.Called, que guarda el item en un Map por CallID.
    const { store, wrapper } = mountPick((i) => (i.kind === 'tool' ? `${i.status}:${i.output}` : ''))

    store.applyEvent({ Kind: 'Tool.Called', CallID: 'c1', ToolName: 'echo' })
    await nextTick()
    expect(wrapper.text()).not.toContain('success:hi')

    store.applyEvent({ Kind: 'Tool.Success', CallID: 'c1', Text: 'hi' })
    await nextTick()
    expect(wrapper.text()).toContain('success:hi')
  })
})

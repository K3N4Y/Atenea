// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { ref, type Ref } from 'vue'
import { mount } from '@vue/test-utils'
import AssistantMessage from './AssistantMessage.vue'

// El streaming visual (reveal caracter a caracter) vive en useSmoothText y se
// prueba en aislamiento en lib/useSmoothText.test.ts. Aca lo mockeamos para
// verificar el contrato de render del componente: que pasa `visible` a
// Markdown durante la escritura y conserva el caret hasta que `done`.
const smooth = vi.hoisted(() => ({
  visible: null as unknown as Ref<string>,
  done: null as unknown as Ref<boolean>,
}))
vi.mock('../lib/useSmoothText', () => ({
  useSmoothText: () => ({ visible: smooth.visible, done: smooth.done }),
}))

beforeEach(() => {
  smooth.visible = ref('')
  smooth.done = ref(false)
})

describe('AssistantMessage', () => {
  it('mientras escribe renderiza Markdown del texto parcial visible y conserva el caret', () => {
    smooth.visible.value = '**Hola**'
    smooth.done.value = false

    const wrapper = mount(AssistantMessage, {
      props: {
        item: {
          kind: 'assistant',
          id: 'a1',
          text: '**Hola** mundo',
          streaming: true,
        },
      },
    })

    // Pinta el parcial revelado, no el texto completo pendiente del store.
    expect(wrapper.text()).not.toContain('mundo')
    expect(wrapper.html()).toContain('<strong>Hola</strong>')
    expect(wrapper.find('[aria-hidden="true"]').exists()).toBe(true)
  })

  it('mientras escribe renderiza una lista parcial sin revelar elementos pendientes', () => {
    smooth.visible.value = '- primer elemento'
    smooth.done.value = false

    const wrapper = mount(AssistantMessage, {
      props: {
        item: {
          kind: 'assistant',
          id: 'a1',
          text: '- primer elemento\n- segundo elemento',
          streaming: true,
        },
      },
    })

    expect(wrapper.html()).toContain('<ul>')
    expect(wrapper.html()).toContain('<li>primer elemento</li>')
    expect(wrapper.text()).not.toContain('segundo elemento')
    expect(wrapper.find('[aria-hidden="true"]').exists()).toBe(true)
  })

  it('al terminar (done) renderiza Markdown del texto completo y oculta el caret', () => {
    smooth.done.value = true

    const wrapper = mount(AssistantMessage, {
      props: {
        item: { kind: 'assistant', id: 'a1', text: '**x**', streaming: false },
      },
    })

    expect(wrapper.html()).toContain('<strong>x</strong>')
    expect(wrapper.find('[aria-hidden="true"]').exists()).toBe(false)
  })
})

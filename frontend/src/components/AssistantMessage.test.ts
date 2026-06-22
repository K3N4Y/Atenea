// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { ref } from 'vue'
import { mount } from '@vue/test-utils'
import AssistantMessage from './AssistantMessage.vue'

// El streaming visual (reveal caracter a caracter) vive en useSmoothText y se
// prueba en aislamiento en lib/useSmoothText.test.ts. Aca lo mockeamos para
// verificar solo el contrato de render del componente: que pinta `visible`
// (texto parcial) durante la escritura y hace swap a Markdown cuando `done`.
const smooth = vi.hoisted(() => ({ visible: null as any, done: null as any }))
vi.mock('../lib/useSmoothText', () => ({
  useSmoothText: () => ({ visible: smooth.visible, done: smooth.done }),
}))

beforeEach(() => {
  smooth.visible = ref('')
  smooth.done = ref(false)
})

describe('AssistantMessage', () => {
  it('mientras escribe muestra el texto parcial (visible) plano y un caret', () => {
    smooth.visible.value = '**Hol'
    smooth.done.value = false

    const wrapper = mount(AssistantMessage, {
      props: { item: { kind: 'assistant', id: 'a1', text: '**Hola**', streaming: true } },
    })

    // Pinta el parcial revelado, no el texto completo del store.
    expect(wrapper.text()).not.toContain('Hola')
    // Texto plano: no se reparsea Markdown en cada frame.
    expect(wrapper.html()).not.toContain('<strong>')
    expect(wrapper.find('[aria-hidden="true"]').exists()).toBe(true)
  })

  it('al terminar (done) renderiza Markdown del texto completo y oculta el caret', () => {
    smooth.done.value = true

    const wrapper = mount(AssistantMessage, {
      props: { item: { kind: 'assistant', id: 'a1', text: '**x**', streaming: false } },
    })

    expect(wrapper.html()).toContain('<strong>x</strong>')
    expect(wrapper.find('[aria-hidden="true"]').exists()).toBe(false)
  })
})

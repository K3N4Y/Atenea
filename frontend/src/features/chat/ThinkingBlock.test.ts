// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { ref, type Ref } from 'vue'
import { mount } from '@vue/test-utils'
import ThinkingBlock from './ThinkingBlock.vue'

// El reveal suave del pensamiento vive en useSmoothText (probado aparte). Aca lo
// mockeamos para verificar el contrato de render: preview sobre `visible` y el
// colapso a "Thought <t>" gateado por `done`, no por item.streaming.
const smooth = vi.hoisted(() => ({
  visible: null as unknown as Ref<string>,
  done: null as unknown as Ref<boolean>,
}))
vi.mock('../../lib/useSmoothText', () => ({
  useSmoothText: () => ({ visible: smooth.visible, done: smooth.done }),
}))

beforeEach(() => {
  smooth.visible = ref('')
  smooth.done = ref(false)
})

describe('ThinkingBlock', () => {
  it('activo: muestra "Thinking" y solo las ultimas 4 lineas de lo revelado (§9)', () => {
    smooth.visible.value = 'l1\nl2\nl3\nl4\nl5'
    smooth.done.value = false

    const wrapper = mount(ThinkingBlock, {
      props: {
        item: {
          kind: 'reasoning',
          id: 'r1',
          text: 'l1\nl2\nl3\nl4\nl5',
          streaming: true,
          durationMs: null,
        },
      },
    })

    expect(wrapper.text()).toContain('Thinking')
    expect(wrapper.text()).toContain('l5')
    expect(wrapper.text()).not.toContain('l1')
  })

  it('finalizado (done): colapsa a "Thought <tiempo>" y expande al click (§9)', async () => {
    smooth.visible.value = 'contenido completo'
    smooth.done.value = true

    const wrapper = mount(ThinkingBlock, {
      props: {
        item: {
          kind: 'reasoning',
          id: 'r1',
          text: 'contenido completo',
          streaming: false,
          durationMs: 3000,
        },
      },
    })

    expect(wrapper.text()).toContain('Thought 3s')
    const button = wrapper.find('button')
    expect(button.attributes('aria-expanded')).toBe('false')
    // El cuerpo siempre vive en el DOM (colapsable por altura via grid-rows);
    // la intencion "no esta mostrado" se aserta sobre el estado colapsado.
    expect(wrapper.find('[data-collapsed]').exists()).toBe(true)
    expect(wrapper.find('[data-expanded]').exists()).toBe(false)

    await button.trigger('click')

    expect(button.attributes('aria-expanded')).toBe('true')
    expect(wrapper.text()).toContain('contenido completo')
    expect(wrapper.find('[data-expanded]').exists()).toBe(true)
    expect(wrapper.find('[data-collapsed]').exists()).toBe(false)
  })
})

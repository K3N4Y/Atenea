// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import ThinkingBlock from './ThinkingBlock.vue'

describe('ThinkingBlock', () => {
  it('activo: muestra "Thinking" y solo las ultimas 4 lineas (§9)', () => {
    const wrapper = mount(ThinkingBlock, {
      props: {
        item: { kind: 'reasoning', id: 'r1', text: 'l1\nl2\nl3\nl4\nl5', streaming: true, durationMs: null },
      },
    })

    expect(wrapper.text()).toContain('Thinking')
    expect(wrapper.text()).toContain('l5')
    expect(wrapper.text()).not.toContain('l1')
  })

  it('finalizado: colapsa a "Thought <tiempo>" y expande al click (§9)', async () => {
    const wrapper = mount(ThinkingBlock, {
      props: {
        item: { kind: 'reasoning', id: 'r1', text: 'contenido completo', streaming: false, durationMs: 3000 },
      },
    })

    expect(wrapper.text()).toContain('Thought 3s')
    const button = wrapper.find('button')
    expect(button.attributes('aria-expanded')).toBe('false')
    expect(wrapper.text()).not.toContain('contenido completo')

    await button.trigger('click')

    expect(button.attributes('aria-expanded')).toBe('true')
    expect(wrapper.text()).toContain('contenido completo')
  })
})

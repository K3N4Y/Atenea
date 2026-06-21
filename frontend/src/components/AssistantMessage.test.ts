// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import AssistantMessage from './AssistantMessage.vue'

describe('AssistantMessage', () => {
  it('durante el streaming muestra texto plano (no parsea Markdown) y un caret', () => {
    const wrapper = mount(AssistantMessage, {
      props: { item: { kind: 'assistant', id: 'a1', text: '**x**', streaming: true } },
    })

    // Por rendimiento, durante el streaming no se reparsea Markdown en cada delta.
    expect(wrapper.text()).toContain('**x**')
    expect(wrapper.html()).not.toContain('<strong>')
    expect(wrapper.find('[aria-hidden="true"]').exists()).toBe(true)
  })

  it('al finalizar renderiza Markdown y oculta el caret', () => {
    const wrapper = mount(AssistantMessage, {
      props: { item: { kind: 'assistant', id: 'a1', text: '**x**', streaming: false } },
    })

    expect(wrapper.html()).toContain('<strong>x</strong>')
    expect(wrapper.find('[aria-hidden="true"]').exists()).toBe(false)
  })
})

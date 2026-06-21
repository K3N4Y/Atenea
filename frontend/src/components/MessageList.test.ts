// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import MessageList from './MessageList.vue'
import type { TurnItem } from '../stores/chat'

describe('MessageList', () => {
  it('lista vacia: muestra el estado inicial zero-friction', () => {
    const wrapper = mount(MessageList, { props: { items: [] } })

    expect(wrapper.text()).toContain('atenea')
    expect(wrapper.text()).toContain('Ask anything')
  })

  it('despacha cada item a su componente segun el tipo', () => {
    const items: TurnItem[] = [
      { kind: 'user', id: 'u1', text: 'pregunta' },
      { kind: 'assistant', id: 'a1', text: '**resp**', streaming: false },
      { kind: 'reasoning', id: 'r1', text: 'idea', streaming: false, durationMs: 3000 },
      { kind: 'tool', id: 't1', callID: 'c1', name: 'echo', input: {}, status: 'success', output: 'salida', error: null },
    ]

    const wrapper = mount(MessageList, { props: { items } })

    expect(wrapper.text()).toContain('pregunta')
    expect(wrapper.html()).toContain('<strong>resp</strong>')
    expect(wrapper.text()).toContain('Thought 3s')
    expect(wrapper.text()).toContain('salida')
  })

  it('el contenedor es una region live para lectores de pantalla', () => {
    const wrapper = mount(MessageList, { props: { items: [] } })

    const log = wrapper.find('[role="log"]')
    expect(log.exists()).toBe(true)
    expect(log.attributes('aria-live')).toBe('polite')
  })
})

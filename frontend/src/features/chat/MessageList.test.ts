// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import MessageList from './MessageList.vue'
import type { TurnItem } from './types'

function configureScroller(scroller: HTMLElement, scrollTop: number) {
  Object.defineProperty(scroller, 'scrollHeight', {
    configurable: true,
    value: 1000,
  })
  Object.defineProperty(scroller, 'clientHeight', {
    configurable: true,
    value: 400,
  })
  Object.defineProperty(scroller, 'scrollTop', {
    configurable: true,
    writable: true,
    value: scrollTop,
  })
}

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
      {
        kind: 'reasoning',
        id: 'r1',
        text: 'idea',
        streaming: false,
        durationMs: 3000,
      },
      {
        kind: 'tool',
        id: 't1',
        callID: 'c1',
        name: 'echo',
        input: {},
        status: 'success',
        output: 'salida',
        error: null,
      },
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

  it('renderiza el contenido del slot al final de la conversacion, dentro del scroller', () => {
    const items: TurnItem[] = [{ kind: 'user', id: 'u1', text: 'hola' }]

    const wrapper = mount(MessageList, {
      props: { items },
      slots: { default: '<div data-test="footer">pie</div>' },
    })

    const footer = wrapper.find('[data-test="footer"]')
    expect(footer.exists()).toBe(true)
    // Vive dentro de la region scrolleable (role=log): scrollea con la conversacion.
    expect(
      wrapper.get('[role="log"]').find('[data-test="footer"]').exists(),
    ).toBe(true)
  })

  it('forwards approve/deny of a pending tool with its callID', async () => {
    const items: TurnItem[] = [
      {
        kind: 'tool',
        id: 't1',
        callID: 'c1',
        name: 'bash',
        input: { command: 'ls' },
        status: 'pending',
        output: '',
        error: null,
      },
    ]

    const wrapper = mount(MessageList, { props: { items } })

    await wrapper.get('[data-action="approve"]').trigger('click')
    expect(wrapper.emitted('approve')?.[0]).toEqual(['c1'])

    await wrapper.get('[data-action="deny"]').trigger('click')
    expect(wrapper.emitted('deny')?.[0]).toEqual(['c1'])
  })

  it('no arrastra al fondo ni oculta nueva actividad mientras el usuario lee arriba', async () => {
    const items: TurnItem[] = [
      {
        kind: 'assistant',
        id: 'a1',
        text: 'Respuesta parcial',
        streaming: true,
      },
    ]
    const wrapper = mount(MessageList, { props: { items } })
    const scroller = wrapper.get('[role="log"]').element

    configureScroller(scroller, 160)

    await wrapper.get('[role="log"]').trigger('scroll')
    await wrapper.setProps({
      items: [{ ...items[0], text: 'Respuesta parcial que sigue llegando' }],
    })
    await nextTick()

    expect(scroller.scrollTop).toBe(160)

    const activityButton = wrapper.get('button')
    expect(activityButton.text()).toMatch(/Nueva actividad|Volver al agente/)
  })

  it('sigue al agente sin mostrar actividad nueva cuando el usuario esta cerca del fondo', async () => {
    const items: TurnItem[] = [
      {
        kind: 'assistant',
        id: 'a1',
        text: 'Respuesta parcial',
        streaming: true,
      },
    ]
    const wrapper = mount(MessageList, { props: { items } })
    const scroller = wrapper.get('[role="log"]').element

    configureScroller(scroller, 570)

    await wrapper.get('[role="log"]').trigger('scroll')
    await wrapper.setProps({
      items: [{ ...items[0], text: 'Respuesta parcial que sigue llegando' }],
    })
    await nextTick()

    expect(scroller.scrollTop).toBe(1000)
    expect(wrapper.find('button').exists()).toBe(false)
  })

  it('vuelve al agente y oculta la actividad nueva al pulsar el control', async () => {
    const items: TurnItem[] = [
      {
        kind: 'assistant',
        id: 'a1',
        text: 'Respuesta parcial',
        streaming: true,
      },
    ]
    const wrapper = mount(MessageList, { props: { items } })
    const scroller = wrapper.get('[role="log"]').element

    configureScroller(scroller, 160)

    await wrapper.get('[role="log"]').trigger('scroll')
    await wrapper.setProps({
      items: [{ ...items[0], text: 'Respuesta parcial que sigue llegando' }],
    })
    await nextTick()

    const activityButton = wrapper.get('button')
    expect(activityButton.text()).toMatch(/Nueva actividad|Volver al agente/)

    await activityButton.trigger('click')

    expect(scroller.scrollTop).toBe(1000)
    expect(wrapper.find('button').exists()).toBe(false)
  })
})

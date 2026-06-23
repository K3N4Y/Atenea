// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import ChatComposer from './ChatComposer.vue'

describe('ChatComposer', () => {
  it('el textarea tiene una etiqueta accesible', () => {
    const wrapper = mount(ChatComposer, { props: { running: false } })
    expect(wrapper.find('textarea').attributes('aria-label')).toBeTruthy()
  })

  it('Enter emite send con el texto y limpia el input', async () => {
    const wrapper = mount(ChatComposer, { props: { running: false } })
    const ta = wrapper.find('textarea')

    await ta.setValue('hola')
    await ta.trigger('keydown', { key: 'Enter' })

    expect(wrapper.emitted('send')?.[0]).toEqual(['hola'])
    expect((ta.element as HTMLTextAreaElement).value).toBe('')
  })

  it('Shift+Enter no emite send (inserta salto de linea)', async () => {
    const wrapper = mount(ChatComposer, { props: { running: false } })
    const ta = wrapper.find('textarea')

    await ta.setValue('linea')
    await ta.trigger('keydown', { key: 'Enter', shiftKey: true })

    expect(wrapper.emitted('send')).toBeUndefined()
  })

  it('input vacio: el boton enviar esta deshabilitado y Enter no emite', async () => {
    const wrapper = mount(ChatComposer, { props: { running: false } })

    await wrapper.find('textarea').trigger('keydown', { key: 'Enter' })

    expect(wrapper.emitted('send')).toBeUndefined()
    expect(wrapper.find('button[aria-label="Send"]').attributes('disabled')).toBeDefined()
  })

  it('running: anuncia el estado (role=status) y emite stop al click', async () => {
    const wrapper = mount(ChatComposer, { props: { running: true } })

    expect(wrapper.find('[role="status"]').exists()).toBe(true)
    await wrapper.find('button[aria-label="Stop"]').trigger('click')

    expect(wrapper.emitted('stop')).toBeTruthy()
  })

  it('el toggle de modo emite toggle-mode al click', async () => {
    const wrapper = mount(ChatComposer, { props: { running: false } })

    await wrapper.get('[data-action="toggle-mode"]').trigger('click')

    expect(wrapper.emitted('toggle-mode')).toBeTruthy()
  })

  it('mode=plan: el toggle refleja aria-pressed=true', () => {
    const wrapper = mount(ChatComposer, { props: { running: false, mode: 'plan' } })

    expect(wrapper.get('[data-action="toggle-mode"]').attributes('aria-pressed')).toBe('true')
  })
})

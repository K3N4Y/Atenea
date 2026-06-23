// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import PlanView from './PlanView.vue'

const plan = (over: Record<string, unknown> = {}) => ({
  plan: {
    callID: 'c1',
    title: 'Mi Plan',
    markdown: '# Encabezado\n\n- punto uno',
    ...over,
  },
})

describe('PlanView', () => {
  it('renderiza el titulo y el cuerpo markdown a pantalla completa', () => {
    const wrapper = mount(PlanView, { props: plan() })

    expect(wrapper.text()).toContain('Mi Plan')
    expect(wrapper.html()).toContain('Encabezado')
    expect(wrapper.html()).toContain('punto uno')
    expect(wrapper.find('[data-action="accept"]').exists()).toBe(true)
    expect(wrapper.find('[data-action="request-change"]').exists()).toBe(true)
  })

  it('aceptar emite accept', async () => {
    const wrapper = mount(PlanView, { props: plan() })

    await wrapper.get('[data-action="accept"]').trigger('click')

    expect(wrapper.emitted('accept')).toBeTruthy()
  })

  it('solicitar cambio revela el textarea y al enviar emite request-change con el texto', async () => {
    const wrapper = mount(PlanView, { props: plan() })

    expect(wrapper.find('textarea').exists()).toBe(false)
    await wrapper.get('[data-action="request-change"]').trigger('click')

    const ta = wrapper.find('textarea')
    expect(ta.exists()).toBe(true)
    await ta.setValue('cambia el paso 2')
    await wrapper.get('[data-action="submit-change"]').trigger('click')

    expect(wrapper.emitted('request-change')?.[0]).toEqual(['cambia el paso 2'])
  })

  it('feedback vacio/espacios: no emite request-change', async () => {
    const wrapper = mount(PlanView, { props: plan() })

    await wrapper.get('[data-action="request-change"]').trigger('click')
    await wrapper.find('textarea').setValue('   ')
    await wrapper.get('[data-action="submit-change"]').trigger('click')

    expect(wrapper.emitted('request-change')).toBeUndefined()
  })

  it('titulo vacio: cae a "Plan"', () => {
    const wrapper = mount(PlanView, { props: plan({ title: '' }) })

    expect(wrapper.text()).toContain('Plan')
  })
})

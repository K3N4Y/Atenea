// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import PlanCard from './PlanCard.vue'

const plan = (over: Record<string, unknown> = {}) => ({
  plan: {
    callID: 'c1',
    title: 'Mi Plan',
    markdown: '# Encabezado\n\n- punto uno',
    ...over,
  },
})

// PlanCard es el plan minimizado: vive en el flujo de la conversacion (como una
// tool card) y solo ofrece expandir; las acciones viven en la vista expandida.
describe('PlanCard (plan minimizado en la conversacion)', () => {
  it('muestra el titulo del plan y la accion de expandir', () => {
    const wrapper = mount(PlanCard, { props: plan() })

    expect(wrapper.text()).toContain('Mi Plan')
    expect(wrapper.text()).toContain('Expandir')
  })

  it('titulo vacio: cae a "Plan"', () => {
    const wrapper = mount(PlanCard, { props: plan({ title: '' }) })

    expect(wrapper.text()).toContain('Plan')
  })

  it('no renderiza el cuerpo del plan (solo es un resumen colapsado)', () => {
    const wrapper = mount(PlanCard, { props: plan() })

    expect(wrapper.html()).not.toContain('Encabezado')
    expect(wrapper.html()).not.toContain('punto uno')
  })

  it('al pulsar la tarjeta emite expand', async () => {
    const wrapper = mount(PlanCard, { props: plan() })

    await wrapper.get('[data-action="expand"]').trigger('click')

    expect(wrapper.emitted('expand')).toBeTruthy()
  })

  it('como una tool: no ofrece aceptar ni solicitar cambio (solo expandir)', () => {
    const wrapper = mount(PlanCard, { props: plan() })

    expect(wrapper.find('[data-action="accept"]').exists()).toBe(false)
    expect(wrapper.find('[data-action="request-change"]').exists()).toBe(false)
  })
})

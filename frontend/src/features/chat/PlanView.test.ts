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
  it('renderiza el titulo y el cuerpo markdown con sus acciones', () => {
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

// Edge cases que un usuario real encuentra frente al overlay del plan: escribe
// feedback y se arrepiente, mezcla acciones, pega texto raro, espera que Enter
// envie (como en el composer), etc. El overlay es a pantalla completa, asi que
// estas son TODAS las interacciones que el usuario puede hacer con un plan.
describe('PlanView: edge cases de usuario', () => {
  it('Cancelar cierra el panel y descarta el borrador: al reabrir el textarea esta vacio', async () => {
    const wrapper = mount(PlanView, { props: plan() })

    await wrapper.get('[data-action="request-change"]').trigger('click')
    await wrapper.find('textarea').setValue('me arrepenti')
    await wrapper.get('[data-action="cancel-change"]').trigger('click')

    // El panel se cierra sin emitir.
    expect(wrapper.find('textarea').exists()).toBe(false)
    expect(wrapper.emitted('request-change')).toBeUndefined()

    // Reabrir: el borrador NO sobrevive a Cancelar.
    await wrapper.get('[data-action="request-change"]').trigger('click')
    expect(
      (wrapper.find('textarea').element as HTMLTextAreaElement).value,
    ).toBe('')
  })

  it('tras enviar un cambio, reabrir el panel muestra el textarea vacio (no conserva el texto enviado)', async () => {
    const wrapper = mount(PlanView, { props: plan() })

    await wrapper.get('[data-action="request-change"]').trigger('click')
    await wrapper.find('textarea').setValue('cambia el paso 2')
    await wrapper.get('[data-action="submit-change"]').trigger('click')

    expect(wrapper.emitted('request-change')?.[0]).toEqual(['cambia el paso 2'])
    // El panel se cerro solo al enviar.
    expect(wrapper.find('textarea').exists()).toBe(false)

    await wrapper.get('[data-action="request-change"]').trigger('click')
    expect(
      (wrapper.find('textarea').element as HTMLTextAreaElement).value,
    ).toBe('')
  })

  it('cerrar el panel con el propio boton "Solicitar cambio" (toggle) CONSERVA el borrador al reabrir', async () => {
    // Asimetria real: "Cancelar" limpia el borrador, pero togglear el boton de la
    // cabecera solo oculta el panel; el texto reaparece al volver a abrir.
    const wrapper = mount(PlanView, { props: plan() })

    await wrapper.get('[data-action="request-change"]').trigger('click')
    await wrapper.find('textarea').setValue('borrador a medias')
    // Segundo click en el boton de la cabecera: oculta el panel (toggle off).
    await wrapper.get('[data-action="request-change"]').trigger('click')
    expect(wrapper.find('textarea').exists()).toBe(false)

    // Tercer click: reabre y el borrador sigue ahi.
    await wrapper.get('[data-action="request-change"]').trigger('click')
    expect(
      (wrapper.find('textarea').element as HTMLTextAreaElement).value,
    ).toBe('borrador a medias')
  })

  it('aceptar con el panel de feedback abierto emite accept y descarta el borrador (no emite request-change)', async () => {
    const wrapper = mount(PlanView, { props: plan() })

    await wrapper.get('[data-action="request-change"]').trigger('click')
    await wrapper.find('textarea').setValue('esto no se va a enviar')
    await wrapper.get('[data-action="accept"]').trigger('click')

    expect(wrapper.emitted('accept')).toBeTruthy()
    expect(wrapper.emitted('request-change')).toBeUndefined()
  })

  it('Enter en el textarea NO envia el cambio (a diferencia del composer)', async () => {
    const wrapper = mount(PlanView, { props: plan() })

    await wrapper.get('[data-action="request-change"]').trigger('click')
    const ta = wrapper.find('textarea')
    await ta.setValue('cambia esto')
    await ta.trigger('keydown', { key: 'Enter' })

    // El plan solo se envia con el boton "Enviar cambio"; Enter inserta salto.
    expect(wrapper.emitted('request-change')).toBeUndefined()
  })

  it('feedback con espacios alrededor de texto real: se emite recortado', async () => {
    const wrapper = mount(PlanView, { props: plan() })

    await wrapper.get('[data-action="request-change"]').trigger('click')
    await wrapper.find('textarea').setValue('   cambia el paso 2   ')
    await wrapper.get('[data-action="submit-change"]').trigger('click')

    expect(wrapper.emitted('request-change')?.[0]).toEqual(['cambia el paso 2'])
  })

  it('feedback multilinea: se emite con los saltos internos, recortado en los extremos', async () => {
    const wrapper = mount(PlanView, { props: plan() })

    await wrapper.get('[data-action="request-change"]').trigger('click')
    await wrapper.find('textarea').setValue('\n\nlinea uno\nlinea dos\n\n')
    await wrapper.get('[data-action="submit-change"]').trigger('click')

    expect(wrapper.emitted('request-change')?.[0]).toEqual([
      'linea uno\nlinea dos',
    ])
  })

  it('feedback de solo tabs y saltos de linea: no emite request-change', async () => {
    const wrapper = mount(PlanView, { props: plan() })

    await wrapper.get('[data-action="request-change"]').trigger('click')
    await wrapper.find('textarea').setValue('\t\n  \n\t')
    await wrapper.get('[data-action="submit-change"]').trigger('click')

    expect(wrapper.emitted('request-change')).toBeUndefined()
  })

  it('si el agente re-presenta el plan (cambia la prop), el cuerpo refleja el plan vigente', async () => {
    const wrapper = mount(PlanView, { props: plan() })
    expect(wrapper.text()).toContain('Mi Plan')

    await wrapper.setProps({
      plan: {
        callID: 'c2',
        title: 'Plan v2',
        markdown: '# Revisado\n\n- nuevo paso',
      },
    })

    expect(wrapper.text()).toContain('Plan v2')
    expect(wrapper.html()).toContain('Revisado')
    expect(wrapper.html()).toContain('nuevo paso')
  })
})

// El plan ya no es un overlay a pantalla completa: vive en la columna del chat y
// se puede minimizar (barra delgada) o expandir (cuerpo completo). Esto deja la
// sidebar libre para cambiar de sesion mientras un plan sigue pendiente.
describe('PlanView: minimizar', () => {
  it('ofrece un boton de minimizar que emite minimize', async () => {
    const wrapper = mount(PlanView, { props: plan() })

    await wrapper.get('[data-action="minimize"]').trigger('click')

    expect(wrapper.emitted('minimize')).toBeTruthy()
  })

  it('minimizar no emite accept ni request-change', async () => {
    const wrapper = mount(PlanView, { props: plan() })

    await wrapper.get('[data-action="minimize"]').trigger('click')

    expect(wrapper.emitted('accept')).toBeUndefined()
    expect(wrapper.emitted('request-change')).toBeUndefined()
  })
})

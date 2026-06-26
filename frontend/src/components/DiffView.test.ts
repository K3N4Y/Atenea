// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import DiffView from './DiffView.vue'

const DIFF = ['--- a/foo.go', '+++ b/foo.go', '@@ -1,3 +1,3 @@', ' a', '-b', '+B', ' c', ''].join('\n')

describe('DiffView', () => {
  it('renderiza filas add/del/context con su tipo', () => {
    const w = mount(DiffView, { props: { diff: DIFF } })
    expect(w.findAll('[data-type="add"]')).toHaveLength(1)
    expect(w.findAll('[data-type="del"]')).toHaveLength(1)
    expect(w.findAll('[data-type="context"]')).toHaveLength(2)
  })

  it('muestra el nombre del archivo del diff', () => {
    const w = mount(DiffView, { props: { diff: DIFF } })
    expect(w.text()).toContain('foo.go')
  })

  it('tiene gutter + en las adiciones y - en los borrados', () => {
    const w = mount(DiffView, { props: { diff: DIFF } })
    expect(w.get('[data-type="add"]').text()).toContain('+')
    expect(w.get('[data-type="del"]').text()).toContain('-')
  })

  it('sanitiza el contenido: una linea con <img onerror> no crea un elemento img', () => {
    const evil = ['--- a/x.txt', '+++ b/x.txt', '@@ -0,0 +1 @@', '+<img src=x onerror=alert(1)>', ''].join(
      '\n',
    )
    const w = mount(DiffView, { props: { diff: evil } })
    expect(w.find('img').exists()).toBe(false)
  })

  it('extension desconocida no rompe el render', () => {
    const raw = ['--- a/Makefile', '+++ b/Makefile', '@@ -0,0 +1 @@', '+all:', ''].join('\n')
    const w = mount(DiffView, { props: { diff: raw } })
    expect(w.findAll('[data-type="add"]')).toHaveLength(1)
  })

  it('diff vacio no renderiza filas', () => {
    const w = mount(DiffView, { props: { diff: '' } })
    expect(w.findAll('[data-type="add"]')).toHaveLength(0)
    expect(w.findAll('[data-type="del"]')).toHaveLength(0)
  })

  // El contenedor recorta sus esquinas con overflow-hidden; un radio grande
  // comeria parte del fondo coloreado de la primera/ultima linea del diff. Por
  // eso va con esquinas rectas (sin rounded-soft).
  it('no redondea las esquinas del contenedor para no tapar los cambios', () => {
    const w = mount(DiffView, { props: { diff: DIFF } })
    expect(w.classes()).not.toContain('rounded-soft')
  })

  it('por defecto muestra las lineas (expandido)', () => {
    const w = mount(DiffView, { props: { diff: DIFF } })
    expect(w.findAll('[data-type]').length).toBeGreaterThan(0)
  })

  it('colapsa al hacer click en la cabecera y oculta las lineas', async () => {
    const w = mount(DiffView, { props: { diff: DIFF } })
    await w.get('[data-action="toggle"]').trigger('click')
    expect(w.findAll('[data-type]')).toHaveLength(0)
  })

  it('un segundo click vuelve a expandir', async () => {
    const w = mount(DiffView, { props: { diff: DIFF } })
    const toggle = w.get('[data-action="toggle"]')
    await toggle.trigger('click')
    await toggle.trigger('click')
    expect(w.findAll('[data-type]').length).toBeGreaterThan(0)
  })

  it('la cabecera resume el conteo de adiciones y borrados', () => {
    const w = mount(DiffView, { props: { diff: DIFF } })
    const header = w.get('[data-action="toggle"]').text()
    expect(header).toContain('+1')
    expect(header).toContain('-1')
  })
})

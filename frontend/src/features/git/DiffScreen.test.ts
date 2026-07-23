// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import DiffScreen from './DiffScreen.vue'

// Diff unificado tipico: un cambio (b -> B) entre dos lineas de contexto.
const DIFF = [
  '--- a/foo.go',
  '+++ b/foo.go',
  '@@ -1,3 +1,3 @@',
  ' a',
  '-b',
  '+B',
  ' c',
  '',
].join('\n')

describe('DiffScreen', () => {
  it('muestra el archivo en la cabecera', () => {
    const w = mount(DiffScreen, { props: { path: 'foo.go', diff: DIFF } })
    expect(w.text()).toContain('foo.go')
  })

  it('renderiza side-by-side: el del a la izquierda y el add a la derecha', () => {
    const w = mount(DiffScreen, { props: { path: 'foo.go', diff: DIFF } })
    const del = w.get('[data-side="left"][data-type="del"]')
    const add = w.get('[data-side="right"][data-type="add"]')
    expect(del.text()).toContain('b')
    expect(add.text()).toContain('B')
  })

  it('el boton de cerrar emite close', async () => {
    const w = mount(DiffScreen, { props: { path: 'foo.go', diff: DIFF } })
    await w.get('[data-action="close"]').trigger('click')
    expect(w.emitted('close')).toBeTruthy()
  })

  it('la tecla Escape cierra la pantalla', async () => {
    const w = mount(DiffScreen, {
      props: { path: 'foo.go', diff: DIFF },
      attachTo: document.body,
    })
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(w.emitted('close')).toBeTruthy()
    w.unmount()
  })

  it('sanitiza el contenido: una linea con <img onerror> no crea un img', () => {
    const evil = [
      '--- /dev/null',
      '+++ b/x.txt',
      '@@ -0,0 +1 @@',
      '+<img src=x onerror=alert(1)>',
      '',
    ].join('\n')
    const w = mount(DiffScreen, { props: { path: 'x.txt', diff: evil } })
    expect(w.find('img').exists()).toBe(false)
  })

  it('diff vacio muestra un estado sin cambios y ninguna fila', () => {
    const w = mount(DiffScreen, { props: { path: 'foo.go', diff: '' } })
    expect(w.findAll('[data-side]')).toHaveLength(0)
    expect(w.text().toLowerCase()).toContain('sin cambios')
  })
})

// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import MarkdownContent from './MarkdownContent.vue'

// Boton "copiar codigo" sobre los bloques <pre> que renderiza la IA. El HTML va
// inyectado con v-html, asi que el boton se agrega al DOM tras el render y al
// hacer click copia el texto del <code> (sin la etiqueta del propio boton).
const ONE = '```js\nconst x = 1\n```'
const TWO = '```js\nconst x = 1\n```\n\n```go\nfmt.Println("hi")\n```'

function stubClipboard() {
  const writeText = vi.fn().mockResolvedValue(undefined)
  Object.defineProperty(navigator, 'clipboard', {
    value: { writeText },
    configurable: true,
    writable: true,
  })
  return writeText
}

describe('MarkdownContent boton copiar codigo', () => {
  beforeEach(() => {
    stubClipboard()
  })

  it('agrega un boton copiar a cada bloque de codigo', () => {
    const w = mount(MarkdownContent, { props: { text: ONE } })
    expect(w.findAll('[data-action="copy"]')).toHaveLength(1)
  })

  it('no agrega boton al codigo inline (solo a los bloques <pre>)', () => {
    const w = mount(MarkdownContent, { props: { text: 'texto con `inline`' } })
    expect(w.findAll('[data-action="copy"]')).toHaveLength(0)
  })

  it('agrega un boton por cada bloque cuando hay varios', () => {
    const w = mount(MarkdownContent, { props: { text: TWO } })
    expect(w.findAll('[data-action="copy"]')).toHaveLength(2)
  })

  it('al hacer click copia el texto del codigo al portapapeles', async () => {
    const writeText = stubClipboard()
    const w = mount(MarkdownContent, { props: { text: ONE } })
    await w.get('[data-action="copy"]').trigger('click')
    expect(writeText).toHaveBeenCalledTimes(1)
    expect(writeText.mock.calls[0][0]).toContain('const x = 1')
  })

  it('copia exactamente el codigo del bloque, sin texto del boton', async () => {
    const writeText = stubClipboard()
    const w = mount(MarkdownContent, { props: { text: ONE } })
    await w.get('[data-action="copy"]').trigger('click')
    expect(writeText.mock.calls[0][0]).toBe('const x = 1\n')
  })

  it('el boton muestra un icono (svg), no texto', () => {
    const w = mount(MarkdownContent, { props: { text: ONE } })
    const btn = w.get('[data-action="copy"]')
    expect(btn.find('svg').exists()).toBe(true)
    expect(btn.text()).toBe('')
  })

  it('tras copiar marca el boton como copiado (aria-label)', async () => {
    const w = mount(MarkdownContent, { props: { text: ONE } })
    const btn = w.get('[data-action="copy"]')
    expect(btn.attributes('aria-label')).toBe('Copiar codigo')
    await btn.trigger('click')
    await flushPromises()
    expect(btn.attributes('aria-label')).toBe('Copiado')
  })

  it('al cambiar el texto no duplica los botones', async () => {
    const w = mount(MarkdownContent, { props: { text: ONE } })
    await w.setProps({ text: TWO })
    expect(w.findAll('[data-action="copy"]')).toHaveLength(2)
  })
})

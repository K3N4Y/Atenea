// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { renderMarkdown } from './markdown'

// Render de Markdown para las respuestas de la IA (front.md Fase 3). El HTML
// se sanitiza antes de inyectarse (DOMPurify) y los bloques de codigo se
// resaltan con highlight.js.
describe('renderMarkdown', () => {
  it('convierte Markdown a HTML', () => {
    expect(renderMarkdown('**bold**')).toContain('<strong>bold</strong>')
  })

  it('sanitiza HTML peligroso', () => {
    const html = renderMarkdown('hola <script>alert(1)</script>')
    expect(html).not.toContain('<script')
  })

  it('resalta bloques de codigo con clases de highlight.js', () => {
    const html = renderMarkdown('```js\nconst x = 1\n```')
    expect(html).toContain('<pre')
    expect(html).toContain('class="hljs')
  })
})

import { marked } from 'marked'
import { markedHighlight } from 'marked-highlight'
import hljs from 'highlight.js/lib/common'
import DOMPurify from 'dompurify'

// Render de Markdown para las respuestas de la IA. Los bloques de codigo se
// resaltan con highlight.js (subset comun) y todo el HTML se sanitiza antes de
// inyectarse en la vista (v-html) con DOMPurify.
marked.use(
  markedHighlight({
    langPrefix: 'hljs language-',
    highlight(code, lang) {
      const language = lang && hljs.getLanguage(lang) ? lang : 'plaintext'
      return hljs.highlight(code, { language }).value
    },
  }),
)

export function renderMarkdown(src: string): string {
  const raw = marked.parse(src ?? '', { async: false }) as string
  return DOMPurify.sanitize(raw)
}

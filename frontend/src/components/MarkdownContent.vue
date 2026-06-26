<script lang="ts" setup>
import { computed, h, onMounted, ref, render, watch, type Component } from 'vue'
import { PhCopy, PhCheck } from '@phosphor-icons/vue'
import { renderMarkdown } from '../lib/markdown'

// Render del Markdown de la IA. El HTML ya viene sanitizado por renderMarkdown
// (DOMPurify), asi que es seguro inyectarlo con v-html.
const props = defineProps<{ text: string }>()
const html = computed(() => renderMarkdown(props.text))

// Como el codigo se inyecta con v-html no se puede colgar un boton de Vue dentro
// de cada <pre>. Tras cada render recorremos el contenedor y añadimos a mano un
// boton "copiar" por bloque (universalmente esperado). El watch corre con
// flush:'post', es decir despues de que Vue repinta el v-html, asi el boton
// siempre se reañade sobre el HTML nuevo.
const root = ref<HTMLElement | null>(null)

// El boton es solo el icono de Phosphor. Lo montamos con render() porque vive en
// un nodo creado a mano (fuera del template), no en el v-html.
function paintIcon(btn: HTMLElement, icon: Component): void {
  render(h(icon, { size: 15, weight: 'bold' }), btn)
}

async function copy(pre: HTMLElement, btn: HTMLButtonElement): Promise<void> {
  // textContent del <code>, no del <pre>: el boton es un icono sin texto, pero
  // asi queda explicito que solo se copia el codigo.
  const code = pre.querySelector('code')?.textContent ?? ''
  try {
    await navigator.clipboard.writeText(code)
  } catch {
    return
  }
  btn.setAttribute('aria-label', 'Copiado')
  paintIcon(btn, PhCheck)
  setTimeout(() => {
    btn.setAttribute('aria-label', 'Copiar codigo')
    paintIcon(btn, PhCopy)
  }, 1500)
}

function decorate(): void {
  const el = root.value
  if (!el) return
  el.querySelectorAll('pre').forEach((pre) => {
    if (pre.querySelector('button[data-action="copy"]')) return
    const btn = document.createElement('button')
    btn.type = 'button'
    btn.dataset.action = 'copy'
    btn.className = 'md-copy'
    btn.setAttribute('aria-label', 'Copiar codigo')
    paintIcon(btn, PhCopy)
    btn.addEventListener('click', () => copy(pre, btn))
    pre.appendChild(btn)
  })
}

onMounted(decorate)
watch(html, decorate, { flush: 'post' })
</script>

<template>
  <div ref="root" class="md" v-html="html"></div>
</template>

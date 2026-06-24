<script lang="ts" setup>
import { computed } from 'vue'
import hljs from 'highlight.js/lib/common'
import DOMPurify from 'dompurify'
import { parseDiff, pathFromDiff, langForPath, type DiffLine } from '../lib/diff'
import { basename } from '../lib/path'

// Render del diff de edit/write (identidad §8): cabecera con el archivo y, debajo,
// las lineas con gutter +/- y fondo verde/rojo/neutro. El texto de cada linea se
// resalta con highlight.js (mismo subset que el Markdown) y se SANITIZA con
// DOMPurify antes de inyectarse con v-html, porque es contenido arbitrario del
// repo. Las lineas meta (headers --- / +++) se omiten: el archivo ya va en la
// cabecera.
const props = defineProps<{ diff: string }>()

const path = computed(() => pathFromDiff(props.diff))
const fileName = computed(() => basename(path.value))

const lang = computed(() => {
  const l = langForPath(path.value)
  return hljs.getLanguage(l) ? l : 'plaintext'
})

const lines = computed(() => parseDiff(props.diff).filter((l) => l.type !== 'meta'))

function gutter(l: DiffLine): string {
  if (l.type === 'add') return '+'
  if (l.type === 'del') return '-'
  return ' '
}

// HTML resaltado y sanitizado de una linea de codigo. hljs ya escapa el codigo;
// DOMPurify es el cinturon de seguridad sobre el v-html.
function codeHtml(text: string): string {
  const html = hljs.highlight(text, { language: lang.value }).value
  return DOMPurify.sanitize(html)
}

const lineClass: Record<DiffLine['type'], string> = {
  add: 'bg-green-500/15',
  del: 'bg-red-500/15',
  context: '',
  hunk: 'bg-black/[0.04] text-accent select-none',
  meta: '',
}
</script>

<template>
  <div class="overflow-hidden rounded-soft border border-black/[0.06] text-xs">
    <div class="border-b border-black/[0.06] bg-black/[0.04] px-3 py-1.5 font-mono opacity-70">
      {{ fileName }}
    </div>
    <div class="overflow-x-auto font-mono leading-relaxed">
      <div
        v-for="(l, i) in lines"
        :key="i"
        :data-type="l.type"
        class="flex whitespace-pre"
        :class="lineClass[l.type]"
      >
        <template v-if="l.type === 'hunk'">
          <span class="px-3 py-0.5 opacity-70">{{ l.text }}</span>
        </template>
        <template v-else>
          <span class="w-5 shrink-0 select-none px-1 text-center opacity-40">{{ gutter(l) }}</span>
          <span class="hljs flex-1 pr-3" v-html="codeHtml(l.text)"></span>
        </template>
      </div>
    </div>
  </div>
</template>

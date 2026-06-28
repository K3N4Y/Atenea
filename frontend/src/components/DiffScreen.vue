<script lang="ts" setup>
import { computed, onMounted, onUnmounted } from 'vue'
import { PhX } from '@phosphor-icons/vue'
import hljs from 'highlight.js/lib/common'
import DOMPurify from 'dompurify'
import { buildSideBySide, langForPath, type DiffCell } from '../lib/diff'
import { basename } from '../lib/path'

// Pantalla de diff a dos columnas (estilo VSCode): se abre desde el panel de git
// al seleccionar un archivo. Overlay sobre la columna del chat (no tapa la sidebar
// ni el panel), como PlanView. Recibe el path y el diff unificado ya armado por el
// backend; el modelo side-by-side y el resaltado se derivan aca. El contenido del
// repo es arbitrario, asi que cada celda se resalta con highlight.js (que escapa)
// y se SANITIZA con DOMPurify antes del v-html.
const props = defineProps<{ path: string; diff: string }>()
const emit = defineEmits<{ close: [] }>()

const fileName = computed(() => basename(props.path))
const rows = computed(() => buildSideBySide(props.diff))

const lang = computed(() => {
  const l = langForPath(props.path)
  return hljs.getLanguage(l) ? l : 'plaintext'
})

// Resumen +adds/-dels para la cabecera (cuenta celdas reales, no vacias).
const adds = computed(
  () => rows.value.filter((r) => r.right.kind === 'add').length,
)
const dels = computed(
  () => rows.value.filter((r) => r.left.kind === 'del').length,
)

function codeHtml(text: string): string {
  if (text === '') return ''
  const html = hljs.highlight(text, { language: lang.value }).value
  return DOMPurify.sanitize(html)
}

// Fondo de cada celda por tipo. 'empty' es el relleno cuando un lado tiene menos
// lineas: gris muy tenue para leerse como "aqui no hay nada".
const cellClass: Record<DiffCell['kind'], string> = {
  add: 'bg-green-500/15',
  del: 'bg-red-500/15',
  context: '',
  empty: 'bg-black/[0.02]',
}

// Esc cierra la pantalla (afordancia esperada de un overlay full-screen).
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') emit('close')
}
onMounted(() => window.addEventListener('keydown', onKey))
onUnmounted(() => window.removeEventListener('keydown', onKey))
</script>

<template>
  <div
    role="dialog"
    aria-label="Diff"
    class="absolute inset-0 z-30 flex flex-col bg-paper"
  >
    <!-- Cabecera: nombre del archivo + ruta tenue, resumen de cambios y cerrar. -->
    <header class="flex items-center gap-3 border-b border-black/5 px-6 py-3">
      <div class="flex min-w-0 flex-1 items-baseline gap-2">
        <span class="truncate text-sm font-medium">{{ fileName }}</span>
        <span class="truncate text-xs opacity-40">{{ props.path }}</span>
      </div>
      <span class="flex shrink-0 gap-1.5 font-mono text-xs">
        <span class="text-green-600">+{{ adds }}</span>
        <span class="text-red-600">-{{ dels }}</span>
      </span>
      <button
        type="button"
        data-action="close"
        aria-label="Cerrar diff"
        class="flex h-8 w-8 shrink-0 items-center justify-center rounded-full transition hover:bg-black/[0.05] active:scale-95"
        @click="emit('close')"
      >
        <PhX :size="18" weight="regular" />
      </button>
    </header>

    <!-- Cuerpo: dos columnas (viejo | nuevo) alineadas fila a fila. -->
    <div class="min-h-0 flex-1 overflow-auto font-mono text-xs leading-relaxed">
      <div
        v-if="!rows.length"
        class="flex h-full items-center justify-center text-sm opacity-40"
      >
        Sin cambios para mostrar
      </div>

      <div v-for="(row, i) in rows" :key="i" class="grid grid-cols-2">
        <!-- Fila de hunk: ocupa ambas columnas, separa rangos. -->
        <template v-if="row.hunk !== null">
          <div
            data-row="hunk"
            class="col-span-2 bg-black/[0.04] px-3 py-0.5 text-accent opacity-70 select-none"
          >
            {{ row.hunk }}
          </div>
        </template>

        <!-- Fila normal: celda vieja a la izquierda, nueva a la derecha. -->
        <template v-else>
          <div
            data-side="left"
            :data-type="row.left.kind"
            class="flex border-r border-black/5 whitespace-pre"
            :class="cellClass[row.left.kind]"
          >
            <span
              class="w-10 shrink-0 select-none px-1 text-right opacity-30"
              >{{ row.left.num ?? '' }}</span
            >
            <span
              class="hljs flex-1 pr-3"
              v-html="codeHtml(row.left.text)"
            ></span>
          </div>
          <div
            data-side="right"
            :data-type="row.right.kind"
            class="flex whitespace-pre"
            :class="cellClass[row.right.kind]"
          >
            <span
              class="w-10 shrink-0 select-none px-1 text-right opacity-30"
              >{{ row.right.num ?? '' }}</span
            >
            <span
              class="hljs flex-1 pr-3"
              v-html="codeHtml(row.right.text)"
            ></span>
          </div>
        </template>
      </div>
    </div>
  </div>
</template>

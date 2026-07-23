<script lang="ts" setup>
import { ref, computed } from 'vue'
import {
  PhFolder,
  PhFolderOpen,
  PhCaretDown,
  PhCheck,
} from '@phosphor-icons/vue'
import type { WorkspaceOption } from './workspaces'

// Selector de carpeta del chat nuevo: un dropdown PROPIO (no <select> nativo, cuyo
// popup lo dibuja el SO y no se puede estilar: chocaba con el look del resto). Un
// disparador muestra la carpeta vigente; al pulsarlo abre un menu flotante con las
// carpetas conocidas (la vigente resaltada con check) y una entrada "Browse folder"
// que abre el dialogo nativo. Es presentacional: emite `select` con la ruta elegida
// y `browse`; la carpeta vigente llega por prop. La vista cablea
// select->pickWorkspace y browse->selectWorkspace en el store.
const props = withDefaults(
  defineProps<{
    workspace?: string
    options?: WorkspaceOption[]
  }>(),
  { workspace: '', options: () => [] },
)
const emit = defineEmits<{
  select: [path: string]
  browse: []
}>()

const open = ref(false)

// La etiqueta del disparador: el nombre de la carpeta vigente (knownWorkspaces la
// incluye siempre). Si no apareciera, cae a un texto de respaldo.
const currentLabel = computed(
  () =>
    props.options.find((o) => o.path === props.workspace)?.label ??
    'Select a folder',
)

function toggle(): void {
  open.value = !open.value
}
function close(): void {
  open.value = false
}
// Elegir cierra el menu y emite: una ruta concreta o el dialogo nativo.
function choose(path: string): void {
  close()
  emit('select', path)
}
function chooseBrowse(): void {
  close()
  emit('browse')
}
</script>

<template>
  <div class="relative mx-auto mb-3 w-full max-w-md" @keydown.escape="close">
    <label
      id="workspace-label"
      class="block px-1 pb-1 text-[11px] uppercase tracking-wide opacity-40"
    >
      Working folder
    </label>

    <!-- Disparador: parece un campo, muestra la carpeta vigente y abre el menu. -->
    <button
      type="button"
      data-workspace-trigger
      aria-haspopup="listbox"
      :aria-expanded="open"
      aria-labelledby="workspace-label"
      class="flex w-full items-center gap-2 rounded-soft bg-black/[0.04] px-3 py-2 text-left text-sm transition hover:bg-black/[0.06] focus:outline-none focus:ring-2 focus:ring-accent/20"
      @click="toggle"
    >
      <PhFolder :size="16" weight="regular" class="shrink-0 opacity-70" />
      <span class="min-w-0 flex-1 truncate">{{ currentLabel }}</span>
      <PhCaretDown
        :size="14"
        weight="bold"
        class="shrink-0 opacity-50 transition-transform"
        :class="open ? 'rotate-180' : ''"
      />
    </button>

    <!-- Fondo para cerrar al hacer click fuera (como el menu "+" del DevToolsPanel). -->
    <div
      v-if="open"
      aria-hidden="true"
      class="fixed inset-0 z-30"
      @click="close"
    ></div>

    <!-- Menu flotante: popover propio, totalmente estilado al look del app. Anima
         solo al abrir/cerrar (origin-top: crece hacia abajo desde el disparador). -->
    <Transition
      enter-active-class="transition duration-150 ease-snappy"
      enter-from-class="opacity-0 scale-[0.98] -translate-y-1"
      leave-active-class="transition duration-[120ms] ease-snappy"
      leave-to-class="opacity-0 scale-[0.98] -translate-y-1"
    >
      <div
        v-if="open"
        role="listbox"
        aria-labelledby="workspace-label"
        class="absolute inset-x-0 z-40 mt-1 origin-top rounded-soft border border-black/5 bg-paper p-1 shadow-lg"
      >
        <button
          v-for="option in options"
          :key="option.path"
          type="button"
          role="option"
          :data-workspace-option="option.path"
          :title="option.path"
          :aria-selected="option.path === workspace ? 'true' : 'false'"
          class="flex w-full items-center gap-2 rounded-[0.5rem] px-2.5 py-2 text-left text-sm transition active:scale-[0.99]"
          :class="
            option.path === workspace
              ? 'bg-accent/10 text-accent'
              : 'opacity-80 hover:bg-black/[0.04] hover:opacity-100'
          "
          @click="choose(option.path)"
        >
          <PhFolder :size="16" weight="regular" class="shrink-0" />
          <span class="min-w-0 flex-1 truncate">{{ option.label }}</span>
          <PhCheck
            v-if="option.path === workspace"
            :size="14"
            weight="bold"
            class="shrink-0"
          />
        </button>

        <div class="my-1 border-t border-black/5"></div>

        <!-- Buscar carpeta: abre el dialogo nativo para una carpeta sin chats aun. -->
        <button
          type="button"
          data-browse-workspace
          class="flex w-full items-center gap-2 rounded-[0.5rem] px-2.5 py-2 text-left text-sm opacity-70 transition hover:bg-black/[0.04] hover:opacity-100 active:scale-[0.99]"
          @click="chooseBrowse"
        >
          <PhFolderOpen :size="16" weight="regular" class="shrink-0" />
          Browse folder…
        </button>
      </div>
    </Transition>
  </div>
</template>

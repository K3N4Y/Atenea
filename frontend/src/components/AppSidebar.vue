<script lang="ts" setup>
import { PhPlus } from '@phosphor-icons/vue'
import { useUiStore } from '../stores/ui'

// Sidebar persistente (identidad §4): el estado colapsado vive en el store de
// UI y se conserva entre sesiones (pinia-plugin-persistedstate). En pantallas
// anchas colapsa por ancho (queda en el flujo); en pantallas estrechas se
// comporta como panel superpuesto que entra/sale por la izquierda. El historial
// de chats es propiedad del backend y se rehidratara mas adelante.
const emit = defineEmits<{ 'new-chat': [] }>()
const ui = useUiStore()
</script>

<template>
  <aside
    id="app-sidebar"
    aria-label="Chat history"
    :data-collapsed="ui.sidebarCollapsed ? 'true' : 'false'"
    class="fixed inset-y-0 left-0 z-30 flex h-full w-64 flex-col overflow-hidden border-black/5 bg-paper transition-all duration-300 ease-out md:static md:bg-black/[0.015]"
    :class="
      ui.sidebarCollapsed
        ? '-translate-x-full border-r-0 md:w-0 md:translate-x-0'
        : 'translate-x-0 border-r'
    "
  >
    <div class="flex w-64 flex-col gap-1 p-3">
      <p class="px-2 py-3 text-lg tracking-tight">atenea</p>

      <button
        type="button"
        class="flex items-center gap-2 rounded-full px-4 py-2.5 text-left text-sm transition hover:bg-black/[0.04]"
        @click="emit('new-chat')"
      >
        <PhPlus :size="18" weight="regular" />
        New chat
      </button>

      <p class="mt-6 px-4 text-xs opacity-40">Chat history coming soon.</p>
    </div>
  </aside>
</template>

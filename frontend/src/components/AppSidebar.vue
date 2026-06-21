<script lang="ts" setup>
import { PhPlus } from '@phosphor-icons/vue'
import { useUiStore } from '../stores/ui'

// Sidebar persistente (identidad §4). El estado colapsado vive en el store de
// UI; su persistencia entre sesiones llega en la Fase 4. El historial de chats
// es propiedad del backend y se rehidratara tambien en la Fase 4.
const emit = defineEmits<{ 'new-chat': [] }>()
const ui = useUiStore()
</script>

<template>
  <aside
    class="flex h-full shrink-0 flex-col overflow-hidden border-r border-black/5 bg-black/[0.015] transition-[width] duration-300 ease-out"
    :class="ui.sidebarCollapsed ? 'w-0 border-r-0' : 'w-64'"
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

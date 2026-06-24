<script lang="ts" setup>
import { ref } from 'vue'
import { PhPlus, PhTrash, PhCheck, PhX } from '@phosphor-icons/vue'
import { useUiStore } from '../stores/ui'
import type { SessionSummary } from '../stores/chat'

// Sidebar persistente (identidad §4): el estado colapsado vive en el store de
// UI y se conserva entre sesiones (pinia-plugin-persistedstate). En pantallas
// anchas colapsa por ancho (queda en el flujo); en pantallas estrechas se
// comporta como panel superpuesto que entra/sale por la izquierda. El historial
// de chats es propiedad del backend y llega via prop (la vista lo trae del
// store). Presentacional: lista las sesiones, resalta la activa y emite la
// seleccion hacia arriba; no toca el store de chat directamente.
const props = withDefaults(
  defineProps<{ sessions?: SessionSummary[]; activeSessionId?: string | null }>(),
  { sessions: () => [], activeSessionId: null },
)
const emit = defineEmits<{
  'new-chat': []
  'select-session': [string]
  'delete-session': [string]
}>()
const ui = useUiStore()

// confirmingId guarda el id de la sesion cuya fila esta en modo "confirmar
// borrado": el control de basura se reemplaza por confirmar/cancelar (borrado en
// dos pasos para que un click accidental no pierda una conversacion).
const confirmingId = ref<string | null>(null)

// Fallback de titulo: una sesion sin primer prompt aun no tiene Title; la
// sidebar muestra un placeholder en vez de una fila vacia (identidad §11: el
// copy debe ser util, no decorativo).
function titleOf(session: SessionSummary): string {
  return session.Title.trim() || 'New chat'
}

// confirmDelete emite el borrado de la sesion confirmada y cierra el modo
// confirmacion.
function confirmDelete(id: string): void {
  emit('delete-session', id)
  confirmingId.value = null
}
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
    <div class="flex w-64 flex-1 flex-col gap-1 overflow-hidden p-3">
      <p class="px-2 py-3 text-lg tracking-tight">atenea</p>

      <button
        type="button"
        class="flex items-center gap-2 rounded-full px-4 py-2.5 text-left text-sm transition hover:bg-black/[0.04]"
        @click="emit('new-chat')"
      >
        <PhPlus :size="18" weight="regular" />
        New chat
      </button>

      <nav
        v-if="props.sessions.length"
        aria-label="Recent chats"
        class="mt-4 flex min-h-0 flex-1 flex-col gap-0.5 overflow-y-auto"
      >
        <div
          v-for="session in props.sessions"
          :key="session.ID"
          class="group flex items-center gap-1 rounded-full pr-1 transition"
          :class="
            session.ID === props.activeSessionId ? 'bg-accent/10' : 'hover:bg-black/[0.04]'
          "
        >
          <button
            type="button"
            :data-session-id="session.ID"
            :aria-current="session.ID === props.activeSessionId ? 'true' : undefined"
            class="min-w-0 flex-1 truncate rounded-full px-4 py-2 text-left text-sm transition"
            :class="
              session.ID === props.activeSessionId
                ? 'text-accent'
                : 'opacity-70 group-hover:opacity-100'
            "
            @click="emit('select-session', session.ID)"
          >
            {{ titleOf(session) }}
          </button>

          <template v-if="confirmingId === session.ID">
            <button
              type="button"
              :data-confirm-delete="session.ID"
              aria-label="Confirm delete"
              class="flex shrink-0 items-center justify-center rounded-full p-1.5 text-sm transition hover:bg-black/[0.06]"
              @click="confirmDelete(session.ID)"
            >
              <PhCheck :size="16" />
            </button>
            <button
              type="button"
              :data-cancel-delete="session.ID"
              aria-label="Cancel delete"
              class="flex shrink-0 items-center justify-center rounded-full p-1.5 text-sm transition hover:bg-black/[0.06]"
              @click="confirmingId = null"
            >
              <PhX :size="16" />
            </button>
          </template>
          <button
            v-else
            type="button"
            :data-delete-session="session.ID"
            aria-label="Delete chat"
            class="flex shrink-0 items-center justify-center rounded-full p-1.5 text-sm opacity-0 transition hover:bg-black/[0.06] group-hover:opacity-100"
            @click="confirmingId = session.ID"
          >
            <PhTrash :size="16" />
          </button>
        </div>
      </nav>
    </div>
  </aside>
</template>

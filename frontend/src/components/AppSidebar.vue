<script lang="ts" setup>
import { ref, computed } from 'vue'
import {
  PhPlus,
  PhTrash,
  PhCheck,
  PhX,
  PhGear,
  PhFolderOpen,
} from '@phosphor-icons/vue'
import { useUiStore } from '../stores/ui'
import type { SessionSummary } from '../stores/chat'
import { groupSessionsByFolder } from '../lib/sessions'
import { basename } from '../lib/path'

// Sidebar persistente (identidad §4): el estado colapsado vive en el store de
// UI y se conserva entre sesiones (pinia-plugin-persistedstate). En pantallas
// anchas colapsa por ancho (queda en el flujo); en pantallas estrechas se
// comporta como panel superpuesto que entra/sale por la izquierda. El historial
// de chats es propiedad del backend y llega via prop (la vista lo trae del
// store). Presentacional: lista las sesiones, resalta la activa y emite la
// seleccion hacia arriba; no toca el store de chat directamente.
// TODO(motion): en >=md el colapso anima `width` (w-64 -> md:w-0), que dispara
// layout/reflow en cada frame. Lo ideal (Emil) seria translate + ancho fijo,
// pero ese refactor es arriesgado para el layout y no se puede testear headless;
// se conserva el mecanismo actual y solo se ajusta la curva (ease-drawer).
const props = withDefaults(
  defineProps<{
    sessions?: SessionSummary[]
    activeSessionId?: string | null
    workspace?: string
  }>(),
  { sessions: () => [], activeSessionId: null, workspace: '' },
)
const emit = defineEmits<{
  'new-chat': []
  'select-session': [string]
  'delete-session': [string]
  'open-settings': []
  'change-workspace': []
}>()
const ui = useUiStore()

// Los chats se agrupan por carpeta de proyecto (identidad: ordenados por carpeta);
// dentro de cada grupo conservan la recencia que da el backend. workspaceLabel es
// el nombre corto de la carpeta vigente para el control de cambio de carpeta.
const groups = computed(() => groupSessionsByFolder(props.sessions))
const workspaceLabel = computed(
  () => basename(props.workspace) || 'Sin carpeta',
)

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
    class="fixed inset-y-0 left-0 z-30 flex h-full w-64 flex-col overflow-hidden border-black/5 bg-paper transition-[transform,width,border-color] duration-300 ease-drawer md:static md:bg-black/[0.015]"
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
        class="flex items-center gap-2 rounded-full px-4 py-2.5 text-left text-sm transition hover:bg-black/[0.04] active:scale-[0.97]"
        @click="emit('new-chat')"
      >
        <PhPlus :size="18" weight="regular" />
        New chat
      </button>

      <!-- Carpeta de trabajo vigente: el agente apunta aqui. Pulsar la cambia
           (dialogo nativo en el backend). -->
      <button
        type="button"
        data-change-workspace
        :title="props.workspace || 'Choose working folder'"
        aria-label="Change working folder"
        class="flex items-center gap-2 rounded-full px-4 py-2 text-left text-xs opacity-70 transition hover:bg-black/[0.04] hover:opacity-100 active:scale-[0.97]"
        @click="emit('change-workspace')"
      >
        <PhFolderOpen :size="16" weight="regular" class="shrink-0" />
        <span class="min-w-0 flex-1 truncate">{{ workspaceLabel }}</span>
      </button>

      <nav
        v-if="props.sessions.length"
        aria-label="Recent chats"
        class="mt-4 flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto"
      >
        <div
          v-for="group in groups"
          :key="group.cwd"
          class="flex flex-col gap-0.5"
        >
          <p
            data-folder-group
            :title="group.cwd || 'Sin carpeta'"
            class="truncate px-4 pb-0.5 pt-1 text-[11px] uppercase tracking-wide opacity-40"
          >
            {{ group.label }}
          </p>

          <div
            v-for="session in group.sessions"
            :key="session.ID"
            class="group flex items-center gap-1 rounded-full pr-1 transition"
            :class="
              session.ID === props.activeSessionId
                ? 'bg-accent/10'
                : 'hover:bg-black/[0.04]'
            "
          >
            <button
              type="button"
              :data-session-id="session.ID"
              :aria-current="
                session.ID === props.activeSessionId ? 'true' : undefined
              "
              class="min-w-0 flex-1 overflow-hidden whitespace-nowrap text-clip rounded-full px-4 py-2 text-left text-sm transition group-hover:text-ellipsis"
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
                class="flex shrink-0 items-center justify-center rounded-full p-1.5 text-sm transition hover:bg-black/[0.06] active:scale-95"
                @click="confirmDelete(session.ID)"
              >
                <PhCheck :size="16" />
              </button>
              <button
                type="button"
                :data-cancel-delete="session.ID"
                aria-label="Cancel delete"
                class="flex shrink-0 items-center justify-center rounded-full p-1.5 text-sm transition hover:bg-black/[0.06] active:scale-95"
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
              class="flex shrink-0 items-center justify-center rounded-full p-1.5 text-sm opacity-0 transition hover:bg-black/[0.06] active:scale-95 group-hover:opacity-100"
              @click="confirmingId = session.ID"
            >
              <PhTrash :size="16" />
            </button>
          </div>
        </div>
      </nav>

      <button
        type="button"
        aria-label="Open settings"
        class="mt-auto flex items-center gap-2 rounded-full px-4 py-2.5 text-left text-sm opacity-70 transition hover:bg-black/[0.04] hover:opacity-100 active:scale-[0.97]"
        @click="emit('open-settings')"
      >
        <PhGear :size="18" weight="regular" />
        Settings
      </button>
    </div>
  </aside>
</template>

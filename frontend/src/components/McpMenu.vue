<script lang="ts" setup>
import { onMounted, onUnmounted, ref } from 'vue'
import { PhPlugs, PhX } from '@phosphor-icons/vue'
import { useMcpStore } from '../stores/mcp'
import { PrettyModal } from '../lib/modal'

// Menu de servidores MCP en la navbar: dialog anclado al boton (Flip desde
// el trigger), con un switch por servidor para conectar/desconectar. Es la
// entrada rapida a on/off; la config (agregar/quitar/editar) sigue en Settings.
// La fuente de verdad del estado de conexion vive en el store; aca solo
// disparamos toggle y refrescamos al abrir.
const DIALOG_ID = 'mcp-menu'

const mcp = useMcpStore()
const modal = new PrettyModal()
const isOpen = ref(false)

const emit = defineEmits<{ close: [] }>()

onMounted(() => {
  void mcp.refresh()
})

onUnmounted(() => {
  if (isOpen.value) modal.close(DIALOG_ID)
})

function open(e: Event) {
  modal.open(DIALOG_ID, e)
  isOpen.value = true
}

function requestClose() {
  if (!isOpen.value) return
  modal.close(DIALOG_ID)
  isOpen.value = false
  emit('close')
}

function onDialogClose() {
  isOpen.value = false
}

function onDialogClick(e: MouseEvent) {
  if (e.target === e.currentTarget) requestClose()
}

function toggle(name: string) {
  void mcp.toggle(name)
}

defineExpose({ open, close: requestClose, isOpen })
</script>

<template>
  <dialog
    :id="DIALOG_ID"
    aria-label="MCP servers"
    class="fixed inset-auto z-40 m-0 w-72 rounded-soft border border-black/5 bg-paper p-2 shadow-lg open:block"
    @click="onDialogClick"
    @cancel.prevent="requestClose"
    @close="onDialogClose"
  >
    <div class="flex items-center justify-between px-1 pb-1.5 pt-1">
      <h2 class="text-sm font-medium tracking-tight">MCP servers</h2>
      <button
        type="button"
        aria-label="Cerrar menu"
        class="flex h-6 w-6 items-center justify-center rounded-full transition hover:bg-black/[0.05] active:scale-90"
        @click="requestClose"
      >
        <PhX :size="14" weight="bold" />
      </button>
    </div>

    <p
      v-if="mcp.servers.length === 0"
      class="flex flex-col items-center gap-2 px-2 py-6 text-center text-sm opacity-50"
    >
      <PhPlugs :size="22" weight="regular" class="opacity-40" />
      No MCP servers configured.
    </p>

    <ul v-else class="flex flex-col gap-1">
      <li
        v-for="server in mcp.servers"
        :key="server.name"
        class="flex items-center gap-3 rounded-[0.5rem] px-1.5 py-1.5 transition hover:bg-black/[0.04]"
      >
        <div class="min-w-0 flex-1">
          <p class="truncate text-sm font-medium">{{ server.name }}</p>
          <p
            class="truncate font-mono text-[11px] opacity-50"
            :title="[server.command, ...server.args].join(' ')"
          >
            {{ [server.command, ...server.args].join(' ') }}
          </p>
        </div>

        <!-- Switch de conexion: arranca/detiene el servidor. role=switch con
               aria-checked reflejando el estado conectado real. Deshabilitado
               mientras la accion esta en vuelo. -->
        <button
          type="button"
          role="switch"
          :data-mcp-switch="server.name"
          :aria-checked="server.connected ? 'true' : 'false'"
          :disabled="mcp.isPending(server.name)"
          class="relative h-5 w-9 shrink-0 rounded-full transition-colors duration-150 ease-snappy disabled:opacity-50 active:scale-95"
          :class="server.connected ? 'bg-accent' : 'bg-black/15'"
          @click="toggle(server.name)"
        >
          <span
            class="absolute top-0.5 h-4 w-4 rounded-full bg-paper shadow-sm transition-[left] duration-150 ease-snappy"
            :class="server.connected ? 'left-[1.125rem]' : 'left-0.5'"
          ></span>
        </button>
      </li>
    </ul>

    <p v-if="mcp.error" role="alert" class="mt-2 px-1.5 text-xs text-red-700">
      {{ mcp.error }}
    </p>
  </dialog>
</template>

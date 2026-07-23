<script lang="ts" setup>
import { onMounted, onBeforeUnmount, ref } from 'vue'
import { attach, detach, resize } from './terminalSession'

// Tab Terminal: presta el xterm de SU sesion (sessionId) del registro
// terminalSession, que vive fuera del arbol de componentes. Al desmontar
// (cambiar de tab o cerrar el panel) solo lo devuelve: el shell sigue vivo y el
// scrollback se conserva. Cerrar la tab (destroy) lo mata; eso lo hace el panel.
// ponytail: GUI pura, no corre headless; la logica testeable vive en el modulo.
const props = defineProps<{ sessionId: string }>()
const host = ref<HTMLDivElement | null>(null)
let ro: ResizeObserver | null = null

onMounted(async () => {
  if (!host.value) return
  await attach(props.sessionId, host.value)
  ro = new ResizeObserver(() => resize(props.sessionId))
  ro.observe(host.value)
})

onBeforeUnmount(() => {
  ro?.disconnect()
  detach(props.sessionId) // NO cierra el pty: la terminal persiste
})
</script>

<template>
  <div ref="host" data-terminal class="h-full w-full overflow-hidden p-1"></div>
</template>

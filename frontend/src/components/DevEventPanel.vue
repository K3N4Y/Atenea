<script lang="ts" setup>
import { ref } from 'vue'
import { useChatStore, type SessionEvent } from '../stores/chat'

// ponytail: herramienta solo-dev. Dispara SessionEvents canned por el mismo
// applyEvent que usa EventsOn, asi se construye/depura la UI (todos, tools,
// streaming, permisos, uso) sin un agente vivo. ChatView la monta bajo
// import.meta.env.DEV, de modo que no entra al bundle de produccion.
const chat = useChatStore()
const open = ref(false)

function fire(...evs: SessionEvent[]): void {
  for (const ev of evs) chat.applyEvent(ev)
}

// id de call unico por preset, para correlacionar Called con Success/Failed.
let n = 0
const cid = () => `dev-${(n += 1)}`

const presets: { key: string; label: string; run: () => void }[] = [
  {
    key: 'todos',
    label: 'Todos',
    run: () =>
      fire({
        Kind: 'Tool.Called',
        ToolName: 'todo_write',
        CallID: cid(),
        Input: {
          todos: [
            { content: 'Leer el spec', status: 'completed' },
            { content: 'Escribir el test', status: 'in_progress' },
            { content: 'Implementar la tool', status: 'pending' },
          ],
        },
      }),
  },
  {
    key: 'plan',
    label: 'Plan',
    run: () =>
      fire({
        Kind: 'Tool.Called',
        ToolName: 'present_plan',
        CallID: cid(),
        Input: {
          title: 'Agregar autenticacion',
          plan: '## Plan\n\n1. Crear el endpoint `/login`\n2. Hashear la password con bcrypt\n3. Emitir el token de sesion\n4. Cubrir con tests el happy path y el fallo',
        },
      }),
  },
  {
    key: 'text',
    label: 'Texto',
    run: () =>
      fire(
        { Kind: 'Text.Started' },
        { Kind: 'Text.Delta', Text: 'Claro, ' },
        { Kind: 'Text.Delta', Text: 'esto es una respuesta ' },
        { Kind: 'Text.Delta', Text: 'del asistente en *streaming*.' },
        { Kind: 'Text.Ended' },
      ),
  },
  {
    key: 'reasoning',
    label: 'Razonamiento',
    run: () =>
      fire(
        { Kind: 'Reasoning.Started' },
        { Kind: 'Reasoning.Delta', Text: 'Pensando en el problema... ' },
        { Kind: 'Reasoning.Delta', Text: 'evaluando opciones.' },
        { Kind: 'Reasoning.Ended' },
      ),
  },
  {
    key: 'tool-ok',
    label: 'Tool ok',
    run: () => {
      const id = cid()
      fire(
        { Kind: 'Tool.Called', ToolName: 'bash', CallID: id, Input: { command: 'ls -la' } },
        { Kind: 'Tool.Success', CallID: id, Text: 'total 8\ndrwxr-xr-x  2 user user\n-rw-r--r--  1 user user main.go' },
      )
    },
  },
  {
    key: 'tool-perm',
    label: 'Tool permiso',
    run: () => {
      const id = cid()
      fire(
        { Kind: 'Tool.Called', ToolName: 'bash', CallID: id, Input: { command: 'rm -rf build/' } },
        { Kind: 'Tool.Permission.Requested', CallID: id },
      )
    },
  },
  {
    key: 'tool-diff',
    label: 'Tool diff',
    run: () => {
      const id = cid()
      fire(
        { Kind: 'Tool.Called', ToolName: 'edit', CallID: id, Input: { path: 'main.go' } },
        {
          Kind: 'Tool.Success',
          CallID: id,
          Text: 'edited main.go',
          Diff: '--- a/main.go\n+++ b/main.go\n@@ -1,3 +1,3 @@\n func main() {\n-\tprintln("hola")\n+\tprintln("hola mundo")\n }',
        },
      )
    },
  },
  {
    key: 'tool-fail',
    label: 'Tool falla',
    run: () => {
      const id = cid()
      fire(
        { Kind: 'Tool.Called', ToolName: 'bash', CallID: id, Input: { command: 'cat nope.txt' } },
        { Kind: 'Tool.Failed', CallID: id, Error: 'cat: nope.txt: No such file or directory' },
      )
    },
  },
  {
    key: 'usage',
    label: 'Uso (barra)',
    run: () =>
      fire({
        Kind: 'Step.Ended',
        Usage: {
          InputTokens: 42000,
          OutputTokens: 1800,
          ReasoningTokens: 600,
          CacheReadTokens: 30000,
          CacheWriteTokens: 5000,
        },
      }),
  },
  {
    key: 'error',
    label: 'Error',
    run: () => chat.applyError('proveedor: stream cortado (dev)'),
  },
  {
    key: 'user',
    label: 'Msg usuario',
    run: () => fire({ Message: { Role: 'user', Text: 'Hola, prueba de dev' } }),
  },
  { key: 'reset', label: 'Reset', run: () => chat.reset() },
]
</script>

<template>
  <div class="fixed bottom-3 left-3 z-50 font-mono text-xs">
    <button
      type="button"
      data-dev="open"
      class="rounded bg-fuchsia-600 px-2 py-1 text-white shadow"
      @click="open = !open"
    >
      {{ open ? 'x' : 'dev events' }}
    </button>
    <div
      v-if="open"
      class="mt-1 flex w-40 flex-col gap-1 rounded bg-white/95 p-2 shadow-lg ring-1 ring-black/10"
    >
      <button
        v-for="p in presets"
        :key="p.key"
        type="button"
        :data-dev="p.key"
        class="rounded px-2 py-1 text-left hover:bg-fuchsia-50"
        @click="p.run()"
      >
        {{ p.label }}
      </button>
    </div>
  </div>
</template>

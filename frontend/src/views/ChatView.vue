<script lang="ts" setup>
import { ref, onMounted } from 'vue'
import { SendPrompt, Stop } from '../../wailsjs/go/main/App'
import { EventsOn } from '../../wailsjs/runtime/runtime'

// M9 cablea una sola sesion. Steering y multiples sesiones llegan despues; el
// canal session:<id> ya lo soporta.
const sessionID = 'main'

type Line = { kind: string; text: string }

const prompt = ref('')
const lines = ref<Line[]>([])
const running = ref(false)
let assistant: Line | null = null

function push(kind: string, text: string) {
  lines.value.push({ kind, text })
}

onMounted(() => {
  // Cada evento durable del log llega por el canal de la sesion, en orden de Seq.
  EventsOn(`session:${sessionID}`, (ev: any) => {
    switch (ev.Kind) {
      case 'Text.Started':
        assistant = { kind: 'assistant', text: '' }
        lines.value.push(assistant)
        break
      case 'Text.Delta':
        if (!assistant) {
          assistant = { kind: 'assistant', text: '' }
          lines.value.push(assistant)
        }
        assistant.text += ev.Text
        break
      case 'Text.Ended':
        assistant = null
        break
      case 'Tool.Called':
        push('tool', `→ ${ev.ToolName}(${ev.CallID})`)
        break
      case 'Tool.Success':
        push('tool', `✓ ${ev.ToolName}: ${ev.Text}`)
        break
      case 'Tool.Failed':
        push('tool-error', `✗ ${ev.ToolName || ev.CallID}: ${ev.Error}`)
        break
      case 'Step.Failed':
        push('error', `interrumpido: ${ev.Error}`)
        running.value = false
        break
      case 'Step.Ended':
        running.value = false
        break
    }
    // El prompt del usuario se promueve como Message{Role:user} (Kind vacio).
    if (ev.Message && ev.Message.Role === 'user') {
      push('user', ev.Message.Text)
    }
  })

  // Cierre por error duro de Run (fallo de proveedor, limite de pasos, stop).
  EventsOn(`session:${sessionID}:error`, (msg: string) => {
    push('error', msg)
    running.value = false
  })
})

async function send() {
  const text = prompt.value.trim()
  if (!text) return
  prompt.value = ''
  running.value = true
  await SendPrompt(sessionID, text)
}

function stop() {
  Stop(sessionID)
}
</script>

<template>
  <main class="chat">
    <ul class="log">
      <li v-for="(l, i) in lines" :key="i" :class="l.kind">{{ l.text }}</li>
    </ul>
    <form class="bar" @submit.prevent="send">
      <input v-model="prompt" placeholder="Escribe un prompt..." autofocus />
      <button type="submit">Enviar</button>
      <button type="button" :disabled="!running" @click="stop">Stop</button>
    </form>
  </main>
</template>

<style>
.chat { display: flex; flex-direction: column; height: 100vh; }
.log { flex: 1; overflow-y: auto; list-style: none; margin: 0; padding: 1rem; text-align: left; }
.log .user { color: #9cdcfe; }
.log .assistant { white-space: pre-wrap; }
.log .tool { color: #c586c0; }
.log .tool-error, .log .error { color: #f48771; }
.bar { display: flex; gap: .5rem; padding: .75rem; }
.bar input { flex: 1; }
</style>

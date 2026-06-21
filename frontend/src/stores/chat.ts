import { ref } from 'vue'
import { defineStore } from 'pinia'
import { SendPrompt, Stop } from '../../wailsjs/go/main/App'
import { EventsOn } from '../../wailsjs/runtime/runtime'

// Mapeo evento->estado de la sesion (front.md §74). El store formaliza la
// traduccion de los eventos durables del canal `session:<id>` a mensajes y
// estado de UI, manteniendo la frontera Wails (bindings + runtime) en un solo
// lugar. Hoy hay una unica sesion ('main'); el multi-sesion llega despues.
const SESSION_ID = 'main'

export type ChatRole = 'user' | 'assistant'

export interface ChatMessage {
  id: string
  role: ChatRole
  text: string
  streaming: boolean
}

// Forma del evento durable serializado por Wails (campos PascalCase, sin json
// tags en Go). Solo declaramos lo que el MVP consume; el resto se ignora.
export interface SessionEvent {
  Kind?: string
  Text?: string
  Error?: string
  Message?: { Role?: string; Text?: string }
}

export const useChatStore = defineStore('chat', () => {
  const messages = ref<ChatMessage[]>([])
  const running = ref(false)
  const errorText = ref<string | null>(null)

  // Mensaje de IA en curso (referencia dentro de `messages`), o null si no hay
  // streaming activo.
  let streaming: ChatMessage | null = null
  let seq = 0
  const unsubscribe: Array<() => void> = []

  function nextId(): string {
    seq += 1
    return `m${seq}`
  }

  function startAssistant(): ChatMessage {
    const msg: ChatMessage = { id: nextId(), role: 'assistant', text: '', streaming: true }
    messages.value.push(msg)
    streaming = msg
    return msg
  }

  function applyEvent(ev: SessionEvent): void {
    switch (ev.Kind) {
      case 'Text.Started':
        startAssistant()
        break
      case 'Text.Delta':
        ;(streaming ?? startAssistant()).text += ev.Text ?? ''
        break
      case 'Text.Ended': {
        const msg = streaming ?? startAssistant()
        if (ev.Text) msg.text = ev.Text
        msg.streaming = false
        streaming = null
        break
      }
      case 'Step.Ended':
        running.value = false
        break
      case 'Step.Failed':
        running.value = false
        if (ev.Error) errorText.value = ev.Error
        break
    }

    // El prompt del usuario se promueve como Message{Role:user} (Kind vacio).
    if (ev.Message && ev.Message.Role === 'user') {
      messages.value.push({
        id: nextId(),
        role: 'user',
        text: ev.Message.Text ?? '',
        streaming: false,
      })
    }
  }

  function applyError(msg: string): void {
    running.value = false
    errorText.value = msg
  }

  // Lienzo nuevo: limpia la vista local. La fuente de verdad sigue siendo el
  // backend; la rehidratacion del historial llega en la Fase 4.
  function reset(): void {
    messages.value = []
    streaming = null
    errorText.value = null
  }

  async function send(text: string): Promise<void> {
    const trimmed = text.trim()
    if (!trimmed) return
    errorText.value = null
    running.value = true
    await SendPrompt(SESSION_ID, trimmed)
  }

  function stop(): void {
    Stop(SESSION_ID)
  }

  function subscribe(): void {
    unsubscribe.push(
      EventsOn(`session:${SESSION_ID}`, (ev: SessionEvent) => applyEvent(ev)),
      EventsOn(`session:${SESSION_ID}:error`, (msg: string) => applyError(msg)),
    )
  }

  function teardown(): void {
    while (unsubscribe.length) unsubscribe.pop()?.()
  }

  return {
    messages,
    running,
    errorText,
    applyEvent,
    applyError,
    reset,
    send,
    stop,
    subscribe,
    teardown,
  }
})

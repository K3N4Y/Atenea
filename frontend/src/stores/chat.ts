import { ref } from 'vue'
import { defineStore } from 'pinia'
import { SendPrompt, Stop } from '../../wailsjs/go/main/App'
import { EventsOn } from '../../wailsjs/runtime/runtime'

// Mapeo evento->estado de la sesion (front.md §74). El store formaliza la
// traduccion de los eventos durables del canal `session:<id>` a items del log
// y estado de UI, manteniendo la frontera Wails (bindings + runtime) en un solo
// lugar. Hoy hay una unica sesion ('main'); el multi-sesion llega despues.
const SESSION_ID = 'main'

export type ToolStatus = 'running' | 'success' | 'failed'

export interface UserItem {
  kind: 'user'
  id: string
  text: string
}

export interface AssistantItem {
  kind: 'assistant'
  id: string
  text: string
  streaming: boolean
}

export interface ReasoningItem {
  kind: 'reasoning'
  id: string
  text: string
  streaming: boolean
  durationMs: number | null
}

export interface ToolItem {
  kind: 'tool'
  id: string
  callID: string
  name: string
  input: unknown
  status: ToolStatus
  output: string
  error: string | null
}

// El log es una secuencia plana y ordenada de items de distinto tipo, que se
// renderizan como un lienzo continuo (identidad §8).
export type TurnItem = UserItem | AssistantItem | ReasoningItem | ToolItem

// Forma del evento durable serializado por Wails (campos PascalCase, sin json
// tags en Go). Solo declaramos lo que el frontend consume.
export interface SessionEvent {
  Kind?: string
  Text?: string
  Error?: string
  CallID?: string
  ToolName?: string
  Input?: unknown
  Message?: { Role?: string; Text?: string }
}

export const useChatStore = defineStore('chat', () => {
  const items = ref<TurnItem[]>([])
  const running = ref(false)
  const errorText = ref<string | null>(null)

  // Punteros al texto / pensamiento en curso (referencias dentro de `items`).
  let streamingText: AssistantItem | null = null
  let streamingReasoning: ReasoningItem | null = null
  let reasoningStartedAt = 0
  // Correlacion CallID -> item de tool para resolver Tool.Success/Failed.
  let toolsByCall = new Map<string, ToolItem>()
  let seq = 0
  const unsubscribe: Array<() => void> = []

  function nextId(): string {
    seq += 1
    return `i${seq}`
  }

  function startAssistant(): AssistantItem {
    const item: AssistantItem = { kind: 'assistant', id: nextId(), text: '', streaming: true }
    items.value.push(item)
    streamingText = item
    return item
  }

  function startReasoning(): ReasoningItem {
    const item: ReasoningItem = {
      kind: 'reasoning',
      id: nextId(),
      text: '',
      streaming: true,
      durationMs: null,
    }
    items.value.push(item)
    streamingReasoning = item
    reasoningStartedAt = Date.now()
    return item
  }

  function applyEvent(ev: SessionEvent): void {
    switch (ev.Kind) {
      case 'Text.Started':
        startAssistant()
        break
      case 'Text.Delta':
        ;(streamingText ?? startAssistant()).text += ev.Text ?? ''
        break
      case 'Text.Ended': {
        const item = streamingText ?? startAssistant()
        if (ev.Text) item.text = ev.Text
        item.streaming = false
        streamingText = null
        break
      }
      case 'Reasoning.Started':
        startReasoning()
        break
      case 'Reasoning.Delta':
        ;(streamingReasoning ?? startReasoning()).text += ev.Text ?? ''
        break
      case 'Reasoning.Ended': {
        const item = streamingReasoning ?? startReasoning()
        if (ev.Text) item.text = ev.Text
        item.streaming = false
        item.durationMs = Date.now() - reasoningStartedAt
        streamingReasoning = null
        break
      }
      case 'Tool.Called': {
        const item: ToolItem = {
          kind: 'tool',
          id: nextId(),
          callID: ev.CallID ?? '',
          name: ev.ToolName ?? '',
          input: ev.Input,
          status: 'running',
          output: '',
          error: null,
        }
        items.value.push(item)
        if (item.callID) toolsByCall.set(item.callID, item)
        break
      }
      case 'Tool.Success': {
        const item = ev.CallID ? toolsByCall.get(ev.CallID) : undefined
        if (item) {
          item.status = 'success'
          item.output = ev.Text ?? ''
        }
        break
      }
      case 'Tool.Failed': {
        const item = ev.CallID ? toolsByCall.get(ev.CallID) : undefined
        if (item) {
          item.status = 'failed'
          item.error = ev.Error ?? ''
        }
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
      items.value.push({ kind: 'user', id: nextId(), text: ev.Message.Text ?? '' })
    }
  }

  function applyError(msg: string): void {
    running.value = false
    errorText.value = msg
  }

  // Lienzo nuevo: limpia la vista local. La fuente de verdad sigue siendo el
  // backend; la rehidratacion del historial llega en la Fase 4.
  function reset(): void {
    items.value = []
    streamingText = null
    streamingReasoning = null
    toolsByCall = new Map()
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
    items,
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

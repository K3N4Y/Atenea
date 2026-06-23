import { ref } from 'vue'
import { defineStore, acceptHMRUpdate } from 'pinia'
import { SendPrompt, Stop, ResolveToolPermission } from '../../wailsjs/go/main/App'
import { EventsOn } from '../../wailsjs/runtime/runtime'

// Mapeo evento->estado de la sesion (front.md §74). El store formaliza la
// traduccion de los eventos durables del canal `session:<id>` a items del log
// y estado de UI, manteniendo la frontera Wails (bindings + runtime) en un solo
// lugar.
function newSessionID(): string {
  const id =
    globalThis.crypto?.randomUUID?.() ??
    `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
  return `chat-${id}`
}

// 'pending' = awaiting user approval (ask-before-run): the UI offers
// Approve/Deny before the tool runs.
export type ToolStatus = 'pending' | 'running' | 'success' | 'failed'

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
  const sessionID = ref(newSessionID())
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

  // pushItem agrega el item al log y devuelve el proxy reactivo que vive dentro
  // de `items` (no la referencia cruda recien pusheada). Mutar ESE proxy durante
  // el streaming agenda los re-renders; mutar la referencia cruda no, porque la
  // reactividad anidada de Vue rastrea los objetos a traves del proxy del array.
  function pushItem<T extends TurnItem>(item: T): T {
    items.value.push(item)
    return items.value[items.value.length - 1] as T
  }

  function startAssistant(): AssistantItem {
    const item: AssistantItem = { kind: 'assistant', id: nextId(), text: '', streaming: true }
    return (streamingText = pushItem(item))
  }

  function startReasoning(): ReasoningItem {
    const item: ReasoningItem = {
      kind: 'reasoning',
      id: nextId(),
      text: '',
      streaming: true,
      durationMs: null,
    }
    reasoningStartedAt = Date.now()
    return (streamingReasoning = pushItem(item))
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
        const stored = pushItem(item)
        if (stored.callID) toolsByCall.set(stored.callID, stored)
        break
      }
      case 'Tool.Permission.Requested': {
        // Tool.Called already created the item; here it moves to 'pending' so the
        // UI can offer Approve/Deny before execution (ask-before-run).
        const item = ev.CallID ? toolsByCall.get(ev.CallID) : undefined
        if (item) item.status = 'pending'
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

  // clearError descarta el aviso de error visible (el usuario lo cierra). No
  // toca el log: la conversacion sigue ahi, solo desaparece el aviso.
  function clearError(): void {
    errorText.value = null
  }

  // Lienzo nuevo: limpia la vista local. La fuente de verdad sigue siendo el
  // backend; la rehidratacion del historial llega en la Fase 4.
  function reset(): void {
    const wasSubscribed = unsubscribe.length > 0
    teardown()
    sessionID.value = newSessionID()
    items.value = []
    streamingText = null
    streamingReasoning = null
    toolsByCall = new Map()
    running.value = false
    errorText.value = null
    if (wasSubscribed) subscribe()
  }

  async function send(text: string): Promise<void> {
    const trimmed = text.trim()
    if (!trimmed) return
    errorText.value = null
    running.value = true
    await SendPrompt(sessionID.value, trimmed)
  }

  function stop(): void {
    Stop(sessionID.value)
  }

  // approveTool / denyTool deliver the decision on a gated tool call
  // (ask-before-run) to the backend. They take the item out of 'pending'
  // immediately (removes the buttons and prevents double clicks): approve
  // moves it to 'running' awaiting Tool.Success/Failed; deny leaves it in
  // 'failed' (the backend's Tool.Failed confirms with its cause).
  function resolveTool(callID: string, approved: boolean): void {
    ResolveToolPermission(sessionID.value, callID, approved)
    const item = toolsByCall.get(callID)
    if (item && item.status === 'pending') {
      item.status = approved ? 'running' : 'failed'
    }
  }

  function approveTool(callID: string): void {
    resolveTool(callID, true)
  }

  function denyTool(callID: string): void {
    resolveTool(callID, false)
  }

  function subscribe(): void {
    teardown()
    unsubscribe.push(
      EventsOn(`session:${sessionID.value}`, (ev: SessionEvent) => applyEvent(ev)),
      EventsOn(`session:${sessionID.value}:error`, (msg: string) => applyError(msg)),
    )
  }

  function teardown(): void {
    while (unsubscribe.length) unsubscribe.pop()?.()
  }

  return {
    sessionID,
    items,
    running,
    errorText,
    applyEvent,
    applyError,
    clearError,
    reset,
    send,
    stop,
    approveTool,
    denyTool,
    subscribe,
    teardown,
  }
})

// HMR: al editar este store, Vite recarga su definicion en caliente en vez de
// dejar viva la instancia vieja (que mantenia las referencias crudas y no
// reaccionaba al streaming). Sin esto un fix al store no se ve hasta reiniciar.
if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useChatStore, import.meta.hot))
}

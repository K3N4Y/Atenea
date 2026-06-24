import { ref } from 'vue'
import { defineStore, acceptHMRUpdate } from 'pinia'
import {
  SendPrompt,
  SendPlanPrompt,
  AcceptPlan,
  Stop,
  ResolveToolPermission,
  ListSessions,
  SessionHistory,
  DeleteSession,
} from '../../wailsjs/go/main/App'
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

// Estado del plan vigente en modo plan. El agente lo presenta via la tool
// `present_plan` (Tool.Called con Input {plan, title?}); la UI lo muestra a
// pantalla completa (no como tool card inline) para aceptarlo o pedir cambios.
export interface PlanState {
  callID: string
  title: string
  markdown: string
}

// planFromInput normaliza el Input de la tool present_plan a PlanState.
// json.RawMessage llega como objeto JS, pero toleramos un string JSON por si
// el backend lo serializa distinto; un input invalido degrada a campos vacios.
function planFromInput(callID: string, input: unknown): PlanState {
  let obj = input
  if (typeof obj === 'string') {
    try {
      obj = JSON.parse(obj)
    } catch {
      obj = {}
    }
  }
  const o = obj && typeof obj === 'object' ? (obj as Record<string, unknown>) : {}
  return {
    callID,
    title: typeof o.title === 'string' ? o.title : '',
    markdown: typeof o.plan === 'string' ? o.plan : '',
  }
}

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

// Resumen de una sesion para el historial de la sidebar (espejo de
// session.SessionSummary del backend). El Title puede venir vacio (sesion sin
// prompt aun); la UI cae a un placeholder.
export interface SessionSummary {
  ID: string
  Title: string
}

export const useChatStore = defineStore('chat', () => {
  const sessionID = ref(newSessionID())
  const items = ref<TurnItem[]>([])
  const running = ref(false)
  const errorText = ref<string | null>(null)
  // Historial de chats para la sidebar. La fuente de verdad es el backend; se
  // refresca con loadSessions (al montar la vista) y tras enviar un prompt.
  const sessions = ref<SessionSummary[]>([])
  // Modo de envio: 'normal' manda prompts directos; 'plan' pide al agente que
  // planifique antes de ejecutar. `plan` guarda el plan vigente que la tool
  // present_plan abre a pantalla completa (null = sin overlay de plan).
  const mode = ref<'normal' | 'plan'>('normal')
  const plan = ref<PlanState | null>(null)
  // planExpanded controla como se ve el plan vigente: expandido (overlay sobre la
  // columna del chat) o minimizado (tarjeta en el flujo de la conversacion, como
  // una tool). Cada present_plan reabre expandido; el usuario lo colapsa/expande.
  const planExpanded = ref(true)

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
        // El plan no es una tool card inline: se muestra a pantalla completa.
        // present_plan abre/actualiza `plan` y no agrega item al log.
        if (ev.ToolName === 'present_plan') {
          plan.value = planFromInput(ev.CallID ?? '', ev.Input)
          // Un plan recien presentado (o reescrito) se abre expandido.
          planExpanded.value = true
          break
        }
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
      // Un mensaje de usuario despues de present_plan significa que el plan ya fue
      // accionado (AcceptPlan promueve "implementa..."; solicitar cambio promueve el
      // feedback). Cerrar el plan aqui evita que la rehidratacion (loadSession) reabra
      // un plan ya ejecutado; el siguiente present_plan en el historial lo reabre.
      plan.value = null
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

  // clearLog vacia el lienzo local y los punteros de streaming/correlacion. No
  // toca la suscripcion ni el sessionID: lo comparten reset (lienzo nuevo) y
  // loadSession (antes de reproducir el historial elegido).
  function clearLog(): void {
    items.value = []
    streamingText = null
    streamingReasoning = null
    toolsByCall = new Map()
    running.value = false
    errorText.value = null
    // Un lienzo nuevo/cargado arranca en modo normal sin overlay de plan.
    // Reproducir un historial que termina en present_plan reabre `plan` via
    // applyEvent durante la rehidratacion.
    plan.value = null
    planExpanded.value = true
    mode.value = 'normal'
  }

  // Lienzo nuevo: abre una sesion vacia y limpia la vista local. La fuente de
  // verdad sigue siendo el backend; el historial se rehidrata via loadSession.
  function reset(): void {
    const wasSubscribed = unsubscribe.length > 0
    teardown()
    sessionID.value = newSessionID()
    clearLog()
    if (wasSubscribed) subscribe()
  }

  // loadSessions trae el historial del backend para poblar la sidebar. Idempotente:
  // la vista la llama al montar y el store tras cada send.
  async function loadSessions(): Promise<void> {
    sessions.value = await ListSessions()
  }

  // deleteSession borra una conversacion del historial: la quita del backend, y si
  // era la sesion activa abre un chat nuevo (reset). Luego refresca la sidebar.
  async function deleteSession(id: string): Promise<void> {
    await DeleteSession(id)
    if (id === sessionID.value) reset()
    await loadSessions()
  }

  // loadSession abre una sesion del historial: cambia el sessionID activo, mueve
  // la suscripcion al canal de esa sesion, limpia el lienzo y reproduce el log
  // durable via applyEvent (reusa todo el render de texto/pensamiento/tools). El
  // log persistido incluye los *.Ended/Step.Ended, asi que los items convergen a
  // su estado terminal (no quedan en streaming) y running queda apagado.
  async function loadSession(id: string): Promise<void> {
    const wasSubscribed = unsubscribe.length > 0
    teardown()
    sessionID.value = id
    clearLog()
    if (wasSubscribed) subscribe()
    const history = await SessionHistory(id)
    for (const ev of history) applyEvent(ev)
  }

  async function send(text: string): Promise<void> {
    const trimmed = text.trim()
    if (!trimmed) return
    errorText.value = null
    running.value = true
    // Un envio nuevo cierra cualquier plan vigente; el agente lo reabrira con
    // present_plan si vuelve a planificar.
    plan.value = null
    if (mode.value === 'plan') {
      await SendPlanPrompt(sessionID.value, trimmed)
    } else {
      await SendPrompt(sessionID.value, trimmed)
    }
    // Refresca el historial: una conversacion nueva (o reactivada) debe aparecer
    // y reordenarse en la sidebar.
    await loadSessions()
  }

  // toggleMode alterna entre envio normal y modo plan.
  function toggleMode(): void {
    mode.value = mode.value === 'plan' ? 'normal' : 'plan'
  }

  // togglePlanExpanded alterna el plan vigente entre expandido (overlay) y
  // minimizado (tarjeta en la conversacion).
  function togglePlanExpanded(): void {
    planExpanded.value = !planExpanded.value
  }

  // acceptPlan acepta el plan vigente y lo ejecuta: vuelve a modo normal, cierra
  // el overlay y delega en el backend (que arranca la ejecucion del plan).
  async function acceptPlan(): Promise<void> {
    errorText.value = null
    running.value = true
    mode.value = 'normal'
    const id = sessionID.value
    plan.value = null
    await AcceptPlan(id)
    await loadSessions()
  }

  // requestPlanChange pide al agente reescribir el plan con el feedback del
  // usuario; sigue en modo plan a la espera del nuevo present_plan.
  async function requestPlanChange(feedback: string): Promise<void> {
    const trimmed = feedback.trim()
    if (!trimmed) return
    errorText.value = null
    running.value = true
    mode.value = 'plan'
    const id = sessionID.value
    plan.value = null
    await SendPlanPrompt(id, trimmed)
    await loadSessions()
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
    sessions,
    mode,
    plan,
    planExpanded,
    applyEvent,
    applyError,
    clearError,
    reset,
    loadSessions,
    loadSession,
    deleteSession,
    send,
    toggleMode,
    togglePlanExpanded,
    acceptPlan,
    requestPlanChange,
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

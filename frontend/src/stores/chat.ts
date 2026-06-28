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
  Model,
  ListProjectFiles,
  ListCommands,
  Workspace,
  SetWorkspace,
  SelectWorkspace,
} from '../../wailsjs/go/main/App'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import type { Command } from '../lib/command'

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
  // diff unificado solo-UI de edit/write (vacio en el resto); lo renderiza DiffView.
  diff: string
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
  const o =
    obj && typeof obj === 'object' ? (obj as Record<string, unknown>) : {}
  return {
    callID,
    title: typeof o.title === 'string' ? o.title : '',
    markdown: typeof o.plan === 'string' ? o.plan : '',
  }
}

// Item del checklist de tareas en vivo (tool todo_write). El agente reemplaza la
// lista entera en cada call; la UI la pinta arriba a la derecha (estilo Codex).
export type TodoStatus = 'pending' | 'in_progress' | 'completed'
export interface TodoItem {
  content: string
  status: TodoStatus
}

// todosFromInput normaliza el Input de todo_write a TodoItem[]. Como planFromInput,
// tolera un string JSON y degrada a lista vacia ante cualquier forma inesperada;
// descarta items sin content o con status fuera del enum (defensa de frontera).
function todosFromInput(input: unknown): TodoItem[] {
  let obj = input
  if (typeof obj === 'string') {
    try {
      obj = JSON.parse(obj)
    } catch {
      return []
    }
  }
  const todos = (obj as { todos?: unknown } | null)?.todos
  if (!Array.isArray(todos)) return []
  const valid: TodoStatus[] = ['pending', 'in_progress', 'completed']
  return todos.flatMap((t): TodoItem[] => {
    const o = t && typeof t === 'object' ? (t as Record<string, unknown>) : {}
    if (
      typeof o.content !== 'string' ||
      !valid.includes(o.status as TodoStatus)
    )
      return []
    return [{ content: o.content, status: o.status as TodoStatus }]
  })
}

// Uso de tokens de la sesion (ocupacion de contexto). camelCase para la UI; el
// backend lo emite en PascalCase dentro de Step.Ended. Solo tokens, sin costos.
export interface Usage {
  inputTokens: number
  outputTokens: number
  reasoningTokens: number
  cacheReadTokens: number
  cacheWriteTokens: number
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
  Diff?: string
  Message?: { Role?: string; Text?: string }
  Usage?: {
    InputTokens: number
    OutputTokens: number
    ReasoningTokens: number
    CacheReadTokens: number
    CacheWriteTokens: number
  }
}

// Resumen de una sesion para el historial de la sidebar (espejo de
// session.SessionSummary del backend). El Title puede venir vacio (sesion sin
// prompt aun); la UI cae a un placeholder.
export interface SessionSummary {
  ID: string
  Title: string
  // Carpeta de trabajo en que se creo la sesion; '' en chats viejos anteriores a
  // la captura. La sidebar agrupa por esta carpeta (groupSessionsByFolder).
  Cwd: string
}

export const useChatStore = defineStore(
  'chat',
  () => {
    const sessionID = ref(newSessionID())
    const items = ref<TurnItem[]>([])
    const running = ref(false)
    const errorText = ref<string | null>(null)
    // Historial de chats para la sidebar. La fuente de verdad es el backend; se
    // refresca con loadSessions (al montar la vista) y tras enviar un prompt.
    const sessions = ref<SessionSummary[]>([])
    // Carpeta de trabajo vigente (la raiz a la que apuntan las tools del agente). La
    // fuente de verdad es el backend (Workspace/SetWorkspace); la sidebar la muestra
    // y la usa para agrupar y para cambiar de carpeta al abrir un chat de otra.
    const workspace = ref('')
    // Modo de envio: 'normal' manda prompts directos; 'plan' pide al agente que
    // planifique antes de ejecutar. `plan` guarda el plan vigente que la tool
    // present_plan abre a pantalla completa (null = sin overlay de plan).
    const mode = ref<'normal' | 'plan'>('normal')
    const plan = ref<PlanState | null>(null)
    // Checklist de tareas en vivo: lo reemplaza cada todo_write. Persiste entre
    // turnos (a proposito: es para no perder el hilo en trabajos de varios pasos);
    // se vacia solo al cambiar de sesion (clearLog) y se reconstruye al rehidratar.
    const todos = ref<TodoItem[]>([])
    // Uso de tokens del ultimo Step.Ended (ocupacion de contexto actual) y modelo
    // activo. La UI los combina para pintar la barra de contexto por modelo.
    const usage = ref<Usage | null>(null)
    const model = ref('')
    // Rutas del workspace para el @-menu de archivos del composer. La fuente de
    // verdad es el backend (ListProjectFiles); se cargan una vez al montar la vista
    // y el composer filtra/ordena en cliente conforme el usuario escribe tras '@'.
    const projectFiles = ref<string[]>([])
    // Comandos del workspace para el slash-menu del composer. La fuente de verdad es
    // el backend (ListCommands); se cargan una vez al montar la vista y el composer
    // filtra/ordena en cliente conforme el usuario escribe tras '/'. Se normalizan a
    // {name, description} (el binding los entrega en PascalCase).
    const commands = ref<Command[]>([])
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
      const item: AssistantItem = {
        kind: 'assistant',
        id: nextId(),
        text: '',
        streaming: true,
      }
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
          // todo_write tampoco es una tool card inline: reemplaza el checklist
          // que la UI pinta arriba a la derecha.
          if (ev.ToolName === 'todo_write') {
            todos.value = todosFromInput(ev.Input)
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
            diff: '',
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
            item.diff = ev.Diff ?? ''
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
          // usage = ultimo Step.Ended (ocupacion de contexto actual): cada step
          // reporta el total acumulado, asi que el mas reciente gana.
          if (ev.Usage) {
            usage.value = {
              inputTokens: ev.Usage.InputTokens,
              outputTokens: ev.Usage.OutputTokens,
              reasoningTokens: ev.Usage.ReasoningTokens,
              cacheReadTokens: ev.Usage.CacheReadTokens,
              cacheWriteTokens: ev.Usage.CacheWriteTokens,
            }
          }
          break
        case 'Step.Failed':
          running.value = false
          if (ev.Error) errorText.value = ev.Error
          break
        case 'Session.Title':
          // El backend genero el titulo de la sesion (async tras el primer mensaje):
          // refresca la sidebar para que reemplace al primer prompt. Fire-and-forget.
          void loadSessions()
          break
      }

      // El prompt del usuario se promueve como Message{Role:user} (Kind vacio).
      if (ev.Message && ev.Message.Role === 'user') {
        items.value.push({
          kind: 'user',
          id: nextId(),
          text: ev.Message.Text ?? '',
        })
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
      todos.value = []
      mode.value = 'normal'
      // Un lienzo nuevo/cargado no arrastra el uso de tokens de la sesion previa.
      // model NO se resetea: es global del proceso, no por sesion.
      usage.value = null
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

    // loadWorkspace trae la carpeta de trabajo vigente del backend (espejo de
    // loadModel). La vista la llama al montar. Si el binding falla (arranque sin
    // backend) degrada a '' y la sidebar omite el encabezado de carpeta.
    async function loadWorkspace(): Promise<void> {
      try {
        workspace.value = await Workspace()
      } catch {
        workspace.value = ''
      }
    }

    // selectWorkspace abre el dialogo nativo de carpeta (backend) y, si el usuario
    // elige una distinta, el agente queda apuntando alli; abre un chat nuevo para que
    // capture la carpeta nueva y refresca la sidebar. Si cancela o repite, no cambia.
    async function selectWorkspace(): Promise<void> {
      const dir = await SelectWorkspace()
      if (dir && dir !== workspace.value) {
        workspace.value = dir
        reset()
      }
      await loadSessions()
    }

    // pickWorkspace fija una carpeta ya conocida sin pasar por el dialogo nativo: la
    // recablea via SetWorkspace, deja el agente apuntando alli y abre un chat nuevo
    // para que capture la carpeta. Repetir la carpeta vigente es un no-op (no recablea
    // ni descarta el lienzo). Lo usa el selector de carpeta del chat nuevo, que ofrece
    // las carpetas conocidas (knownWorkspaces) para elegir con un clic.
    async function pickWorkspace(path: string): Promise<void> {
      if (!path || path === workspace.value) return
      await SetWorkspace(path)
      workspace.value = path
      reset()
    }

    // restoreWorkspace fija la carpeta de trabajo al montar la vista. El backend
    // siempre arranca en la carpeta por defecto, asi que si hay una carpeta persistida
    // de una corrida anterior (rehidratada de localStorage) la re-aplica con
    // SetWorkspace: un chat nuevo sigue en la ultima carpeta usada entre reinicios. Si
    // esa carpeta ya no existe (SetWorkspace falla) o no habia ninguna, cae a la del
    // backend (loadWorkspace).
    async function restoreWorkspace(): Promise<void> {
      if (workspace.value) {
        try {
          await SetWorkspace(workspace.value)
          return
        } catch {
          // la carpeta persistida ya no existe: cae a la vigente del backend.
        }
      }
      await loadWorkspace()
    }

    // loadModel trae el modelo activo del backend una vez (espejo de loadSessions):
    // la UI lo usa para dimensionar la barra de contexto por modelo. Si el binding
    // no esta disponible (p. ej. arranque sin backend) cae a un modelo vacio: la
    // barra usa entonces la ventana por defecto.
    async function loadModel(): Promise<void> {
      try {
        model.value = await Model()
      } catch {
        model.value = ''
      }
    }

    // loadProjectFiles trae el listado de archivos del workspace del backend para
    // el @-menu del composer. Idempotente: la vista la llama una vez al montar. Si
    // el binding falla (arranque sin backend) degrada a lista vacia: el menu queda
    // sin candidatos en vez de romper.
    async function loadProjectFiles(): Promise<void> {
      try {
        projectFiles.value = await ListProjectFiles()
      } catch {
        projectFiles.value = []
      }
    }

    // loadCommands trae los slash-commands del backend para el menu del composer y los
    // normaliza a {name, description}. Idempotente: la vista la llama una vez al montar.
    // Si el binding falla (arranque sin backend) degrada a lista vacia: el menu queda
    // sin candidatos en vez de romper.
    async function loadCommands(): Promise<void> {
      try {
        const list = await ListCommands()
        commands.value = list.map((c) => ({
          name: c.Name,
          description: c.Description,
        }))
      } catch {
        commands.value = []
      }
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
      // Abrir un chat de otra carpeta cambia el workspace en vivo: el agente queda
      // apuntando a la carpeta en que se creo ese chat. Se hace antes de reproducir
      // el log para que un envio posterior corra en la carpeta correcta.
      const summary = sessions.value.find((s) => s.ID === id)
      if (summary?.Cwd && summary.Cwd !== workspace.value) {
        await SetWorkspace(summary.Cwd)
        workspace.value = summary.Cwd
      }
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
        EventsOn(`session:${sessionID.value}`, (ev: SessionEvent) =>
          applyEvent(ev),
        ),
        EventsOn(`session:${sessionID.value}:error`, (msg: string) =>
          applyError(msg),
        ),
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
      workspace,
      mode,
      plan,
      planExpanded,
      todos,
      usage,
      model,
      projectFiles,
      commands,
      applyEvent,
      applyError,
      clearError,
      reset,
      loadSessions,
      loadWorkspace,
      selectWorkspace,
      pickWorkspace,
      restoreWorkspace,
      loadModel,
      loadProjectFiles,
      loadCommands,
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
  },
  {
    // Solo se persiste la carpeta de trabajo: asi un chat nuevo sigue en la ultima
    // carpeta usada tras cerrar y reabrir la app (restoreWorkspace la re-aplica al
    // backend). El resto del store es estado vivo (log, streaming, suscripcion) cuya
    // fuente de verdad es el backend; no debe ir a localStorage.
    persist: { pick: ['workspace'] },
  },
)

// HMR: al editar este store, Vite recarga su definicion en caliente en vez de
// dejar viva la instancia vieja (que mantenia las referencias crudas y no
// reaccionaba al streaming). Sin esto un fix al store no se ve hasta reiniciar.
if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useChatStore, import.meta.hot))
}

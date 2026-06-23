import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

// El store toca la frontera Wails (bindings y runtime). En el test la
// reemplazamos por fakes para verificar el mapeo evento->estado en aislamiento.
vi.mock('../../wailsjs/go/main/App', () => ({
  SendPrompt: vi.fn(() => Promise.resolve()),
  SendPlanPrompt: vi.fn(() => Promise.resolve()),
  AcceptPlan: vi.fn(() => Promise.resolve()),
  Stop: vi.fn(),
  ResolveToolPermission: vi.fn(),
  ListSessions: vi.fn(() => Promise.resolve([])),
  SessionHistory: vi.fn(() => Promise.resolve([])),
}))
vi.mock('../../wailsjs/runtime/runtime', () => ({
  EventsOn: vi.fn(() => () => {}),
}))

import * as App from '../../wailsjs/go/main/App'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import { useChatStore } from './chat'

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
})

describe('chat store: texto de la IA (Text.*)', () => {
  it('coalesce un mensaje de IA a partir de Text.Started/Delta/Ended', () => {
    const store = useChatStore()

    store.applyEvent({ Kind: 'Text.Started' })
    store.applyEvent({ Kind: 'Text.Delta', Text: 'Hola' })
    store.applyEvent({ Kind: 'Text.Delta', Text: ' mundo' })

    expect(store.items).toHaveLength(1)
    expect(store.items[0]).toMatchObject({ kind: 'assistant', text: 'Hola mundo', streaming: true })

    store.applyEvent({ Kind: 'Text.Ended', Text: 'Hola mundo' })

    expect(store.items).toHaveLength(1)
    const item = store.items[0]
    expect(item.kind).toBe('assistant')
    if (item.kind === 'assistant') {
      expect(item.streaming).toBe(false)
      expect(item.text).toBe('Hola mundo')
    }
  })

  it('arranca un mensaje de IA aunque llegue Text.Delta sin Text.Started', () => {
    const store = useChatStore()

    store.applyEvent({ Kind: 'Text.Delta', Text: 'sin started' })

    expect(store.items).toHaveLength(1)
    expect(store.items[0]).toMatchObject({ kind: 'assistant', text: 'sin started' })
  })
})

describe('chat store: prompt del usuario', () => {
  it('promueve el prompt del usuario (Message.Role === "user") al log', () => {
    const store = useChatStore()

    store.applyEvent({ Message: { Role: 'user', Text: 'ping' } })

    expect(store.items).toHaveLength(1)
    expect(store.items[0]).toMatchObject({ kind: 'user', text: 'ping' })
  })

  it('no agrega un assistant Message de Text.Ended como item aparte', () => {
    const store = useChatStore()

    store.applyEvent({ Kind: 'Text.Started' })
    store.applyEvent({ Kind: 'Text.Delta', Text: 'x' })
    store.applyEvent({ Kind: 'Text.Ended', Text: 'x', Message: { Role: 'assistant', Text: 'x' } })

    expect(store.items).toHaveLength(1)
    expect(store.items[0].kind).toBe('assistant')
  })
})

describe('chat store: pensamiento (Reasoning.*)', () => {
  afterEach(() => vi.useRealTimers())

  it('coalesce el pensamiento y mide su duracion', () => {
    vi.useFakeTimers()
    vi.setSystemTime(1000)
    const store = useChatStore()

    store.applyEvent({ Kind: 'Reasoning.Started' })
    vi.setSystemTime(1450)
    store.applyEvent({ Kind: 'Reasoning.Delta', Text: 'pen' })
    store.applyEvent({ Kind: 'Reasoning.Delta', Text: 'sando' })
    store.applyEvent({ Kind: 'Reasoning.Ended', Text: 'pensando' })

    expect(store.items).toHaveLength(1)
    const item = store.items[0]
    expect(item.kind).toBe('reasoning')
    if (item.kind === 'reasoning') {
      expect(item.text).toBe('pensando')
      expect(item.streaming).toBe(false)
      expect(item.durationMs).toBe(450)
    }
  })

  it('arranca el pensamiento aunque llegue Reasoning.Delta sin Started', () => {
    const store = useChatStore()

    store.applyEvent({ Kind: 'Reasoning.Delta', Text: 'fragmento' })

    expect(store.items).toHaveLength(1)
    expect(store.items[0]).toMatchObject({ kind: 'reasoning', text: 'fragmento', streaming: true })
  })
})

describe('chat store: herramientas (Tool.*)', () => {
  it('Tool.Called crea un item en ejecucion y Tool.Success lo completa', () => {
    const store = useChatStore()

    store.applyEvent({ Kind: 'Tool.Called', CallID: 'c1', ToolName: 'echo', Input: { text: 'hi' } })

    expect(store.items).toHaveLength(1)
    expect(store.items[0]).toMatchObject({ kind: 'tool', callID: 'c1', name: 'echo', status: 'running' })

    store.applyEvent({ Kind: 'Tool.Success', CallID: 'c1', ToolName: 'echo', Text: 'hi' })

    const item = store.items[0]
    if (item.kind === 'tool') {
      expect(item.status).toBe('success')
      expect(item.output).toBe('hi')
    }
  })

  it('Tool.Failed marca el item como fallido con su causa', () => {
    const store = useChatStore()

    store.applyEvent({ Kind: 'Tool.Called', CallID: 'c2', ToolName: 'echo' })
    store.applyEvent({ Kind: 'Tool.Failed', CallID: 'c2', ToolName: 'echo', Error: 'boom' })

    const item = store.items[0]
    expect(item.kind).toBe('tool')
    if (item.kind === 'tool') {
      expect(item.status).toBe('failed')
      expect(item.error).toBe('boom')
    }
  })

  it('un resultado de tool con CallID desconocido no rompe ni cambia nada', () => {
    const store = useChatStore()

    store.applyEvent({ Kind: 'Tool.Success', CallID: 'fantasma', Text: 'x' })

    expect(store.items).toHaveLength(0)
  })
})

describe('chat store: tool permission (ask-before-run)', () => {
  it('Tool.Permission.Requested leaves the tool item pending approval', () => {
    const store = useChatStore()

    store.applyEvent({ Kind: 'Tool.Called', CallID: 'c1', ToolName: 'bash', Input: { command: 'ls' } })
    store.applyEvent({ Kind: 'Tool.Permission.Requested', CallID: 'c1', ToolName: 'bash' })

    const item = store.items[0]
    expect(item.kind).toBe('tool')
    if (item.kind === 'tool') {
      expect(item.status).toBe('pending')
    }
  })

  it('approveTool resolves the permission with true and leaves the item running', () => {
    const store = useChatStore()
    const sessionID = store.sessionID

    store.applyEvent({ Kind: 'Tool.Called', CallID: 'c1', ToolName: 'bash', Input: { command: 'ls' } })
    store.applyEvent({ Kind: 'Tool.Permission.Requested', CallID: 'c1', ToolName: 'bash' })
    store.approveTool('c1')

    expect(App.ResolveToolPermission).toHaveBeenCalledWith(sessionID, 'c1', true)
    const item = store.items[0]
    if (item.kind === 'tool') {
      expect(item.status).toBe('running')
    }
  })

  it('denyTool resolves the permission with false', () => {
    const store = useChatStore()
    const sessionID = store.sessionID

    store.applyEvent({ Kind: 'Tool.Called', CallID: 'c1', ToolName: 'bash', Input: { command: 'rm -rf /' } })
    store.applyEvent({ Kind: 'Tool.Permission.Requested', CallID: 'c1', ToolName: 'bash' })
    store.denyTool('c1')

    expect(App.ResolveToolPermission).toHaveBeenCalledWith(sessionID, 'c1', false)
    // The item leaves 'pending' (stops offering the buttons); the backend's
    // Tool.Failed confirms the final state.
    const item = store.items[0]
    if (item.kind === 'tool') {
      expect(item.status).not.toBe('pending')
    }
  })

  it('a Tool.Success after approval completes the item', () => {
    const store = useChatStore()

    store.applyEvent({ Kind: 'Tool.Called', CallID: 'c1', ToolName: 'bash', Input: { command: 'ls' } })
    store.applyEvent({ Kind: 'Tool.Permission.Requested', CallID: 'c1', ToolName: 'bash' })
    store.approveTool('c1')
    store.applyEvent({ Kind: 'Tool.Success', CallID: 'c1', Text: 'file.txt' })

    const item = store.items[0]
    if (item.kind === 'tool') {
      expect(item.status).toBe('success')
      expect(item.output).toBe('file.txt')
    }
  })
})

describe('chat store: historial de sesiones (sidebar)', () => {
  it('loadSessions trae la lista del backend y la expone en sessions', async () => {
    vi.mocked(App.ListSessions).mockResolvedValueOnce([
      { ID: 's2', Title: 'segunda' },
      { ID: 's1', Title: 'primera' },
    ] as never)
    const store = useChatStore()

    await store.loadSessions()

    expect(App.ListSessions).toHaveBeenCalled()
    expect(store.sessions).toEqual([
      { ID: 's2', Title: 'segunda' },
      { ID: 's1', Title: 'primera' },
    ])
  })

  it('loadSession fija el sessionID activo y reproduce el historial via applyEvent', async () => {
    vi.mocked(App.SessionHistory).mockResolvedValueOnce([
      { Message: { Role: 'user', Text: 'hola' } },
      { Kind: 'Text.Started' },
      { Kind: 'Text.Delta', Text: 'mundo' },
      { Kind: 'Text.Ended', Text: 'mundo' },
      { Kind: 'Step.Ended' },
    ] as never)
    const store = useChatStore()

    await store.loadSession('s1')

    expect(App.SessionHistory).toHaveBeenCalledWith('s1')
    expect(store.sessionID).toBe('s1')
    // El historial reproducido reconstruye la conversacion: usuario + asistente.
    expect(store.items.map((i) => i.kind)).toEqual(['user', 'assistant'])
    const assistant = store.items[1]
    if (assistant.kind === 'assistant') {
      // Converge a estado terminal: el Text.Ended dejo el item no-streaming.
      expect(assistant.streaming).toBe(false)
      expect(assistant.text).toBe('mundo')
    }
    // El log durable incluye Step.Ended, asi que running queda apagado.
    expect(store.running).toBe(false)
  })

  it('loadSession limpia el log previo antes de reproducir la sesion elegida', async () => {
    const store = useChatStore()
    // Estado de una sesion previa que NO debe sobrevivir al cambio.
    store.applyEvent({ Message: { Role: 'user', Text: 'viejo' } })
    expect(store.items).toHaveLength(1)

    vi.mocked(App.SessionHistory).mockResolvedValueOnce([
      { Message: { Role: 'user', Text: 'nuevo' } },
    ] as never)

    await store.loadSession('s9')

    expect(store.items).toHaveLength(1)
    expect(store.items[0]).toMatchObject({ kind: 'user', text: 'nuevo' })
  })

  it('loadSession mueve los listeners al canal de la sesion abierta', async () => {
    const store = useChatStore()
    store.subscribe()

    vi.mocked(App.SessionHistory).mockResolvedValueOnce([] as never)
    await store.loadSession('s7')

    // El ultimo par de suscripciones apunta al canal de s7.
    expect(EventsOn).toHaveBeenCalledWith('session:s7', expect.any(Function))
    expect(EventsOn).toHaveBeenCalledWith('session:s7:error', expect.any(Function))
  })

  it('send refresca la lista de sesiones para que la conversacion nueva aparezca', async () => {
    vi.mocked(App.ListSessions).mockResolvedValue([{ ID: 'x', Title: 'hola' }] as never)
    const store = useChatStore()

    await store.send('hola')

    expect(App.ListSessions).toHaveBeenCalled()
  })
})

describe('chat store: extensibilidad (forward-compat)', () => {
  it('ignora eventos con Kind desconocido sin romper ni ensuciar el log', () => {
    const store = useChatStore()

    // Una capacidad futura del agente puede emitir un Kind que la UI aun no
    // entiende; debe degradar con elegancia hasta que se le de soporte.
    store.applyEvent({ Kind: 'Plan.Updated', Text: 'algo nuevo' })
    store.applyEvent({ Kind: 'Future.Whatever' })

    expect(store.items).toHaveLength(0)
  })
})

describe('chat store: rehidratacion del plan', () => {
  it('un mensaje de usuario posterior a present_plan cierra el plan (ya accionado)', () => {
    const store = useChatStore()

    store.applyEvent({
      Kind: 'Tool.Called',
      ToolName: 'present_plan',
      CallID: 'c1',
      Input: { title: 'T', plan: 'cuerpo' },
    })
    expect(store.plan).not.toBeNull()

    // AcceptPlan promueve un prompt de usuario ("implementa..."); solicitar cambio
    // promueve el feedback. En ambos casos, un mensaje de usuario DESPUES del
    // present_plan significa que el plan ya fue accionado: no debe reabrirse.
    store.applyEvent({ Message: { Role: 'user', Text: 'El plan fue aprobado. Implementalo ahora.' } })

    expect(store.plan).toBeNull()
  })

  it('present_plan sin mensaje de usuario posterior mantiene el plan abierto', () => {
    const store = useChatStore()

    store.applyEvent({ Message: { Role: 'user', Text: 'planea X' } })
    store.applyEvent({
      Kind: 'Tool.Called',
      ToolName: 'present_plan',
      CallID: 'c1',
      Input: { title: 'T', plan: 'cuerpo' },
    })

    expect(store.plan).toMatchObject({ markdown: 'cuerpo' })
  })

  it('loadSession NO reabre un plan ya ejecutado', async () => {
    const store = useChatStore()
    vi.mocked(App.SessionHistory).mockResolvedValueOnce([
      { Message: { Role: 'user', Text: 'planea X' } },
      { Kind: 'Tool.Called', ToolName: 'present_plan', CallID: 'c1', Input: { title: 'T', plan: 'v1' } },
      { Message: { Role: 'user', Text: 'implementa el plan' } },
    ] as never)

    await store.loadSession('s1')

    expect(store.plan).toBeNull()
  })

  it('loadSession reabre el plan si la sesion quedo esperando decision', async () => {
    const store = useChatStore()
    vi.mocked(App.SessionHistory).mockResolvedValueOnce([
      { Message: { Role: 'user', Text: 'planea X' } },
      { Kind: 'Tool.Called', ToolName: 'present_plan', CallID: 'c1', Input: { title: 'T', plan: 'v1' } },
    ] as never)

    await store.loadSession('s1')

    expect(store.plan).toMatchObject({ markdown: 'v1' })
  })
})

describe('chat store: flujo continuo y ordenado entre tipos', () => {
  it('mantiene usuario, pensamiento, IA y tool en orden de evento', () => {
    const store = useChatStore()

    store.applyEvent({ Message: { Role: 'user', Text: 'ping' } })
    store.applyEvent({ Kind: 'Reasoning.Started' })
    store.applyEvent({ Kind: 'Reasoning.Ended', Text: 'mmm' })
    store.applyEvent({ Kind: 'Text.Started' })
    store.applyEvent({ Kind: 'Text.Ended', Text: 'pong' })
    store.applyEvent({ Kind: 'Tool.Called', CallID: 'c1', ToolName: 'echo' })

    expect(store.items.map((i) => i.kind)).toEqual(['user', 'reasoning', 'assistant', 'tool'])
  })
})

describe('chat store: estado de ejecucion', () => {
  it('Step.Started no agrega items', () => {
    const store = useChatStore()

    store.applyEvent({ Kind: 'Step.Started' })

    expect(store.items).toHaveLength(0)
  })

  it('Step.Ended apaga running', () => {
    const store = useChatStore()
    store.running = true

    store.applyEvent({ Kind: 'Step.Ended' })

    expect(store.running).toBe(false)
  })

  it('Step.Failed apaga running y guarda el error', () => {
    const store = useChatStore()
    store.running = true

    store.applyEvent({ Kind: 'Step.Failed', Error: 'limite de pasos' })

    expect(store.running).toBe(false)
    expect(store.errorText).toBe('limite de pasos')
  })

  it('applyError apaga running y guarda el mensaje del canal de error', () => {
    const store = useChatStore()
    store.running = true

    store.applyError('fallo del proveedor')

    expect(store.running).toBe(false)
    expect(store.errorText).toBe('fallo del proveedor')
  })

  it('clearError descarta el error visible sin tocar el resto del estado', () => {
    const store = useChatStore()
    store.applyEvent({ Message: { Role: 'user', Text: 'ping' } })
    store.applyError('fallo del proveedor')

    store.clearError()

    expect(store.errorText).toBeNull()
    // El log permanece: descartar el aviso no borra la conversacion.
    expect(store.items).toHaveLength(1)
  })
})

describe('chat store: acciones sobre los bindings', () => {
  it('send enciende running y llama SendPrompt con (sessionID, texto)', async () => {
    const store = useChatStore()
    const sessionID = store.sessionID

    await store.send('  hola  ')

    expect(store.running).toBe(true)
    expect(App.SendPrompt).toHaveBeenCalledWith(sessionID, 'hola')
  })

  it('send ignora texto vacio o en blanco', async () => {
    const store = useChatStore()

    await store.send('   ')

    expect(store.running).toBe(false)
    expect(App.SendPrompt).not.toHaveBeenCalled()
  })

  it('stop llama Stop con el sessionID', () => {
    const store = useChatStore()
    const sessionID = store.sessionID

    store.stop()

    expect(App.Stop).toHaveBeenCalledWith(sessionID)
  })

  it('subscribe registra los canales session:<id> y session:<id>:error', () => {
    const store = useChatStore()
    const sessionID = store.sessionID

    store.subscribe()

    expect(EventsOn).toHaveBeenCalledWith(`session:${sessionID}`, expect.any(Function))
    expect(EventsOn).toHaveBeenCalledWith(`session:${sessionID}:error`, expect.any(Function))
  })

  it('reset limpia el log, las herramientas y el error para un lienzo nuevo', () => {
    const store = useChatStore()
    store.applyEvent({ Message: { Role: 'user', Text: 'hola' } })
    store.applyEvent({ Kind: 'Tool.Called', CallID: 'c1', ToolName: 'echo' })
    store.applyError('algo fallo')
    store.running = true

    store.reset()
    // Tras el reset, un resultado de la tool previa no debe reaparecer.
    store.applyEvent({ Kind: 'Tool.Success', CallID: 'c1', Text: 'hi' })

    expect(store.items).toHaveLength(0)
    expect(store.running).toBe(false)
    expect(store.errorText).toBeNull()
  })

  it('reset abre una sesion nueva para que el siguiente prompt no reutilice contexto', async () => {
    const store = useChatStore()

    await store.send('primero')
    const firstSessionID = vi.mocked(App.SendPrompt).mock.calls[0][0]

    store.reset()
    await store.send('segundo')

    const secondSessionID = vi.mocked(App.SendPrompt).mock.calls[1][0]
    expect(secondSessionID).not.toBe(firstSessionID)
    expect(App.SendPrompt).toHaveBeenLastCalledWith(secondSessionID, 'segundo')
  })

  it('reset mueve los listeners al canal de la nueva sesion', () => {
    const store = useChatStore()

    store.subscribe()
    const firstSessionID = store.sessionID
    store.reset()
    const secondSessionID = store.sessionID

    expect(secondSessionID).not.toBe(firstSessionID)
    expect(EventsOn).toHaveBeenNthCalledWith(3, `session:${secondSessionID}`, expect.any(Function))
    expect(EventsOn).toHaveBeenNthCalledWith(4, `session:${secondSessionID}:error`, expect.any(Function))
  })
})

describe('chat store: modo plan', () => {
  it('send usa SendPlanPrompt cuando mode==="plan"', async () => {
    const store = useChatStore()
    const sessionID = store.sessionID
    store.toggleMode()

    await store.send('planea X')

    expect(App.SendPlanPrompt).toHaveBeenCalledWith(sessionID, 'planea X')
    expect(App.SendPrompt).not.toHaveBeenCalled()
  })

  it('send usa SendPrompt cuando mode==="normal"', async () => {
    const store = useChatStore()
    const sessionID = store.sessionID

    await store.send('hola')

    expect(App.SendPrompt).toHaveBeenCalledWith(sessionID, 'hola')
    expect(App.SendPlanPrompt).not.toHaveBeenCalled()
  })

  it('Tool.Called present_plan abre el plan y no crea tool item', () => {
    const store = useChatStore()

    store.applyEvent({
      Kind: 'Tool.Called',
      ToolName: 'present_plan',
      CallID: 'c1',
      Input: { title: 'T', plan: '# Plan\n- a' },
    })

    expect(store.plan).toEqual({ callID: 'c1', title: 'T', markdown: '# Plan\n- a' })
    expect(store.items).toHaveLength(0)
  })

  it('acceptPlan ejecuta en modo normal y cierra el plan', async () => {
    const store = useChatStore()
    const sessionID = store.sessionID
    store.applyEvent({
      Kind: 'Tool.Called',
      ToolName: 'present_plan',
      CallID: 'c1',
      Input: { title: 'T', plan: '# Plan' },
    })

    await store.acceptPlan()

    expect(App.AcceptPlan).toHaveBeenCalledWith(sessionID)
    expect(store.mode).toBe('normal')
    expect(store.plan).toBeNull()
  })

  it('requestPlanChange reescribe el plan via SendPlanPrompt', async () => {
    const store = useChatStore()
    const sessionID = store.sessionID
    store.applyEvent({
      Kind: 'Tool.Called',
      ToolName: 'present_plan',
      CallID: 'c1',
      Input: { title: 'T', plan: '# Plan' },
    })

    await store.requestPlanChange('cambia el paso 2')

    expect(App.SendPlanPrompt).toHaveBeenCalledWith(sessionID, 'cambia el paso 2')
    expect(store.plan).toBeNull()
    expect(store.mode).toBe('plan')
  })

  it('Tool.Called present_plan tolera Input como string JSON', () => {
    const store = useChatStore()

    store.applyEvent({
      Kind: 'Tool.Called',
      ToolName: 'present_plan',
      CallID: 'c1',
      Input: '{"title":"T2","plan":"texto"}',
    })

    expect(store.plan?.markdown).toBe('texto')
    expect(store.plan?.title).toBe('T2')
  })

  it('requestPlanChange ignora feedback vacio', async () => {
    const store = useChatStore()
    store.applyEvent({
      Kind: 'Tool.Called',
      ToolName: 'present_plan',
      CallID: 'c1',
      Input: { title: 'T', plan: '# Plan' },
    })
    const planBefore = store.plan

    await store.requestPlanChange('   ')

    expect(App.SendPlanPrompt).not.toHaveBeenCalled()
    expect(store.plan).toEqual(planBefore)
  })

  it('toggleMode alterna normal<->plan', () => {
    const store = useChatStore()

    expect(store.mode).toBe('normal')
    store.toggleMode()
    expect(store.mode).toBe('plan')
    store.toggleMode()
    expect(store.mode).toBe('normal')
  })
})

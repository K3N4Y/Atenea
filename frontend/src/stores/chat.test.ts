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
  DeleteSession: vi.fn(() => Promise.resolve()),
  Model: vi.fn(() => Promise.resolve('anthropic/claude-opus-4.8')),
  ListProjectFiles: vi.fn(() => Promise.resolve([])),
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
    expect(store.items[0]).toMatchObject({
      kind: 'assistant',
      text: 'Hola mundo',
      streaming: true,
    })

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
    expect(store.items[0]).toMatchObject({
      kind: 'assistant',
      text: 'sin started',
    })
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
    store.applyEvent({
      Kind: 'Text.Ended',
      Text: 'x',
      Message: { Role: 'assistant', Text: 'x' },
    })

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
    expect(store.items[0]).toMatchObject({
      kind: 'reasoning',
      text: 'fragmento',
      streaming: true,
    })
  })
})

describe('chat store: herramientas (Tool.*)', () => {
  it('Tool.Called crea un item en ejecucion y Tool.Success lo completa', () => {
    const store = useChatStore()

    store.applyEvent({
      Kind: 'Tool.Called',
      CallID: 'c1',
      ToolName: 'echo',
      Input: { text: 'hi' },
    })

    expect(store.items).toHaveLength(1)
    expect(store.items[0]).toMatchObject({
      kind: 'tool',
      callID: 'c1',
      name: 'echo',
      status: 'running',
    })

    store.applyEvent({
      Kind: 'Tool.Success',
      CallID: 'c1',
      ToolName: 'echo',
      Text: 'hi',
    })

    const item = store.items[0]
    if (item.kind === 'tool') {
      expect(item.status).toBe('success')
      expect(item.output).toBe('hi')
    }
  })

  it('Tool.Success con Diff puebla item.diff (edit/write); sin Diff queda vacio', () => {
    const store = useChatStore()

    store.applyEvent({
      Kind: 'Tool.Called',
      CallID: 'e1',
      ToolName: 'edit',
      Input: {},
    })
    store.applyEvent({
      Kind: 'Tool.Success',
      CallID: 'e1',
      ToolName: 'edit',
      Text: '[foo.go#ab12]',
      Diff: '--- a/foo.go\n+++ b/foo.go\n@@ -1 +1 @@\n-a\n+b\n',
    })

    const edited = store.items[0]
    if (edited.kind === 'tool') {
      expect(edited.diff).toContain('+++ b/foo.go')
    }

    // Una tool sin diff (bash/echo) deja item.diff vacio.
    store.applyEvent({ Kind: 'Tool.Called', CallID: 'b1', ToolName: 'echo' })
    store.applyEvent({
      Kind: 'Tool.Success',
      CallID: 'b1',
      ToolName: 'echo',
      Text: 'ok',
    })
    const bashed = store.items[1]
    if (bashed.kind === 'tool') {
      expect(bashed.diff).toBe('')
    }
  })

  it('Tool.Failed marca el item como fallido con su causa', () => {
    const store = useChatStore()

    store.applyEvent({ Kind: 'Tool.Called', CallID: 'c2', ToolName: 'echo' })
    store.applyEvent({
      Kind: 'Tool.Failed',
      CallID: 'c2',
      ToolName: 'echo',
      Error: 'boom',
    })

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

    store.applyEvent({
      Kind: 'Tool.Called',
      CallID: 'c1',
      ToolName: 'bash',
      Input: { command: 'ls' },
    })
    store.applyEvent({
      Kind: 'Tool.Permission.Requested',
      CallID: 'c1',
      ToolName: 'bash',
    })

    const item = store.items[0]
    expect(item.kind).toBe('tool')
    if (item.kind === 'tool') {
      expect(item.status).toBe('pending')
    }
  })

  it('approveTool resolves the permission with true and leaves the item running', () => {
    const store = useChatStore()
    const sessionID = store.sessionID

    store.applyEvent({
      Kind: 'Tool.Called',
      CallID: 'c1',
      ToolName: 'bash',
      Input: { command: 'ls' },
    })
    store.applyEvent({
      Kind: 'Tool.Permission.Requested',
      CallID: 'c1',
      ToolName: 'bash',
    })
    store.approveTool('c1')

    expect(App.ResolveToolPermission).toHaveBeenCalledWith(
      sessionID,
      'c1',
      true,
    )
    const item = store.items[0]
    if (item.kind === 'tool') {
      expect(item.status).toBe('running')
    }
  })

  it('denyTool resolves the permission with false', () => {
    const store = useChatStore()
    const sessionID = store.sessionID

    store.applyEvent({
      Kind: 'Tool.Called',
      CallID: 'c1',
      ToolName: 'bash',
      Input: { command: 'rm -rf /' },
    })
    store.applyEvent({
      Kind: 'Tool.Permission.Requested',
      CallID: 'c1',
      ToolName: 'bash',
    })
    store.denyTool('c1')

    expect(App.ResolveToolPermission).toHaveBeenCalledWith(
      sessionID,
      'c1',
      false,
    )
    // The item leaves 'pending' (stops offering the buttons); the backend's
    // Tool.Failed confirms the final state.
    const item = store.items[0]
    if (item.kind === 'tool') {
      expect(item.status).not.toBe('pending')
    }
  })

  it('a Tool.Success after approval completes the item', () => {
    const store = useChatStore()

    store.applyEvent({
      Kind: 'Tool.Called',
      CallID: 'c1',
      ToolName: 'bash',
      Input: { command: 'ls' },
    })
    store.applyEvent({
      Kind: 'Tool.Permission.Requested',
      CallID: 'c1',
      ToolName: 'bash',
    })
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
    expect(EventsOn).toHaveBeenCalledWith(
      'session:s7:error',
      expect.any(Function),
    )
  })

  it('send refresca la lista de sesiones para que la conversacion nueva aparezca', async () => {
    vi.mocked(App.ListSessions).mockResolvedValue([
      { ID: 'x', Title: 'hola' },
    ] as never)
    const store = useChatStore()

    await store.send('hola')

    expect(App.ListSessions).toHaveBeenCalled()
  })
})

describe('chat store: borrar sesion', () => {
  it('deleteSession llama al binding y refresca la sidebar', async () => {
    const store = useChatStore()

    await store.deleteSession('chat-x')

    expect(App.DeleteSession).toHaveBeenCalledWith('chat-x')
    expect(App.ListSessions).toHaveBeenCalled()
  })

  it('borrar la sesion activa abre un chat nuevo', async () => {
    const store = useChatStore()
    const before = store.sessionID

    await store.deleteSession(before)

    expect(store.sessionID).not.toBe(before)
  })

  it('borrar una sesion NO activa no cambia la sesion actual', async () => {
    const store = useChatStore()
    const active = store.sessionID

    await store.deleteSession('otra-sesion-distinta')

    // Borrar otra sesion no reabre un chat nuevo: la activa se conserva.
    expect(store.sessionID).toBe(active)
    expect(App.DeleteSession).toHaveBeenCalledWith('otra-sesion-distinta')
    // ...pero la sidebar igual se refresca para que la fila borrada desaparezca.
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
    store.applyEvent({
      Message: {
        Role: 'user',
        Text: 'El plan fue aprobado. Implementalo ahora.',
      },
    })

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
      {
        Kind: 'Tool.Called',
        ToolName: 'present_plan',
        CallID: 'c1',
        Input: { title: 'T', plan: 'v1' },
      },
      { Message: { Role: 'user', Text: 'implementa el plan' } },
    ] as never)

    await store.loadSession('s1')

    expect(store.plan).toBeNull()
  })

  it('loadSession reabre el plan si la sesion quedo esperando decision', async () => {
    const store = useChatStore()
    vi.mocked(App.SessionHistory).mockResolvedValueOnce([
      { Message: { Role: 'user', Text: 'planea X' } },
      {
        Kind: 'Tool.Called',
        ToolName: 'present_plan',
        CallID: 'c1',
        Input: { title: 'T', plan: 'v1' },
      },
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

    expect(store.items.map((i) => i.kind)).toEqual([
      'user',
      'reasoning',
      'assistant',
      'tool',
    ])
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

    expect(EventsOn).toHaveBeenCalledWith(
      `session:${sessionID}`,
      expect.any(Function),
    )
    expect(EventsOn).toHaveBeenCalledWith(
      `session:${sessionID}:error`,
      expect.any(Function),
    )
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
    expect(EventsOn).toHaveBeenNthCalledWith(
      3,
      `session:${secondSessionID}`,
      expect.any(Function),
    )
    expect(EventsOn).toHaveBeenNthCalledWith(
      4,
      `session:${secondSessionID}:error`,
      expect.any(Function),
    )
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

    expect(store.plan).toEqual({
      callID: 'c1',
      title: 'T',
      markdown: '# Plan\n- a',
    })
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

    expect(App.SendPlanPrompt).toHaveBeenCalledWith(
      sessionID,
      'cambia el paso 2',
    )
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

// Edge cases del modo plan que un usuario real provoca: el agente devuelve un
// Input malformado, el usuario hace doble click en Aceptar, abre una sesion vieja
// que quedo esperando decision, el agente re-planifica, o el turno de planeo
// falla. No son pasos logicos del flujo feliz: son los tropiezos que ocurren.
describe('chat store: modo plan (edge cases de usuario)', () => {
  it('present_plan con Input string JSON roto: abre el plan con campos vacios, no rompe ni crea items', () => {
    const store = useChatStore()

    store.applyEvent({
      Kind: 'Tool.Called',
      ToolName: 'present_plan',
      CallID: 'c1',
      Input: '{"title":"x","plan":', // JSON truncado
    })

    expect(store.plan).toEqual({ callID: 'c1', title: '', markdown: '' })
    expect(store.items).toHaveLength(0)
  })

  it('present_plan con Input null: degrada a campos vacios sin romper', () => {
    const store = useChatStore()

    store.applyEvent({
      Kind: 'Tool.Called',
      ToolName: 'present_plan',
      CallID: 'c1',
      Input: null,
    })

    expect(store.plan).toEqual({ callID: 'c1', title: '', markdown: '' })
  })

  it('present_plan con plan no-string (numero): markdown vacio, conserva el title valido', () => {
    const store = useChatStore()

    store.applyEvent({
      Kind: 'Tool.Called',
      ToolName: 'present_plan',
      CallID: 'c1',
      Input: { title: 'T', plan: 123 },
    })

    expect(store.plan).toEqual({ callID: 'c1', title: 'T', markdown: '' })
  })

  it('un segundo present_plan sobrescribe el plan vigente y sigue sin crear items', () => {
    const store = useChatStore()

    store.applyEvent({
      Kind: 'Tool.Called',
      ToolName: 'present_plan',
      CallID: 'c1',
      Input: { title: 'A', plan: 'uno' },
    })
    store.applyEvent({
      Kind: 'Tool.Called',
      ToolName: 'present_plan',
      CallID: 'c2',
      Input: { title: 'B', plan: 'dos' },
    })

    expect(store.plan).toEqual({ callID: 'c2', title: 'B', markdown: 'dos' })
    expect(store.items).toHaveLength(0)
  })

  it('doble acceptPlan (doble click): no lanza, cierra el plan y llama AcceptPlan cada vez (sin guarda)', async () => {
    const store = useChatStore()
    const sessionID = store.sessionID
    store.applyEvent({
      Kind: 'Tool.Called',
      ToolName: 'present_plan',
      CallID: 'c1',
      Input: { title: 'T', plan: '# Plan' },
    })

    await store.acceptPlan()
    await store.acceptPlan()

    expect(App.AcceptPlan).toHaveBeenCalledTimes(2)
    expect(App.AcceptPlan).toHaveBeenCalledWith(sessionID)
    expect(store.plan).toBeNull()
  })

  it('enviar un prompt nuevo con un plan abierto lo cierra (no se queda el overlay viejo)', async () => {
    const store = useChatStore()
    store.applyEvent({
      Kind: 'Tool.Called',
      ToolName: 'present_plan',
      CallID: 'c1',
      Input: { title: 'T', plan: '# Plan' },
    })
    expect(store.plan).not.toBeNull()

    await store.send('mejor hablemos de otra cosa')

    expect(store.plan).toBeNull()
    expect(App.SendPrompt).toHaveBeenCalledWith(
      store.sessionID,
      'mejor hablemos de otra cosa',
    )
  })

  it('requestPlanChange desde modo normal fuerza el modo plan (un plan que aparecio sin el toggle)', async () => {
    const store = useChatStore()
    expect(store.mode).toBe('normal')
    store.applyEvent({
      Kind: 'Tool.Called',
      ToolName: 'present_plan',
      CallID: 'c1',
      Input: { title: 'T', plan: '# Plan' },
    })

    await store.requestPlanChange('reescribe el paso 1')

    expect(store.mode).toBe('plan')
    expect(App.SendPlanPrompt).toHaveBeenCalledWith(
      store.sessionID,
      'reescribe el paso 1',
    )
    expect(store.plan).toBeNull()
  })

  it('acceptPlan descarta un error visible previo al ejecutar', async () => {
    const store = useChatStore()
    store.applyError('el proveedor fallo antes')
    store.applyEvent({
      Kind: 'Tool.Called',
      ToolName: 'present_plan',
      CallID: 'c1',
      Input: { title: 'T', plan: '# Plan' },
    })

    await store.acceptPlan()

    expect(store.errorText).toBeNull()
  })

  it('Step.Failed durante el planeo NO cierra el plan: el overlay sigue con el error de fondo', () => {
    const store = useChatStore()
    store.applyEvent({
      Kind: 'Tool.Called',
      ToolName: 'present_plan',
      CallID: 'c1',
      Input: { title: 'T', plan: '# Plan' },
    })
    store.running = true

    store.applyEvent({ Kind: 'Step.Failed', Error: 'se corto el stream' })

    expect(store.plan).not.toBeNull()
    expect(store.running).toBe(false)
    expect(store.errorText).toBe('se corto el stream')
  })

  it('reset con un plan abierto en modo plan limpia ambos para el lienzo nuevo', () => {
    const store = useChatStore()
    store.toggleMode()
    store.applyEvent({
      Kind: 'Tool.Called',
      ToolName: 'present_plan',
      CallID: 'c1',
      Input: { title: 'T', plan: '# Plan' },
    })
    expect(store.plan).not.toBeNull()
    expect(store.mode).toBe('plan')

    store.reset()

    expect(store.plan).toBeNull()
    expect(store.mode).toBe('normal')
  })

  it('present_plan abre el plan expandido por defecto', () => {
    const store = useChatStore()

    store.applyEvent({
      Kind: 'Tool.Called',
      ToolName: 'present_plan',
      CallID: 'c1',
      Input: { title: 'T', plan: '# Plan' },
    })

    expect(store.planExpanded).toBe(true)
  })

  it('togglePlanExpanded alterna entre expandido y minimizado', () => {
    const store = useChatStore()
    store.applyEvent({
      Kind: 'Tool.Called',
      ToolName: 'present_plan',
      CallID: 'c1',
      Input: { title: 'T', plan: '# Plan' },
    })

    store.togglePlanExpanded()
    expect(store.planExpanded).toBe(false)
    store.togglePlanExpanded()
    expect(store.planExpanded).toBe(true)
  })

  it('un present_plan posterior reabre el plan expandido aunque estuviera minimizado', () => {
    const store = useChatStore()
    store.applyEvent({
      Kind: 'Tool.Called',
      ToolName: 'present_plan',
      CallID: 'c1',
      Input: { title: 'T', plan: 'v1' },
    })
    store.togglePlanExpanded()
    expect(store.planExpanded).toBe(false)

    // El agente reescribe el plan: la nueva version se abre expandida.
    store.applyEvent({
      Kind: 'Tool.Called',
      ToolName: 'present_plan',
      CallID: 'c2',
      Input: { title: 'T', plan: 'v2' },
    })

    expect(store.planExpanded).toBe(true)
  })

  it('loadSession reabre un plan pendiente pero deja el modo en normal (el toggle no refleja el plan pendiente)', async () => {
    const store = useChatStore()
    store.toggleMode()
    expect(store.mode).toBe('plan')
    vi.mocked(App.SessionHistory).mockResolvedValueOnce([
      { Message: { Role: 'user', Text: 'planea X' } },
      {
        Kind: 'Tool.Called',
        ToolName: 'present_plan',
        CallID: 'c1',
        Input: { title: 'T', plan: 'v1' },
      },
    ] as never)

    await store.loadSession('s1')

    // El plan vuelve a abrirse via rehidratacion...
    expect(store.plan).toMatchObject({ markdown: 'v1' })
    // ...pero clearLog reseteo el modo: el composer mostraria "normal" aun con un
    // plan esperando decision. Pedir un cambio re-sincroniza (requestPlanChange).
    expect(store.mode).toBe('normal')
  })
})

// El Usage llega en Step.Ended (input/output/reasoning/cache). El store lo guarda
// en camelCase para que la UI pinte el contexto usado por modelo, sin costos.
describe('chat store: uso de tokens (Usage)', () => {
  it('Step.Ended con Usage llena store.usage', () => {
    const store = useChatStore()

    store.applyEvent({
      Kind: 'Step.Ended',
      Usage: {
        InputTokens: 1200,
        OutputTokens: 340,
        ReasoningTokens: 0,
        CacheReadTokens: 0,
        CacheWriteTokens: 0,
      },
    })

    expect(store.usage).toMatchObject({ inputTokens: 1200, outputTokens: 340 })
  })

  it('el ultimo Step.Ended gana', () => {
    const store = useChatStore()

    store.applyEvent({
      Kind: 'Step.Ended',
      Usage: {
        InputTokens: 1200,
        OutputTokens: 340,
        ReasoningTokens: 0,
        CacheReadTokens: 0,
        CacheWriteTokens: 0,
      },
    })
    store.applyEvent({
      Kind: 'Step.Ended',
      Usage: {
        InputTokens: 5000,
        OutputTokens: 800,
        ReasoningTokens: 0,
        CacheReadTokens: 0,
        CacheWriteTokens: 0,
      },
    })

    expect(store.usage).toMatchObject({ inputTokens: 5000, outputTokens: 800 })
  })

  it('clearLog (via reset) resetea usage a null', () => {
    const store = useChatStore()
    store.applyEvent({
      Kind: 'Step.Ended',
      Usage: {
        InputTokens: 1200,
        OutputTokens: 340,
        ReasoningTokens: 0,
        CacheReadTokens: 0,
        CacheWriteTokens: 0,
      },
    })
    expect(store.usage).not.toBeNull()

    store.reset()

    expect(store.usage).toBeNull()
  })

  it('Step.Ended sin Usage no pisa el usage previo', () => {
    const store = useChatStore()
    store.applyEvent({
      Kind: 'Step.Ended',
      Usage: {
        InputTokens: 1200,
        OutputTokens: 340,
        ReasoningTokens: 0,
        CacheReadTokens: 0,
        CacheWriteTokens: 0,
      },
    })

    // Un Step.Ended sin Usage (p. ej. un step sin llamada al modelo) no debe
    // borrar la ocupacion de contexto ya conocida: el ultimo Usage real se
    // conserva en vez de quedar en null.
    store.applyEvent({ Kind: 'Step.Ended' })

    expect(store.usage).toMatchObject({ inputTokens: 1200 })
  })

  it('loadModel trae el modelo del binding', async () => {
    const store = useChatStore()

    await store.loadModel()

    expect(store.model).toBe('anthropic/claude-opus-4.8')
  })

  it('loadModel cae a modelo vacio si el binding falla', async () => {
    // Sin backend disponible el binding rechaza; loadModel degrada a modelo
    // vacio para que la barra use la ventana por defecto en vez de romper.
    vi.mocked(App.Model).mockRejectedValueOnce(new Error('no backend'))
    const store = useChatStore()

    await store.loadModel()

    expect(store.model).toBe('')
  })

  it('loadProjectFiles trae las rutas del workspace para el @-menu', async () => {
    vi.mocked(App.ListProjectFiles).mockResolvedValueOnce([
      'app.go',
      'internal/tool/glob.go',
    ])
    const store = useChatStore()

    await store.loadProjectFiles()

    expect(App.ListProjectFiles).toHaveBeenCalled()
    expect(store.projectFiles).toEqual(['app.go', 'internal/tool/glob.go'])
  })

  it('loadProjectFiles degrada a lista vacia si el binding falla', async () => {
    // Sin backend el binding rechaza; el @-menu simplemente queda sin candidatos
    // en vez de romper el composer.
    vi.mocked(App.ListProjectFiles).mockRejectedValueOnce(
      new Error('no backend'),
    )
    const store = useChatStore()

    await store.loadProjectFiles()

    expect(store.projectFiles).toEqual([])
  })
})

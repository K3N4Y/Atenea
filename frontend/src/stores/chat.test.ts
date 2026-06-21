import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

// El store toca la frontera Wails (bindings y runtime). En el test la
// reemplazamos por fakes para verificar el mapeo evento->estado en aislamiento.
vi.mock('../../wailsjs/go/main/App', () => ({
  SendPrompt: vi.fn(() => Promise.resolve()),
  Stop: vi.fn(),
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
})

describe('chat store: acciones sobre los bindings', () => {
  it('send enciende running y llama SendPrompt con (sessionID, texto)', async () => {
    const store = useChatStore()

    await store.send('  hola  ')

    expect(store.running).toBe(true)
    expect(App.SendPrompt).toHaveBeenCalledWith('main', 'hola')
  })

  it('send ignora texto vacio o en blanco', async () => {
    const store = useChatStore()

    await store.send('   ')

    expect(store.running).toBe(false)
    expect(App.SendPrompt).not.toHaveBeenCalled()
  })

  it('stop llama Stop con el sessionID', () => {
    const store = useChatStore()

    store.stop()

    expect(App.Stop).toHaveBeenCalledWith('main')
  })

  it('subscribe registra los canales session:<id> y session:<id>:error', () => {
    const store = useChatStore()

    store.subscribe()

    expect(EventsOn).toHaveBeenCalledWith('session:main', expect.any(Function))
    expect(EventsOn).toHaveBeenCalledWith('session:main:error', expect.any(Function))
  })

  it('reset limpia el log, las herramientas y el error para un lienzo nuevo', () => {
    const store = useChatStore()
    store.applyEvent({ Message: { Role: 'user', Text: 'hola' } })
    store.applyEvent({ Kind: 'Tool.Called', CallID: 'c1', ToolName: 'echo' })
    store.applyError('algo fallo')

    store.reset()
    // Tras el reset, un resultado de la tool previa no debe reaparecer.
    store.applyEvent({ Kind: 'Tool.Success', CallID: 'c1', Text: 'hi' })

    expect(store.items).toHaveLength(0)
    expect(store.errorText).toBeNull()
  })
})

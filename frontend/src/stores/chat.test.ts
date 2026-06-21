import { describe, it, expect, beforeEach, vi } from 'vitest'
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

describe('chat store: mapeo evento->estado (Text.*)', () => {
  it('coalesce un mensaje de IA a partir de Text.Started/Delta/Ended', () => {
    const store = useChatStore()

    store.applyEvent({ Kind: 'Text.Started' })
    store.applyEvent({ Kind: 'Text.Delta', Text: 'Hola' })
    store.applyEvent({ Kind: 'Text.Delta', Text: ' mundo' })

    // Durante el streaming hay un unico mensaje assistant en curso.
    expect(store.messages).toHaveLength(1)
    expect(store.messages[0]).toMatchObject({
      role: 'assistant',
      text: 'Hola mundo',
      streaming: true,
    })

    store.applyEvent({ Kind: 'Text.Ended', Text: 'Hola mundo' })

    expect(store.messages).toHaveLength(1)
    expect(store.messages[0].streaming).toBe(false)
    expect(store.messages[0].text).toBe('Hola mundo')
  })

  it('promueve el prompt del usuario (Message.Role === "user") al log', () => {
    const store = useChatStore()

    store.applyEvent({ Message: { Role: 'user', Text: 'ping' } })

    expect(store.messages).toHaveLength(1)
    expect(store.messages[0]).toMatchObject({ role: 'user', text: 'ping' })
  })

  it('mantiene usuario e IA en un flujo continuo y ordenado', () => {
    const store = useChatStore()

    store.applyEvent({ Message: { Role: 'user', Text: 'ping' } })
    store.applyEvent({ Kind: 'Text.Started' })
    store.applyEvent({ Kind: 'Text.Delta', Text: 'pong' })
    store.applyEvent({ Kind: 'Text.Ended', Text: 'pong' })

    expect(store.messages.map((m) => m.role)).toEqual(['user', 'assistant'])
    expect(store.messages.map((m) => m.text)).toEqual(['ping', 'pong'])
  })

  it('arranca un mensaje de IA aunque llegue Text.Delta sin Text.Started', () => {
    const store = useChatStore()

    store.applyEvent({ Kind: 'Text.Delta', Text: 'sin started' })

    expect(store.messages).toHaveLength(1)
    expect(store.messages[0]).toMatchObject({ role: 'assistant', text: 'sin started' })
  })

  it('no agrega un assistant Message de Text.Ended como mensaje aparte', () => {
    const store = useChatStore()

    store.applyEvent({ Kind: 'Text.Started' })
    store.applyEvent({ Kind: 'Text.Delta', Text: 'x' })
    store.applyEvent({
      Kind: 'Text.Ended',
      Text: 'x',
      Message: { Role: 'assistant', Text: 'x' },
    })

    expect(store.messages).toHaveLength(1)
    expect(store.messages[0].role).toBe('assistant')
  })

  it('ignora eventos fuera del alcance MVP (Tool.*, Reasoning.*) sin romper', () => {
    const store = useChatStore()

    store.applyEvent({ Kind: 'Tool.Called', ToolName: 'read', CallID: 'c1' })
    store.applyEvent({ Kind: 'Reasoning.Delta', Text: 'pensando' })

    expect(store.messages).toHaveLength(0)
  })
})

describe('chat store: estado de ejecucion', () => {
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

  it('reset limpia el log y el error para un lienzo nuevo', () => {
    const store = useChatStore()
    store.applyEvent({ Message: { Role: 'user', Text: 'hola' } })
    store.applyError('algo fallo')

    store.reset()

    expect(store.messages).toHaveLength(0)
    expect(store.errorText).toBeNull()
  })
})

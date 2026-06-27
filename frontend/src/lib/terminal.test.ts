// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'

const WritePty = vi.fn()
const ClosePty = vi.fn()
const EventsOn = vi.fn()

vi.mock('../../wailsjs/go/main/App', () => ({
  WritePty: (id: string, d: string) => WritePty(id, d),
  ClosePty: (id: string) => ClosePty(id),
}))
vi.mock('../../wailsjs/runtime/runtime', () => ({
  EventsOn: (ev: string, cb: (...a: unknown[]) => void) => EventsOn(ev, cb),
}))

import { b64ToBytes, connectTerminal } from './terminal'

beforeEach(() => {
  WritePty.mockReset()
  ClosePty.mockReset()
  EventsOn.mockReset()
})

describe('b64ToBytes', () => {
  it('decodifica base64 a bytes', () => {
    // "hi" = [104, 105]; Wails serializa el []byte del pty como base64.
    expect(Array.from(b64ToBytes('aGk='))).toEqual([104, 105])
  })
})

describe('connectTerminal', () => {
  it('se suscribe al canal de SU sesion y decodifica la salida', () => {
    let dataCb: (b64: string) => void = () => {}
    EventsOn.mockImplementation((_ev, cb) => {
      dataCb = cb
      return () => {}
    })
    const term = { write: vi.fn(), onData: vi.fn() }
    connectTerminal('t1', term)
    expect(EventsOn).toHaveBeenCalledWith('pty:data:t1', expect.any(Function))
    dataCb('aGk=')
    expect(term.write).toHaveBeenCalledTimes(1)
    expect(Array.from(term.write.mock.calls[0][0])).toEqual([104, 105])
  })

  it('manda el input del term a WritePty con su id', () => {
    EventsOn.mockReturnValue(() => {})
    let inputCb: (d: string) => void = () => {}
    const term = {
      write: vi.fn(),
      onData: vi.fn((cb) => {
        inputCb = cb
      }),
    }
    connectTerminal('t2', term)
    inputCb('ls\n')
    expect(WritePty).toHaveBeenCalledWith('t2', 'ls\n')
  })

  it('dispose desuscribe del evento y cierra el pty de su id', () => {
    const off = vi.fn()
    EventsOn.mockReturnValue(off)
    const term = { write: vi.fn(), onData: vi.fn() }
    const dispose = connectTerminal('t3', term)
    dispose()
    expect(off).toHaveBeenCalled()
    expect(ClosePty).toHaveBeenCalledWith('t3')
  })
})

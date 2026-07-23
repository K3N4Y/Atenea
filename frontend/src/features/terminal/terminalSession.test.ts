// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import type { ITerminalOptions } from '@xterm/xterm'

const StartPty = vi.fn()
const ResizePty = vi.fn()
const ClosePty = vi.fn()
const connectTerminal = vi.fn()

const fitFit = vi.fn()
// Captura las opciones con las que se construye el xterm para poder afirmar el
// tema/fuente sin montar un terminal real (no corre headless).
let captured: ITerminalOptions = {}
class FakeTerminal {
  cols = 80
  rows = 24
  loadAddon = vi.fn()
  open = vi.fn()
  focus = vi.fn()
  refresh = vi.fn()
  dispose = vi.fn()
  constructor(opts: ITerminalOptions) {
    captured = opts
  }
}
class FakeFitAddon {
  fit = fitFit
}

vi.mock('@xterm/xterm', () => ({ Terminal: FakeTerminal }))
vi.mock('@xterm/addon-fit', () => ({ FitAddon: FakeFitAddon }))
vi.mock('../../../wailsjs/go/main/App', () => ({
  StartPty: (id: string, c: number, r: number) => StartPty(id, c, r),
  ResizePty: (id: string, c: number, r: number) => ResizePty(id, c, r),
  ClosePty: (id: string) => ClosePty(id),
}))
vi.mock('./terminal', () => ({
  connectTerminal: (id: string, t: unknown) => connectTerminal(id, t),
}))

beforeEach(() => {
  vi.resetModules() // registro fresco por test
  StartPty.mockReset()
  ResizePty.mockReset()
  ClosePty.mockReset()
  connectTerminal.mockReset()
  fitFit.mockReset()
  captured = {}
})

describe('terminalSession (persistencia + multi-sesion)', () => {
  it('arranca el pty una sola vez por id aunque se re-monte', async () => {
    const { attach, detach } = await import('./terminalSession')

    await attach('a', document.createElement('div'))
    expect(StartPty).toHaveBeenCalledWith('a', 80, 24)
    expect(StartPty).toHaveBeenCalledTimes(1)
    expect(connectTerminal).toHaveBeenCalledWith('a', expect.anything())

    detach('a') // desmonta: NO cierra ni reinicia
    await attach('a', document.createElement('div')) // re-monta

    expect(StartPty).toHaveBeenCalledTimes(1) // misma sesion, no reinicia
    expect(ResizePty).toHaveBeenCalledWith('a', 80, 24)
    expect(ClosePty).not.toHaveBeenCalled()
  })

  it('cada id es una sesion pty independiente', async () => {
    const { attach } = await import('./terminalSession')
    await attach('a', document.createElement('div'))
    await attach('b', document.createElement('div'))
    expect(StartPty).toHaveBeenCalledWith('a', 80, 24)
    expect(StartPty).toHaveBeenCalledWith('b', 80, 24)
    expect(StartPty).toHaveBeenCalledTimes(2)
  })

  it('destroy cierra el pty de ese id (cerrar la tab)', async () => {
    const { attach, destroy } = await import('./terminalSession')
    await attach('a', document.createElement('div'))
    destroy('a')
    expect(ClosePty).toHaveBeenCalledWith('a')
  })
})

describe('terminalSession (tema visual)', () => {
  it('crea el xterm con el tema papel del proyecto y Red Hat Mono', async () => {
    const { attach } = await import('./terminalSession')
    await attach('a', document.createElement('div'))

    expect(captured.theme?.background).toBe('#fef9ed')
    expect(captured.theme?.foreground).toBe('#1c1c1a')
    expect(captured.theme?.cursor).toBe('#f97316')
    expect(captured.fontFamily).toContain('Red Hat Mono')
  })

  it('lleva seleccion con tinte de acento y paleta ANSI completa', async () => {
    const { attach } = await import('./terminalSession')
    await attach('a', document.createElement('div'))

    expect(captured.theme?.selectionBackground).toBe('rgba(249, 115, 22, 0.22)')
    // Paleta ANSI presente (afinada para fondo claro), no la default de xterm.
    expect(captured.theme?.red).toBeDefined()
    expect(captured.theme?.brightBlue).toBeDefined()
  })

  it('no regresiona otras opciones del constructor (fontSize/cursorBlink)', async () => {
    const { attach } = await import('./terminalSession')
    await attach('a', document.createElement('div'))

    expect(captured.fontSize).toBe(13)
    expect(captured.cursorBlink).toBe(true)
  })

  it('active es el theme papel con sus hex base (datos puros)', async () => {
    const { active, paper } = await import('./terminalThemes')
    expect(active).toBe(paper)
    expect(active.background).toBe('#fef9ed')
    expect(active.foreground).toBe('#1c1c1a')
    expect(active.cursor).toBe('#f97316')
  })
})

// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

const ListMCPs = vi.fn()
const ConnectMCP = vi.fn()
const DisconnectMCP = vi.fn()

vi.mock('../../wailsjs/go/main/App', () => ({
  ListMCPs: (...a: unknown[]) => ListMCPs(...a),
  ConnectMCP: (...a: unknown[]) => ConnectMCP(...a),
  DisconnectMCP: (...a: unknown[]) => DisconnectMCP(...a),
}))

import { useMcpStore } from './mcp'
import { useChatStore } from './chat'

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  ListMCPs.mockResolvedValue([])
  ConnectMCP.mockResolvedValue(undefined)
  DisconnectMCP.mockResolvedValue(undefined)
})

describe('mcp store', () => {
  it('refresh trae el estado conectado del backend', async () => {
    ListMCPs.mockResolvedValue([
      {
        name: 'github',
        command: 'npx',
        args: ['-y'],
        connected: true,
        tools: 4,
      },
    ])
    const mcp = useMcpStore()
    await mcp.refresh()
    expect(mcp.connected).toHaveLength(1)
    expect(mcp.connected[0].name).toBe('github')
  })

  it('servers une configs persistidas con el estado conectado del backend', async () => {
    const chat = useChatStore()
    chat.saveMCPServer({ name: 'github', command: 'npx', args: ['-y'] })
    ListMCPs.mockResolvedValue([
      {
        name: 'github',
        command: 'npx',
        args: ['-y'],
        connected: true,
        tools: 4,
      },
    ])
    const mcp = useMcpStore()
    await mcp.refresh()

    expect(mcp.servers).toHaveLength(1)
    expect(mcp.servers[0]).toMatchObject({
      name: 'github',
      connected: true,
      tools: 4,
    })
  })

  it('marca como desconectado un server configurado que el backend no reporta', async () => {
    const chat = useChatStore()
    chat.saveMCPServer({ name: 'github', command: 'npx', args: ['-y'] })
    ListMCPs.mockResolvedValue([])
    const mcp = useMcpStore()
    await mcp.refresh()

    expect(mcp.servers).toHaveLength(1)
    expect(mcp.servers[0].connected).toBe(false)
    expect(mcp.servers[0].tools).toBe(0)
  })

  it('toggle conecta un server desconectado', async () => {
    const chat = useChatStore()
    chat.saveMCPServer({ name: 'github', command: 'npx', args: ['-y'] })
    ListMCPs.mockResolvedValue([])
    const mcp = useMcpStore()
    await mcp.refresh()

    await mcp.toggle('github')

    expect(ConnectMCP).toHaveBeenCalledWith({
      name: 'github',
      command: 'npx',
      args: ['-y'],
    })
    expect(DisconnectMCP).not.toHaveBeenCalled()
  })

  it('toggle desconecta un server conectado', async () => {
    const chat = useChatStore()
    chat.saveMCPServer({ name: 'github', command: 'npx', args: ['-y'] })
    ListMCPs.mockResolvedValue([
      {
        name: 'github',
        command: 'npx',
        args: ['-y'],
        connected: true,
        tools: 1,
      },
    ])
    const mcp = useMcpStore()
    await mcp.refresh()

    await mcp.toggle('github')

    expect(DisconnectMCP).toHaveBeenCalledWith('github')
    expect(ConnectMCP).not.toHaveBeenCalled()
  })

  it('toggle ignora pedidos cruzados mientras una accion esta en vuelo', async () => {
    const chat = useChatStore()
    chat.saveMCPServer({ name: 'github', command: 'npx', args: ['-y'] })
    // Connect cuelga para que pending quede a true durante el segundo toggle.
    let resolveConnect: () => void
    ConnectMCP.mockReturnValue(
      new Promise<void>((resolve) => {
        resolveConnect = resolve
      }),
    )
    ListMCPs.mockResolvedValue([])
    const mcp = useMcpStore()
    await mcp.refresh()

    const first = mcp.toggle('github')
    const second = mcp.toggle('github') // se ignora: ya hay uno en vuelo
    await second
    resolveConnect!()
    await first

    // Connect se llama una sola vez: el segundo toggle se descarto.
    expect(ConnectMCP).toHaveBeenCalledTimes(1)
  })

  it('isPending refleja si una accion esta en vuelo', async () => {
    const chat = useChatStore()
    chat.saveMCPServer({ name: 'github', command: 'npx', args: ['-y'] })
    let resolveConnect: () => void
    ConnectMCP.mockReturnValue(
      new Promise<void>((resolve) => {
        resolveConnect = resolve
      }),
    )
    ListMCPs.mockResolvedValue([])
    const mcp = useMcpStore()
    await mcp.refresh()

    const pending = mcp.toggle('github')
    expect(mcp.isPending('github')).toBe(true)
    resolveConnect!()
    await pending
    expect(mcp.isPending('github')).toBe(false)
  })

  it('connect captura el error del backend en el store', async () => {
    const chat = useChatStore()
    chat.saveMCPServer({ name: 'github', command: 'npx', args: ['-y'] })
    ConnectMCP.mockRejectedValue(new Error('boom'))
    const mcp = useMcpStore()
    const ok = await mcp.connect('github')
    expect(ok).toBe(false)
    expect(mcp.error).toBe('boom')
  })

  it('connect devuelve true cuando el backend confirma', async () => {
    const chat = useChatStore()
    chat.saveMCPServer({ name: 'github', command: 'npx', args: ['-y'] })
    ListMCPs.mockResolvedValue([
      {
        name: 'github',
        command: 'npx',
        args: ['-y'],
        connected: true,
        tools: 2,
      },
    ])
    const mcp = useMcpStore()
    const ok = await mcp.connect('github')
    expect(ok).toBe(true)
  })

  it('toggle voltea el switch al instante (optimista)', async () => {
    const chat = useChatStore()
    chat.saveMCPServer({ name: 'github', command: 'npx', args: ['-y'] })
    // Connect cuelga para atrapar el estado DURANTE la accion: el override ya
    // aplica aunque el backend todavia no confirmo.
    let resolveConnect: () => void
    ConnectMCP.mockReturnValue(
      new Promise<void>((resolve) => {
        resolveConnect = resolve
      }),
    )
    ListMCPs.mockResolvedValue([])
    const mcp = useMcpStore()
    await mcp.refresh()

    const pending = mcp.toggle('github')
    // Optimista: conectado al instante, sin esperar al backend.
    expect(mcp.servers[0].connected).toBe(true)

    resolveConnect!()
    await pending
  })

  it('toggle revierte el switch si la conexion falla', async () => {
    const chat = useChatStore()
    chat.saveMCPServer({ name: 'github', command: 'npx', args: ['-y'] })
    ConnectMCP.mockRejectedValue(new Error('boom'))
    ListMCPs.mockResolvedValue([])
    const mcp = useMcpStore()
    await mcp.refresh()
    expect(mcp.servers[0].connected).toBe(false)

    await mcp.toggle('github')

    // Revertido al estado real (desconectado) y el error queda a la vista.
    expect(mcp.servers[0].connected).toBe(false)
    expect(mcp.error).toBe('boom')
  })

  it('toggle revierte el switch si la desconexion falla', async () => {
    const chat = useChatStore()
    chat.saveMCPServer({ name: 'github', command: 'npx', args: ['-y'] })
    DisconnectMCP.mockRejectedValue(new Error('boom'))
    ListMCPs.mockResolvedValue([
      {
        name: 'github',
        command: 'npx',
        args: ['-y'],
        connected: true,
        tools: 1,
      },
    ])
    const mcp = useMcpStore()
    await mcp.refresh()
    expect(mcp.servers[0].connected).toBe(true)

    await mcp.toggle('github')

    // Revertido al estado real (conectado) y el error queda a la vista.
    expect(mcp.servers[0].connected).toBe(true)
    expect(mcp.error).toBe('boom')
  })
})

// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

const ListMCPs = vi.fn()
const ConnectMCP = vi.fn()
const DisconnectMCP = vi.fn()
const SaveMCPConfig = vi.fn()

vi.mock('../../../wailsjs/go/main/App', () => ({
  ListMCPs: (...a: unknown[]) => ListMCPs(...a),
  ConnectMCP: (...a: unknown[]) => ConnectMCP(...a),
  DisconnectMCP: (...a: unknown[]) => DisconnectMCP(...a),
  SaveMCPConfig: (...a: unknown[]) => SaveMCPConfig(...a),
}))

import { useMcpStore } from './mcp'

beforeEach(() => {
  setActivePinia(createPinia())
  localStorage.clear()
  vi.clearAllMocks()
  ListMCPs.mockResolvedValue([])
  ConnectMCP.mockResolvedValue(undefined)
  DisconnectMCP.mockResolvedValue(undefined)
  SaveMCPConfig.mockResolvedValue(undefined)
})

// declared arma la respuesta de ListMCPs para un server declarado en la
// config del backend, conectado o no.
function declared(name: string, connected = false, tools = 0) {
  return { name, command: 'npx', args: ['-y'], connected, tools }
}

describe('mcp store', () => {
  it('refresh trae la lista declarada del backend con su estado', async () => {
    ListMCPs.mockResolvedValue([declared('github', true, 4)])
    const mcp = useMcpStore()
    await mcp.refresh()
    expect(mcp.servers).toHaveLength(1)
    expect(mcp.servers[0]).toMatchObject({
      name: 'github',
      connected: true,
      tools: 4,
    })
  })

  it('normaliza args nulos del backend a lista vacia', async () => {
    ListMCPs.mockResolvedValue([
      {
        name: 'github',
        command: 'npx',
        args: null,
        connected: false,
        tools: 0,
      },
    ])
    const mcp = useMcpStore()
    await mcp.refresh()
    expect(mcp.servers[0].args).toEqual([])
  })

  it('migra las configs legadas de localStorage a la config global', async () => {
    localStorage.setItem(
      'chat',
      JSON.stringify({
        workspace: '/home/u/project',
        mcpServers: [
          { name: 'github', command: 'npx', args: ['-y'] },
          { name: 'sentry', command: 'uvx', args: [] },
        ],
      }),
    )
    const mcp = useMcpStore()
    await mcp.refresh()

    expect(SaveMCPConfig).toHaveBeenCalledTimes(2)
    expect(SaveMCPConfig).toHaveBeenCalledWith({
      name: 'github',
      command: 'npx',
      args: ['-y'],
    })
    expect(JSON.parse(localStorage.getItem('chat') as string)).toEqual({
      workspace: '/home/u/project',
    })
    expect(ListMCPs).toHaveBeenCalled()
  })

  it('conserva las configs legadas si la migracion falla, para reintentar', async () => {
    const legacy = {
      workspace: '/home/u/project',
      mcpServers: [{ name: 'github', command: 'npx', args: ['-y'] }],
    }
    localStorage.setItem('chat', JSON.stringify(legacy))
    SaveMCPConfig.mockRejectedValue(new Error('backend down'))
    const mcp = useMcpStore()
    await mcp.refresh()

    expect(JSON.parse(localStorage.getItem('chat') as string)).toEqual(legacy)
    expect(mcp.error).toBe('backend down')
  })

  it('toggle conecta un server desconectado', async () => {
    ListMCPs.mockResolvedValue([declared('github')])
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
    ListMCPs.mockResolvedValue([declared('github', true, 1)])
    const mcp = useMcpStore()
    await mcp.refresh()

    await mcp.toggle('github')

    expect(DisconnectMCP).toHaveBeenCalledWith('github')
    expect(ConnectMCP).not.toHaveBeenCalled()
  })

  it('toggle ignora pedidos cruzados mientras una accion esta en vuelo', async () => {
    // Connect cuelga para que pending quede a true durante el segundo toggle.
    let resolveConnect: () => void
    ConnectMCP.mockReturnValue(
      new Promise<void>((resolve) => {
        resolveConnect = resolve
      }),
    )
    ListMCPs.mockResolvedValue([declared('github')])
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
    let resolveConnect: () => void
    ConnectMCP.mockReturnValue(
      new Promise<void>((resolve) => {
        resolveConnect = resolve
      }),
    )
    ListMCPs.mockResolvedValue([declared('github')])
    const mcp = useMcpStore()
    await mcp.refresh()

    const pending = mcp.toggle('github')
    expect(mcp.isPending('github')).toBe(true)
    resolveConnect!()
    await pending
    expect(mcp.isPending('github')).toBe(false)
  })

  it('connect captura el error del backend en el store', async () => {
    ListMCPs.mockResolvedValue([declared('github')])
    ConnectMCP.mockRejectedValue(new Error('boom'))
    const mcp = useMcpStore()
    await mcp.refresh()
    const ok = await mcp.connect('github')
    expect(ok).toBe(false)
    expect(mcp.error).toBe('boom')
  })

  it('connect devuelve true cuando el backend confirma', async () => {
    ListMCPs.mockResolvedValue([declared('github', true, 2)])
    const mcp = useMcpStore()
    await mcp.refresh()
    const ok = await mcp.connect('github')
    expect(ok).toBe(true)
  })

  it('toggle voltea el switch al instante (optimista)', async () => {
    // Connect cuelga para atrapar el estado DURANTE la accion: el override ya
    // aplica aunque el backend todavia no confirmo.
    let resolveConnect: () => void
    ConnectMCP.mockReturnValue(
      new Promise<void>((resolve) => {
        resolveConnect = resolve
      }),
    )
    ListMCPs.mockResolvedValue([declared('github')])
    const mcp = useMcpStore()
    await mcp.refresh()

    const pending = mcp.toggle('github')
    // Optimista: conectado al instante, sin esperar al backend.
    expect(mcp.servers[0].connected).toBe(true)

    resolveConnect!()
    await pending
  })

  it('toggle revierte el switch si la conexion falla', async () => {
    ConnectMCP.mockRejectedValue(new Error('boom'))
    ListMCPs.mockResolvedValue([declared('github')])
    const mcp = useMcpStore()
    await mcp.refresh()
    expect(mcp.servers[0].connected).toBe(false)

    await mcp.toggle('github')

    // Revertido al estado real (desconectado) y el error queda a la vista.
    expect(mcp.servers[0].connected).toBe(false)
    expect(mcp.error).toBe('boom')
  })

  it('toggle revierte el switch si la desconexion falla', async () => {
    DisconnectMCP.mockRejectedValue(new Error('boom'))
    ListMCPs.mockResolvedValue([declared('github', true, 1)])
    const mcp = useMcpStore()
    await mcp.refresh()
    expect(mcp.servers[0].connected).toBe(true)

    await mcp.toggle('github')

    // Revertido al estado real (conectado) y el error queda a la vista.
    expect(mcp.servers[0].connected).toBe(true)
    expect(mcp.error).toBe('boom')
  })
})

import { ref, computed } from 'vue'
import { defineStore } from 'pinia'
import { ConnectMCP, DisconnectMCP, ListMCPs } from '../../wailsjs/go/main/App'
import { useChatStore } from './chat'

// Estado de los servidores MCP: las definiciones durables (no secretas) viven
// en el store de chat y se persisten; aqui llevamos el estado de conexion
// efimero (lo devuelve el backend con ListMCPs) y las acciones que hablan con
// el proceso real (Connect/Disconnect). Una conexion NO se persiste: reabrir
// Atenea no debe ejecutar un comando arbitrario sin accion explicita.
//
// `servers` unifica ambos mundos: cada config duradera mas su estado conectado
// actual. Es lo que consumen tanto el menu de la navbar (switches on/off) como
// la pestaña MCPs de Settings.
export interface MCPServer {
  name: string
  command: string
  args: string[]
  connected: boolean
  tools: number
}

export const useMcpStore = defineStore('mcp', () => {
  const chat = useChatStore()

  // Estado conectado efimero: lo trae el backend (ListMCPs). Fuera de linea o
  // sin conectar, es la fuente de verdad de `connected`/`tools`.
  const connected = ref<MCPServer[]>([])
  // Override optimista: nombre -> estado conectado que el usuario acabo de
  // pedir pero el backend todavia no confirmo. toggle() lo aplica al instante
  // para que el switch se voltee sin esperar; si la accion falla se limpia y el
  // estado vuelve al real (refresh). Es un Map (no reactivo por item): lo
  // reemplazamos en cada cambio para que Vue lo note.
  const optimistic = ref(new Map<string, boolean>())
  // Nombres con una accion en vuelo (connect/disconnect): para que el switch
  // muestre estado de carga y no se re_TOGGLEE mientras la promesa resuelve.
  const pending = ref(new Set<string>())
  const error = ref('')

  // servers: configs persistidas unidas al estado conectado del backend, con
  // el override optimista por encima cuando existe. Primero las configuradas
  // (orden estable del usuario), luego las conectadas sin config (agregadas
  // desde otra ventana) para no ocultarlas.
  const servers = computed<MCPServer[]>(() => {
    const connectedByName = new Map(
      connected.value.map((server) => [server.name, server]),
    )
    const configuredNames = new Set(
      chat.mcpServers.map((server) => server.name),
    )
    const configuredServers = chat.mcpServers.map((config) => {
      const status = connectedByName.get(config.name)
      const optimisticConnected = optimistic.value.get(config.name)
      return {
        name: config.name,
        command: config.command,
        args: config.args,
        connected: optimisticConnected ?? status?.connected ?? false,
        tools: status?.tools ?? 0,
      }
    })
    return [
      ...configuredServers,
      ...connected.value.filter((server) => !configuredNames.has(server.name)),
    ]
  })

  async function refresh() {
    try {
      connected.value = await ListMCPs()
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
    }
  }

  // connect/disconnect son la capa contra el backend: no tocan el override
  // optimista (eso lo hace toggle). Refrescan al confirmar para que el estado
  // real reemplace al optimista sin parpadeo (coinciden). Devuelven true si la
  // accion confirmo, false si fallo (y dejan el error en `error`); no lanzan,
  // asi los consumidores directos (Settings) pueden llamarlos sin try/catch y
  // toggle puede revertir el optimismo segun el resultado.
  async function connect(name: string): Promise<boolean> {
    error.value = ''
    pending.value.add(name)
    pending.value = new Set(pending.value)
    try {
      const config = chat.mcpServers.find((server) => server.name === name)
      if (!config) throw new Error(`No MCP server named "${name}"`)
      await ConnectMCP({
        name: config.name,
        command: config.command,
        args: config.args,
      })
      await refresh()
      return true
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
      return false
    } finally {
      pending.value.delete(name)
      pending.value = new Set(pending.value)
    }
  }

  async function disconnect(name: string): Promise<boolean> {
    error.value = ''
    pending.value.add(name)
    pending.value = new Set(pending.value)
    try {
      await DisconnectMCP(name)
      await refresh()
      return true
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
      return false
    } finally {
      pending.value.delete(name)
      pending.value = new Set(pending.value)
    }
  }

  // toggle es el switch de la navbar: optimista. Voltea el estado al instante
  // y dispara la accion contra el backend. Si esta falla, revierte el override
  // y el switch vuelve a su estado real (deja el error en `error` para la UI).
  // No togglea si ya hay una accion en vuelo (evita pedidos cruzados).
  async function toggle(name: string) {
    if (pending.value.has(name)) return
    const next = !servers.value.find((s) => s.name === name)?.connected
    optimistic.value.set(name, next)
    optimistic.value = new Map(optimistic.value)
    const ok = next ? await connect(name) : await disconnect(name)
    if (!ok) {
      // Revert: descarta el override. No hace falta refrescar: el backend no
      // cambio el estado real, y refresh ya corrio (y fallo) dentro de
      // connect/disconnect; `connected` sigue siendo la verdad anterior.
      optimistic.value.delete(name)
      optimistic.value = new Map(optimistic.value)
    }
  }

  // isPending es helper reactivo para la UI (un Set no es reactivo por item;
  // lo reemplazamos arriba, pero leerlo por nombre va por aca).
  function isPending(name: string): boolean {
    return pending.value.has(name)
  }

  return {
    connected,
    servers,
    pending,
    error,
    refresh,
    connect,
    disconnect,
    toggle,
    isPending,
  }
})

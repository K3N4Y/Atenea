import { ref, computed } from 'vue'
import { defineStore } from 'pinia'
import {
  ConnectMCP,
  DisconnectMCP,
  ListMCPs,
  SaveMCPConfig,
} from '../../wailsjs/go/main/App'
import { useChatStore } from './chat'

// El backend es la fuente de verdad de los servidores MCP: las definiciones
// viven en la config global (~/.config/atenea/mcp.json) mas el .mcp.json del
// workspace, y ListMCPs las devuelve ya mezcladas con el estado de conexion.
// Asi la app de escritorio y la TUI comparten la misma lista. Este store lleva
// esa lista, el override optimista de los switches y las acciones contra el
// proceso real (Connect/Disconnect). Una conexion NO se persiste: reabrir
// Atenea no debe ejecutar un comando arbitrario sin accion explicita.
//
// `servers` es lo que consumen tanto el menu de la navbar (switches on/off)
// como la pestaña MCPs de Settings.
export interface MCPServer {
  name: string
  command: string
  args: string[]
  env?: Record<string, string>
  cwd?: string
  connected: boolean
  tools: number
}

export const useMcpStore = defineStore('mcp', () => {
  const chat = useChatStore()

  // Lista declarada + estado conectado, tal como la devuelve el backend.
  const list = ref<MCPServer[]>([])
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

  const servers = computed<MCPServer[]>(() =>
    list.value.map((server) => ({
      ...server,
      args: server.args ?? [],
      connected: optimistic.value.get(server.name) ?? server.connected,
    })),
  )

  // Migracion one-shot: las configs MCP vivian en localStorage (store de
  // chat); ahora la fuente de verdad es la config global del backend. Si
  // quedan configs viejas se suben con SaveMCPConfig y se vacia el legado;
  // si el backend falla se conservan para reintentar en el proximo refresh.
  async function migrateLegacyConfigs() {
    if (chat.mcpServers.length === 0) return
    for (const config of chat.mcpServers) await SaveMCPConfig(config)
    chat.mcpServers = []
  }

  async function refresh() {
    try {
      await migrateLegacyConfigs()
      list.value = await ListMCPs()
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
      const config = list.value.find((server) => server.name === name)
      if (!config) throw new Error(`No MCP server named "${name}"`)
      await ConnectMCP({
        name: config.name,
        command: config.command,
        args: config.args ?? [],
        env: config.env,
        cwd: config.cwd,
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
      // connect/disconnect; `list` sigue siendo la verdad anterior.
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
    list,
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

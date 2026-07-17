<script lang="ts" setup>
import { computed, ref, onMounted, onUnmounted } from 'vue'
import { PhX, PhGear, PhPlugs, PhSparkle } from '@phosphor-icons/vue'
import { ConnectMCP, DisconnectMCP, ListMCPs } from '../../wailsjs/go/main/App'
import ProviderSettings from './ProviderSettings.vue'
import { useChatStore } from '../stores/chat'

const chat = useChatStore()
const emit = defineEmits<{ close: [] }>()

type TabId = 'general' | 'mcps' | 'skills'
const tabs = [
  { id: 'general', label: 'General', icon: PhGear },
  { id: 'mcps', label: 'MCPs', icon: PhPlugs },
  { id: 'skills', label: 'Skills', icon: PhSparkle },
] as const

interface MCPServer {
  name: string
  command: string
  args: string[]
  connected: boolean
  tools: number
}

const active = ref<TabId>('general')
const connectedServers = ref<MCPServer[]>([])
const servers = computed<MCPServer[]>(() => {
  const connectedByName = new Map(
    connectedServers.value.map((server) => [server.name, server]),
  )
  const configuredNames = new Set(chat.mcpServers.map((server) => server.name))
  const configuredServers = chat.mcpServers.map((config) => {
    const connected = connectedByName.get(config.name)
    return {
      ...config,
      connected: connected?.connected ?? false,
      tools: connected?.tools ?? 0,
    }
  })

  return [
    ...configuredServers,
    ...connectedServers.value.filter(
      (server) => !configuredNames.has(server.name),
    ),
  ]
})
const serverName = ref('')
const command = ref('')
const args = ref('')
const mcpError = ref('')
const connecting = ref(false)

async function refreshMCPs() {
  connectedServers.value = await ListMCPs()
}

async function connectMCP() {
  mcpError.value = ''
  connecting.value = true
  try {
    const config = {
      name: serverName.value.trim(),
      command: command.value.trim(),
      args: args.value
        .split('\n')
        .map((arg) => arg.trim())
        .filter(Boolean),
    }
    await ConnectMCP(config)
    chat.saveMCPServer(config)
    serverName.value = ''
    command.value = ''
    args.value = ''
    await refreshMCPs()
  } catch (error) {
    mcpError.value = error instanceof Error ? error.message : String(error)
  } finally {
    connecting.value = false
  }
}

async function disconnectMCP(name: string) {
  mcpError.value = ''
  try {
    await DisconnectMCP(name)
    await refreshMCPs()
  } catch (error) {
    mcpError.value = error instanceof Error ? error.message : String(error)
  }
}

async function reconnectMCP(server: MCPServer) {
  mcpError.value = ''
  connecting.value = true
  try {
    await ConnectMCP({
      name: server.name,
      command: server.command,
      args: server.args,
    })
    await refreshMCPs()
  } catch (error) {
    mcpError.value = error instanceof Error ? error.message : String(error)
  } finally {
    connecting.value = false
  }
}

async function removeMCP(server: MCPServer) {
  mcpError.value = ''
  try {
    if (server.connected) await DisconnectMCP(server.name)
    chat.removeMCPServer(server.name)
    await refreshMCPs()
  } catch (error) {
    mcpError.value = error instanceof Error ? error.message : String(error)
  }
}

function selectTab(id: TabId) {
  active.value = id
  if (id === 'mcps') void refreshMCPs()
}

function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') emit('close')
}

onMounted(() => {
  window.addEventListener('keydown', onKeydown)
  void refreshMCPs()
})
onUnmounted(() => window.removeEventListener('keydown', onKeydown))
</script>

<template>
  <div
    role="dialog"
    aria-modal="true"
    aria-label="Configuracion"
    class="fixed inset-0 z-40 flex bg-paper"
  >
    <nav
      role="tablist"
      aria-label="Configuracion"
      class="flex w-56 shrink-0 flex-col gap-1 border-r border-black/5 bg-black/[0.015] p-3"
    >
      <p class="px-2 py-3 text-lg tracking-tight">Configuracion</p>
      <button
        v-for="tab in tabs"
        :key="tab.id"
        type="button"
        role="tab"
        :aria-selected="active === tab.id ? 'true' : 'false'"
        class="flex items-center gap-2 rounded-full px-4 py-2.5 text-left text-sm transition active:scale-[0.97]"
        :class="
          active === tab.id
            ? 'bg-black/[0.06] font-medium'
            : 'hover:bg-black/[0.04]'
        "
        @click="selectTab(tab.id)"
      >
        <component :is="tab.icon" :size="18" weight="regular" />
        {{ tab.label }}
      </button>
    </nav>

    <section class="relative flex min-w-0 flex-1 flex-col overflow-y-auto">
      <button
        type="button"
        aria-label="Cerrar configuracion"
        class="absolute right-4 top-4 flex h-9 w-9 items-center justify-center rounded-full transition hover:bg-black/[0.05] active:scale-95"
        @click="emit('close')"
      >
        <PhX :size="20" weight="regular" />
      </button>

      <div class="mx-auto w-full max-w-3xl px-8 py-10">
        <template v-if="active === 'general'">
          <ProviderSettings
            :providerKind="chat.providerKind"
            :baseURL="chat.baseURL"
            :model="chat.model"
            :availableModels="chat.availableModels"
            @apply="(k, b, m) => chat.setProvider(k, b, m)"
            @list-models="(b) => chat.listModels(b)"
          />
        </template>

        <template v-else-if="active === 'mcps'">
          <h2 class="text-lg tracking-tight">MCP servers</h2>
          <p class="mt-1 text-sm opacity-50">
            Configurations stay on this device. Servers run only after you
            explicitly connect them.
          </p>
          <form
            class="mt-6 grid gap-4 rounded-soft border border-black/5 bg-black/[0.02] p-5"
            @submit.prevent="connectMCP"
          >
            <label class="grid gap-1.5 text-sm">
              Name
              <input
                v-model="serverName"
                data-mcp-name
                required
                pattern="[A-Za-z0-9_-]{1,48}"
                class="rounded-lg border border-black/10 bg-paper px-3 py-2"
                placeholder="github"
              />
            </label>
            <label class="grid gap-1.5 text-sm">
              Command
              <input
                v-model="command"
                data-mcp-command
                required
                class="rounded-lg border border-black/10 bg-paper px-3 py-2 font-mono text-xs"
                placeholder="npx"
              />
            </label>
            <label class="grid gap-1.5 text-sm">
              Arguments <span class="opacity-50">(one per line)</span>
              <textarea
                v-model="args"
                data-mcp-args
                rows="3"
                class="rounded-lg border border-black/10 bg-paper px-3 py-2 font-mono text-xs"
                placeholder="-y&#10;@modelcontextprotocol/server-github"
              />
            </label>
            <p v-if="mcpError" role="alert" class="text-sm text-red-700">
              {{ mcpError }}
            </p>
            <button
              data-connect-mcp
              type="submit"
              :disabled="connecting"
              class="w-fit rounded-full bg-ink px-4 py-2 text-sm text-paper transition disabled:opacity-50"
            >
              {{ connecting ? 'Connecting…' : 'Connect server' }}
            </button>
          </form>
          <div class="mt-6 grid gap-3">
            <p v-if="servers.length === 0" class="text-sm opacity-50">
              No MCP servers configured.
            </p>
            <article
              v-for="server in servers"
              :key="server.name"
              class="flex items-center justify-between gap-4 rounded-soft border border-black/5 bg-black/[0.02] p-4"
            >
              <div class="min-w-0">
                <h3 class="font-medium">{{ server.name }}</h3>
                <p class="mt-1 truncate font-mono text-xs opacity-60">
                  {{ [server.command, ...server.args].join(' ') }}
                </p>
                <p
                  :data-mcp-status="server.name"
                  class="mt-2 text-xs font-medium"
                  :class="
                    server.connected ? 'text-emerald-700' : 'text-stone-500'
                  "
                >
                  <template v-if="server.connected">
                    Connected · {{ server.tools }} tools available
                  </template>
                  <template v-else> Disconnected </template>
                </p>
              </div>
              <div class="flex shrink-0 gap-2">
                <button
                  v-if="server.connected"
                  type="button"
                  :data-disconnect-mcp="server.name"
                  class="rounded-full border border-black/10 px-3 py-1.5 text-sm transition hover:bg-black/[0.05]"
                  @click="disconnectMCP(server.name)"
                >
                  Disconnect
                </button>
                <button
                  v-else
                  type="button"
                  :data-reconnect-mcp="server.name"
                  :disabled="connecting"
                  class="rounded-full bg-ink px-3 py-1.5 text-sm text-paper transition disabled:opacity-50"
                  @click="reconnectMCP(server)"
                >
                  Connect
                </button>
                <button
                  type="button"
                  :data-remove-mcp="server.name"
                  class="rounded-full border border-black/10 px-3 py-1.5 text-sm transition hover:bg-black/[0.05]"
                  @click="removeMCP(server)"
                >
                  Remove
                </button>
              </div>
            </article>
          </div>
        </template>

        <template v-else>
          <h2 class="text-lg tracking-tight">Skills</h2>
          <p class="mt-3 text-sm opacity-50">Skills coming soon.</p>
        </template>
      </div>
    </section>
  </div>
</template>

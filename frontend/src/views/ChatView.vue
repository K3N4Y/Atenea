<script lang="ts" setup>
import { onMounted, onUnmounted } from 'vue'
import { PhSidebarSimple } from '@phosphor-icons/vue'
import AppSidebar from '../components/AppSidebar.vue'
import MessageList from '../components/MessageList.vue'
import ChatComposer from '../components/ChatComposer.vue'
import { useChatStore } from '../stores/chat'
import { useUiStore } from '../stores/ui'

// Vista raiz del chat: arma el layout (sidebar persistente + chat central) y
// conecta el store de chat al canal de la sesion. El mapeo evento->estado vive
// en el store (front.md §74); aqui solo gestionamos el ciclo de vida de la
// suscripcion, que la Fase 1 dejaba sin limpiar.
const chat = useChatStore()
const ui = useUiStore()

onMounted(() => chat.subscribe())
onUnmounted(() => chat.teardown())
</script>

<template>
  <div class="flex h-screen w-screen overflow-hidden">
    <AppSidebar @new-chat="chat.reset()" />

    <!-- Fondo para cerrar la sidebar superpuesta en pantallas estrechas. -->
    <div
      v-if="!ui.sidebarCollapsed"
      aria-hidden="true"
      class="fixed inset-0 z-20 bg-black/20 md:hidden"
      @click="ui.toggleSidebar()"
    ></div>

    <main class="flex min-w-0 flex-1 flex-col">
      <header class="flex items-center px-3 py-3">
        <button
          type="button"
          aria-label="Toggle sidebar"
          aria-controls="app-sidebar"
          :aria-expanded="!ui.sidebarCollapsed"
          class="flex h-9 w-9 items-center justify-center rounded-full transition hover:bg-black/[0.05]"
          @click="ui.toggleSidebar()"
        >
          <PhSidebarSimple :size="20" weight="regular" />
        </button>
      </header>

      <MessageList :items="chat.items" />
      <ChatComposer :running="chat.running" @send="chat.send" @stop="chat.stop" />
    </main>
  </div>
</template>

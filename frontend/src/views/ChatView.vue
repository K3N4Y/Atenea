<script lang="ts" setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { PhSidebarSimple, PhGear } from '@phosphor-icons/vue'
import AppSidebar from '../components/AppSidebar.vue'
import MessageList from '../components/MessageList.vue'
import ErrorNotice from '../components/ErrorNotice.vue'
import ChatComposer from '../components/ChatComposer.vue'
import SettingsPanel from '../components/SettingsPanel.vue'
import { useChatStore } from '../stores/chat'
import { useUiStore } from '../stores/ui'

// Vista raiz del chat: arma el layout (sidebar persistente + chat central) y
// conecta el store de chat al canal de la sesion. El mapeo evento->estado vive
// en el store (front.md §74); aqui solo gestionamos el ciclo de vida de la
// suscripcion, que la Fase 1 dejaba sin limpiar.
const chat = useChatStore()
const ui = useUiStore()

// Settings panel open state: ephemeral UI state of the view (not persisted, so
// it does not reappear on app relaunch).
const settingsOpen = ref(false)

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

        <button
          type="button"
          aria-label="Open settings"
          class="ml-auto flex h-9 w-9 items-center justify-center rounded-full transition hover:bg-black/[0.05]"
          @click="settingsOpen = true"
        >
          <PhGear :size="20" weight="regular" />
        </button>
      </header>

      <MessageList :items="chat.items" @approve="chat.approveTool" @deny="chat.denyTool" />

      <!-- Aviso de error de la sesion (fallo del proveedor o stream cortado).
           Vive sobre el composer, dentro de la columna del chat: visible pero
           sin alarmar, y el usuario lo descarta cuando quiera (identidad §11). -->
      <div v-if="chat.errorText" class="mx-auto w-full max-w-3xl px-6 pt-2">
        <ErrorNotice :message="chat.errorText" @dismiss="chat.clearError" />
      </div>

      <ChatComposer :running="chat.running" @send="chat.send" @stop="chat.stop" />
    </main>

    <SettingsPanel v-if="settingsOpen" @close="settingsOpen = false" />
  </div>
</template>

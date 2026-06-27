<script lang="ts" setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { PhSidebarSimple, PhGear } from '@phosphor-icons/vue'
import AppSidebar from '../components/AppSidebar.vue'
import MessageList from '../components/MessageList.vue'
import ErrorNotice from '../components/ErrorNotice.vue'
import ChatComposer from '../components/ChatComposer.vue'
import SettingsPanel from '../components/SettingsPanel.vue'
import PlanView from '../components/PlanView.vue'
import PlanCard from '../components/PlanCard.vue'
import TodoList from '../components/TodoList.vue'
import ContextUsedBar from '../components/ContextUsedBar.vue'
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

onMounted(() => {
  chat.subscribe()
  // Puebla la sidebar con el historial de chats del backend. La app abre en un
  // chat nuevo vacio (identidad §2, Chat First): NO se auto-carga la ultima
  // sesion; la sidebar es como el usuario vuelve a una conversacion pasada.
  chat.loadSessions()
  // Trae el modelo activo una vez para dimensionar la barra de contexto.
  chat.loadModel()
  // Lista los archivos del workspace una vez para el @-menu del composer.
  chat.loadProjectFiles()
  // Lista los comandos una vez para el slash-menu del composer.
  chat.loadCommands()
})
onUnmounted(() => chat.teardown())
</script>

<template>
  <div class="flex h-screen w-screen overflow-hidden">
    <AppSidebar
      :sessions="chat.sessions"
      :active-session-id="chat.sessionID"
      @new-chat="chat.reset()"
      @select-session="(id: string) => chat.loadSession(id)"
      @delete-session="(id: string) => chat.deleteSession(id)"
    />

    <!-- Fondo para cerrar la sidebar superpuesta en pantallas estrechas. -->
    <div
      v-if="!ui.sidebarCollapsed"
      aria-hidden="true"
      class="fixed inset-0 z-20 bg-black/20 md:hidden"
      @click="ui.toggleSidebar()"
    ></div>

    <main class="relative flex min-w-0 flex-1 flex-col">
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

        <!-- Uso de contexto: alineado a la derecha, antes del engranaje. -->
        <ContextUsedBar
          class="ml-auto"
          :usage="chat.usage"
          :model="chat.model"
        />

        <button
          type="button"
          aria-label="Open settings"
          class="ml-2 flex h-9 w-9 items-center justify-center rounded-full transition hover:bg-black/[0.05]"
          @click="settingsOpen = true"
        >
          <PhGear :size="20" weight="regular" />
        </button>
      </header>

      <!-- Checklist de tareas en vivo: flota arriba a la derecha (estilo Codex),
           bajo el header. Vacio => no renderiza nada. -->
      <TodoList class="absolute right-3 top-16 z-10" :todos="chat.todos" />

      <MessageList
        :items="chat.items"
        @approve="chat.approveTool"
        @deny="chat.denyTool"
      >
        <!-- Plan minimizado: tarjeta al final de la conversacion (scrollea con
             ella, como una tool). Expandir reabre el overlay. -->
        <PlanCard
          v-if="chat.plan && !chat.planExpanded"
          :plan="chat.plan"
          @expand="chat.togglePlanExpanded"
        />
      </MessageList>

      <!-- Aviso de error de la sesion (fallo del proveedor o stream cortado).
           Vive sobre el composer, dentro de la columna del chat: visible pero
           sin alarmar, y el usuario lo descarta cuando quiera (identidad §11). -->
      <div v-if="chat.errorText" class="mx-auto w-full max-w-3xl px-6 pt-2">
        <ErrorNotice :message="chat.errorText" @dismiss="chat.clearError" />
      </div>

      <ChatComposer
        :running="chat.running"
        :mode="chat.mode"
        :files="chat.projectFiles"
        :commands="chat.commands"
        @send="chat.send"
        @stop="chat.stop"
        @toggle-mode="chat.toggleMode"
      />

      <!-- Plan expandido: overlay sobre la columna del chat (no tapa la sidebar).
           Minimizar lo colapsa a la tarjeta de la conversacion; aceptar lo
           ejecuta; solicitar cambio lo reescribe. -->
      <PlanView
        v-if="chat.plan && chat.planExpanded"
        :plan="chat.plan"
        @accept="chat.acceptPlan"
        @request-change="chat.requestPlanChange"
        @minimize="chat.togglePlanExpanded"
      />
    </main>

    <SettingsPanel v-if="settingsOpen" @close="settingsOpen = false" />
  </div>
</template>

<script lang="ts" setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { PhSidebarSimple, PhWrench } from '@phosphor-icons/vue'
import AppSidebar from '../components/AppSidebar.vue'
import DevToolsPanel from '../components/DevToolsPanel.vue'
import MessageList from '../components/MessageList.vue'
import ErrorNotice from '../components/ErrorNotice.vue'
import ChatComposer from '../components/ChatComposer.vue'
import WorkspacePicker from '../components/WorkspacePicker.vue'
import SettingsPanel from '../components/SettingsPanel.vue'
import PlanView from '../components/PlanView.vue'
import PlanCard from '../components/PlanCard.vue'
import TodoList from '../components/TodoList.vue'
import ContextUsedBar from '../components/ContextUsedBar.vue'
import DevEventPanel from '../components/DevEventPanel.vue'
import DiffScreen from '../components/DiffScreen.vue'
import { useChatStore } from '../stores/chat'
import { useGitStore } from '../stores/git'
import { knownWorkspaces } from '../lib/sessions'

// Solo en dev: panel para disparar eventos canned y construir la UI sin agente.
const dev = import.meta.env.DEV
import { useUiStore } from '../stores/ui'

// Vista raiz del chat: arma el layout (sidebar persistente + chat central) y
// conecta el store de chat al canal de la sesion. El mapeo evento->estado vive
// en el store (front.md §74); aqui solo gestionamos el ciclo de vida de la
// suscripcion, que la Fase 1 dejaba sin limpiar.
const chat = useChatStore()
const ui = useUiStore()
// Pantalla de diff: la abre el panel de git (git.openDiff) y vive como overlay en
// la columna del chat. La fuente de verdad (archivo + diff) esta en el store de git.
const git = useGitStore()

// Settings panel open state: ephemeral UI state of the view (not persisted, so
// it does not reappear on app relaunch).
const settingsOpen = ref(false)

// Un chat nuevo e inactivo (sin mensajes, sin plan, sin corrida en vuelo) muestra
// el composer al centro con el selector de carpeta; al primer envio (running) o
// cuando entra el primer item, la vista pasa al layout normal (composer abajo).
const isEmpty = computed(
  () => chat.items.length === 0 && !chat.running && !chat.plan,
)
// Carpetas elegibles para el chat nuevo: la vigente mas las que ya tienen chats.
// La fuente de verdad (sesiones y carpeta vigente) vive en el store.
const workspaceOptions = computed(() =>
  knownWorkspaces(chat.sessions, chat.workspace),
)

onMounted(async () => {
  chat.subscribe()
  // Puebla la sidebar con el historial de chats del backend. La app abre en un
  // chat nuevo vacio (identidad §2, Chat First): NO se auto-carga la ultima
  // sesion; la sidebar es como el usuario vuelve a una conversacion pasada.
  chat.loadSessions()
  // Trae el modelo activo una vez para dimensionar la barra de contexto.
  chat.loadModel()
  // Re-aplica la ultima carpeta usada (persistida entre reinicios) y la deja
  // vigente; cae a la del backend si no hay ninguna o ya no existe. Se hace antes
  // de listar archivos y comandos, que dependen de la carpeta vigente.
  await chat.restoreWorkspace()
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
      @open-settings="settingsOpen = true"
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
          class="flex h-9 w-9 items-center justify-center rounded-full transition hover:bg-black/[0.05] active:scale-95"
          @click="ui.toggleSidebar()"
        >
          <PhSidebarSimple :size="20" weight="regular" />
        </button>

        <!-- Uso de contexto: alineado a la derecha (el engranaje vive ahora al
             fondo de la sidebar). -->
        <ContextUsedBar
          class="ml-auto"
          :usage="chat.usage"
          :model="chat.model"
        />

        <!-- Abre/cierra el panel de herramientas de desarrollo (git, ...). -->
        <button
          type="button"
          aria-label="Toggle developer tools"
          :aria-pressed="ui.devPanelOpen"
          class="ml-2 flex h-9 w-9 items-center justify-center rounded-full transition hover:bg-black/[0.05] active:scale-95"
          :class="ui.devPanelOpen ? 'text-accent' : ''"
          @click="ui.toggleDevPanel()"
        >
          <PhWrench :size="20" weight="regular" />
        </button>
      </header>

      <!-- Checklist de tareas en vivo: flota arriba a la derecha (estilo Codex),
           bajo el header. Vacio => no renderiza nada. -->
      <TodoList class="absolute right-3 top-16 z-10" :todos="chat.todos" />

      <!-- Chat nuevo e inactivo: el composer se presenta al centro con el selector
           de carpeta de trabajo. Es el "Chat First" en su estado de partida: elegir
           donde trabajara el agente y empezar a escribir. Al primer envio la vista
           pasa al layout de conversacion (composer abajo). -->
      <div
        v-if="isEmpty"
        class="flex min-h-0 flex-1 flex-col items-center justify-center overflow-y-auto px-6"
      >
        <div class="w-full max-w-3xl">
          <h1 class="mb-6 text-center text-2xl tracking-tight">
            What are we working on?
          </h1>

          <!-- Selector de carpeta: las carpetas conocidas (con chats) mas la
               vigente, mas "Browse folder" para abrir el dialogo nativo. -->
          <WorkspacePicker
            :workspace="chat.workspace"
            :options="workspaceOptions"
            @select="(path: string) => chat.pickWorkspace(path)"
            @browse="() => chat.selectWorkspace()"
          />

          <!-- Aviso de error tambien en el chat nuevo: un envio puede fallar antes
               de que entre el primer mensaje. -->
          <Transition
            enter-active-class="transition duration-200 ease-snappy"
            enter-from-class="opacity-0 translate-y-2"
            leave-active-class="transition duration-150 ease-snappy"
            leave-to-class="opacity-0"
          >
            <div v-if="chat.errorText" class="pb-2">
              <ErrorNotice
                :message="chat.errorText"
                @dismiss="chat.clearError"
              />
            </div>
          </Transition>

          <ChatComposer
            :running="chat.running"
            :mode="chat.mode"
            :files="chat.projectFiles"
            :commands="chat.commands"
            @send="chat.send"
            @stop="chat.stop"
            @toggle-mode="chat.toggleMode"
          />
        </div>
      </div>

      <!-- Conversacion activa: lista de mensajes arriba (crece y scrollea), composer
           abajo. -->
      <template v-else>
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
             sin alarmar, y el usuario lo descarta cuando quiera (identidad §11).
             Aparece/desaparece con transicion (Emil: surgir sin transicion se
             siente roto). role=alert => fade + leve translateY de entrada; salida
             mas rapida y sin movimiento. -->
        <Transition
          enter-active-class="transition duration-200 ease-snappy"
          enter-from-class="opacity-0 translate-y-2"
          leave-active-class="transition duration-150 ease-snappy"
          leave-to-class="opacity-0"
        >
          <div v-if="chat.errorText" class="mx-auto w-full max-w-3xl px-6 pt-2">
            <ErrorNotice :message="chat.errorText" @dismiss="chat.clearError" />
          </div>
        </Transition>

        <ChatComposer
          :running="chat.running"
          :mode="chat.mode"
          :files="chat.projectFiles"
          :commands="chat.commands"
          @send="chat.send"
          @stop="chat.stop"
          @toggle-mode="chat.toggleMode"
        />
      </template>

      <!-- Plan expandido: overlay sobre la columna del chat (no tapa la sidebar).
           Minimizar lo colapsa a la tarjeta de la conversacion; aceptar lo
           ejecuta; solicitar cambio lo reescribe. Transicion modal (origin
           center, no anclado a un trigger): entra con fade + leve scale; sale
           mas rapido (Emil: la salida mas rapida que la entrada). -->
      <Transition
        enter-active-class="transition duration-[250ms] ease-snappy"
        enter-from-class="opacity-0 scale-[0.98]"
        leave-active-class="transition duration-[180ms] ease-snappy"
        leave-to-class="opacity-0 scale-[0.98]"
      >
        <PlanView
          v-if="chat.plan && chat.planExpanded"
          :plan="chat.plan"
          @accept="chat.acceptPlan"
          @request-change="chat.requestPlanChange"
          @minimize="chat.togglePlanExpanded"
        />
      </Transition>

      <!-- Pantalla de diff (estilo VSCode): overlay sobre la columna del chat al
           seleccionar un archivo en el panel de git. No tapa la sidebar ni el
           panel, asi se puede saltar de archivo en archivo. Misma transicion modal
           que el plan: entra con fade + leve scale; sale mas rapido. -->
      <Transition
        enter-active-class="transition duration-[250ms] ease-snappy"
        enter-from-class="opacity-0 scale-[0.98]"
        leave-active-class="transition duration-[180ms] ease-snappy"
        leave-to-class="opacity-0 scale-[0.98]"
      >
        <DiffScreen
          v-if="git.diffPath"
          :path="git.diffPath"
          :diff="git.diff"
          @close="git.closeDiff()"
        />
      </Transition>
    </main>

    <!-- Panel de herramientas de desarrollo: columna a la derecha (no overlay),
         con tabs (hoy solo Git). Entra/sale deslizando desde la derecha. -->
    <Transition
      enter-active-class="transition-[width] duration-200 ease-drawer"
      enter-from-class="w-0"
      leave-active-class="transition-[width] duration-200 ease-drawer"
      leave-to-class="w-0"
    >
      <div v-if="ui.devPanelOpen" class="shrink-0 overflow-hidden">
        <DevToolsPanel @close="ui.toggleDevPanel()" />
      </div>
    </Transition>

    <!-- Panel de configuracion full-screen: es un modal (origin center, no
         anclado a un trigger). Entra con fade + leve scale; sale mas rapido
         (Emil: la salida mas rapida que la entrada). El Flip GSAP interno de
         las cards MCP corre en interaccion posterior, no choca con esto. -->
    <Transition
      enter-active-class="transition duration-200 ease-snappy"
      enter-from-class="opacity-0 scale-[0.98]"
      leave-active-class="transition duration-150 ease-snappy"
      leave-to-class="opacity-0 scale-[0.98]"
    >
      <SettingsPanel v-if="settingsOpen" @close="settingsOpen = false" />
    </Transition>

    <DevEventPanel v-if="dev" />
  </div>
</template>

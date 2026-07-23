<script lang="ts" setup>
import { ref, computed, onMounted, watch } from 'vue'
import {
  PhX,
  PhPlus,
  PhGitBranch,
  PhTerminal,
  PhSpinnerGap,
} from '@phosphor-icons/vue'
import { useGitStore } from '../features/git/git'
import { useChatStore } from '../stores/chat'
import { useUiStore } from '../stores/ui'
import { useTabsStore, type Tab, type TabKind } from '../stores/tabs'
import { destroy } from '../features/terminal/terminalSession'
import TerminalPanel from '../features/terminal/TerminalPanel.vue'

// Panel de herramientas: barra de tabs ABIERTAS (instancias, no un set fijo) con
// un boton "+" para agregar Git/Terminal y un cierre por tab. El contenido se elige
// por el `kind` de la tab activa. Presentacional respecto del open/close del panel
// (lo controla la vista via el store de UI); aqui solo emitimos close.
// ponytail: agregar un tipo de tab es sumar al menu + un bloque por `kind`.
const emit = defineEmits<{ close: [] }>()

const git = useGitStore()
const chat = useChatStore()
const ui = useUiStore()
const tabs = useTabsStore()

const KINDS: { kind: TabKind; label: string; icon: unknown }[] = [
  { kind: 'terminal', label: 'Terminal', icon: PhTerminal },
  { kind: 'git', label: 'Git', icon: PhGitBranch },
]
const iconFor = (kind: TabKind) =>
  kind === 'terminal' ? PhTerminal : PhGitBranch

const addOpen = ref(false)
function add(kind: TabKind) {
  tabs.addTab(kind)
  addOpen.value = false
}

// Cerrar una tab: si es terminal hay que matar su pty (destroy); luego la saca de
// la lista. Cambiar de tab o cerrar el panel NO pasan por aca (la terminal persiste).
function closeTab(tab: Tab) {
  if (tab.kind === 'terminal') destroy(tab.id)
  tabs.closeTab(tab.id)
}

const active = computed(() => tabs.active)

onMounted(() => {
  tabs.ensureDefault()
  git.loadStatus()
})

// El estado de git lo lee el backend de la carpeta de trabajo vigente. Cambiar de
// carpeta (elegir/abrir otro proyecto) la mueve en vivo, asi que recargamos para no
// seguir mostrando los cambios del proyecto anterior. El onMounted cubre el primer
// load; este watch cubre los cambios mientras el panel esta abierto.
watch(
  () => chat.workspace,
  () => git.loadStatus(),
)

// Resize arrastrando el borde izquierdo: el panel esta a la derecha, asi que
// mover el handle hacia la izquierda lo ensancha. El ancho (acotado) vive en el
// store de UI y se persiste. ponytail: pointer events nativos, sin libreria.
function startResize(e: PointerEvent) {
  const startX = e.clientX
  const startW = ui.devPanelWidth
  document.body.style.userSelect = 'none' // no seleccionar texto al arrastrar
  const move = (ev: PointerEvent) =>
    ui.setDevPanelWidth(startW + (startX - ev.clientX))
  const up = () => {
    document.body.style.userSelect = ''
    window.removeEventListener('pointermove', move)
    window.removeEventListener('pointerup', up)
  }
  window.addEventListener('pointermove', move)
  window.addEventListener('pointerup', up)
}
</script>

<template>
  <aside
    aria-label="Developer tools"
    :style="{ width: ui.devPanelWidth + 'px' }"
    class="relative flex h-full shrink-0 flex-col border-l border-black/5 bg-black/[0.015]"
  >
    <!-- Handle de resize: franja sobre el borde izquierdo. -->
    <div
      role="separator"
      aria-orientation="vertical"
      aria-label="Resize developer tools"
      class="absolute inset-y-0 left-0 z-10 w-1 touch-none cursor-col-resize transition-colors hover:bg-accent/30 active:bg-accent/40"
      @pointerdown.prevent="startResize"
    ></div>

    <!-- Barra: tabs (con scroll propio) + boton agregar + cerrar panel. El "+" y su
         menu viven FUERA del nav: overflow-x-auto recorta el overflow vertical y
         taparia el menu que se abre hacia abajo. -->
    <div class="flex items-center gap-1 border-b border-black/5 px-2 py-2">
      <nav
        role="tablist"
        aria-label="Developer tools"
        class="flex min-w-0 items-center gap-1 overflow-x-auto"
      >
        <div
          v-for="tab in tabs.tabs"
          :key="tab.id"
          role="tab"
          :aria-selected="active?.id === tab.id ? 'true' : 'false'"
          class="flex shrink-0 items-center gap-1.5 rounded-full py-1.5 pl-3 pr-1.5 text-sm transition"
          :class="
            active?.id === tab.id
              ? 'bg-black/[0.06] font-medium'
              : 'cursor-pointer hover:bg-black/[0.04]'
          "
          @click="tabs.setActive(tab.id)"
        >
          <component :is="iconFor(tab.kind)" :size="16" weight="regular" />
          {{ tab.title }}
          <button
            type="button"
            aria-label="Cerrar tab"
            class="flex h-5 w-5 items-center justify-center rounded-full transition hover:bg-black/10 active:scale-90"
            @click.stop="closeTab(tab)"
          >
            <PhX :size="12" weight="bold" />
          </button>
        </div>
      </nav>

      <!-- Agregar tab: boton + menu de tipos. -->
      <div class="relative shrink-0">
        <button
          type="button"
          aria-label="Agregar herramienta"
          class="flex h-8 w-8 items-center justify-center rounded-full transition hover:bg-black/[0.05] active:scale-95"
          @click="addOpen = !addOpen"
        >
          <PhPlus :size="16" weight="bold" />
        </button>
        <template v-if="addOpen">
          <!-- backdrop para cerrar al clickear fuera -->
          <div class="fixed inset-0 z-20" @click="addOpen = false"></div>
          <div
            role="menu"
            class="absolute left-0 top-full z-30 mt-1 flex w-40 flex-col rounded-lg border border-black/10 bg-paper p-1 shadow-lg"
          >
            <button
              v-for="k in KINDS"
              :key="k.kind"
              type="button"
              role="menuitem"
              class="flex items-center gap-2 rounded-md px-2 py-1.5 text-sm transition hover:bg-black/[0.05]"
              @click="add(k.kind)"
            >
              <component :is="k.icon" :size="16" weight="regular" />
              {{ k.label }}
            </button>
          </div>
        </template>
      </div>

      <button
        type="button"
        aria-label="Cerrar herramientas"
        class="ml-auto flex h-8 w-8 shrink-0 items-center justify-center rounded-full transition hover:bg-black/[0.05] active:scale-95"
        @click="emit('close')"
      >
        <PhX :size="18" weight="regular" />
      </button>
    </div>

    <!-- Panel vacio (sin tabs). -->
    <div
      v-if="!active"
      class="flex flex-1 flex-col items-center justify-center gap-2 text-center text-sm opacity-50"
    >
      <PhPlus :size="24" weight="regular" class="opacity-40" />
      Agrega una herramienta con +
    </div>

    <!-- Tab Terminal: shell real bajo un pty (una por id). -->
    <TerminalPanel
      v-else-if="active.kind === 'terminal'"
      :key="active.id"
      :session-id="active.id"
      class="min-h-0 flex-1"
    />

    <!-- Tab Git. -->
    <div v-else class="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-3">
      <!-- Sin repo: ofrecer iniciar uno en vez de la UI de commit. -->
      <div
        v-if="git.status && !git.status.isRepo"
        class="flex flex-1 flex-col items-center justify-center gap-3 text-center"
      >
        <PhGitBranch :size="28" weight="regular" class="opacity-30" />
        <p class="text-sm opacity-60">
          Este proyecto todavia no tiene un repositorio git.
        </p>
        <button
          type="button"
          aria-label="Iniciar repositorio"
          :disabled="git.initializing"
          class="flex items-center gap-1.5 rounded-full bg-accent px-4 py-1.5 text-sm text-paper transition hover:brightness-95 active:scale-[0.97] disabled:opacity-40"
          @click="git.initRepo()"
        >
          <PhSpinnerGap
            v-if="git.initializing"
            :size="16"
            class="animate-spin"
          />
          Iniciar repositorio
        </button>
        <p v-if="git.error" class="text-xs text-red-600">{{ git.error }}</p>
      </div>

      <!-- Con repo: mensaje del commit + acciones y listas de cambios. -->
      <template v-else>
        <div class="flex flex-col gap-2">
          <textarea
            v-model="git.message"
            rows="3"
            placeholder="Mensaje del commit"
            class="w-full resize-none rounded-md border border-black/10 bg-paper px-3 py-2 text-sm outline-none focus:border-accent/50"
          ></textarea>
          <div class="flex gap-2">
            <button
              type="button"
              aria-label="Generar mensaje"
              :disabled="git.generating"
              class="flex items-center gap-1.5 rounded-full px-3 py-1.5 text-sm transition hover:bg-black/[0.05] active:scale-[0.97] disabled:opacity-50"
              @click="git.generate()"
            >
              <PhSpinnerGap
                v-if="git.generating"
                :size="16"
                class="animate-spin"
              />
              Generar mensaje
            </button>
            <button
              type="button"
              aria-label="Crear commit"
              :disabled="git.committing || !git.message.trim()"
              class="ml-auto flex items-center gap-1.5 rounded-full bg-accent px-4 py-1.5 text-sm text-paper transition hover:brightness-95 active:scale-[0.97] disabled:opacity-40"
              @click="git.commit()"
            >
              Commit
            </button>
          </div>
        </div>

        <p v-if="git.error" class="text-xs text-red-600">{{ git.error }}</p>

        <!-- Staged. -->
        <section class="flex flex-col gap-1">
          <h3 class="px-1 text-xs uppercase tracking-wide opacity-50">
            Staged ({{ git.status?.staged?.length ?? 0 }})
          </h3>
          <p v-if="!git.status?.staged?.length" class="px-1 text-sm opacity-40">
            Sin cambios staged
          </p>
          <button
            v-for="change in git.status?.staged ?? []"
            :key="change.path"
            type="button"
            data-staged
            :title="change.path"
            class="flex items-center gap-2 rounded-md px-1 py-1 text-left text-sm transition hover:bg-black/[0.04] active:scale-[0.99]"
            @click="git.openDiff(change.path)"
          >
            <span class="w-4 shrink-0 text-center text-xs opacity-50">{{
              change.status
            }}</span>
            <span class="truncate">{{ change.path }}</span>
          </button>
        </section>

        <!-- Cambios sin stage (working tree). -->
        <section class="flex flex-col gap-1">
          <h3 class="px-1 text-xs uppercase tracking-wide opacity-50">
            Cambios ({{ git.status?.unstaged?.length ?? 0 }})
          </h3>
          <p
            v-if="!git.status?.unstaged?.length"
            class="px-1 text-sm opacity-40"
          >
            Sin cambios sin stage
          </p>
          <button
            v-for="change in git.status?.unstaged ?? []"
            :key="change.path"
            type="button"
            data-unstaged
            :title="change.path"
            class="flex items-center gap-2 rounded-md px-1 py-1 text-left text-sm transition hover:bg-black/[0.04] active:scale-[0.99]"
            @click="git.openDiff(change.path)"
          >
            <span class="w-4 shrink-0 text-center text-xs opacity-50">{{
              change.status
            }}</span>
            <span class="truncate">{{ change.path }}</span>
          </button>
        </section>

        <!-- Untracked. -->
        <section class="flex flex-col gap-1">
          <h3 class="px-1 text-xs uppercase tracking-wide opacity-50">
            Untracked ({{ git.status?.untracked?.length ?? 0 }})
          </h3>
          <p
            v-if="!git.status?.untracked?.length"
            class="px-1 text-sm opacity-40"
          >
            Sin archivos nuevos
          </p>
          <button
            v-for="change in git.status?.untracked ?? []"
            :key="change.path"
            type="button"
            data-untracked
            :title="change.path"
            class="flex items-center gap-2 rounded-md px-1 py-1 text-left text-sm transition hover:bg-black/[0.04] active:scale-[0.99]"
            @click="git.openDiff(change.path)"
          >
            <span class="w-4 shrink-0 text-center text-xs opacity-50">{{
              change.status
            }}</span>
            <span class="truncate">{{ change.path }}</span>
          </button>
        </section>
      </template>
    </div>
  </aside>
</template>

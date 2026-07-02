<script lang="ts" setup>
import { ref, computed, nextTick, onMounted, onUnmounted } from 'vue'
import gsap from 'gsap'
import { Flip } from 'gsap/Flip'
import {
  PhX,
  PhGear,
  PhPlugs,
  PhSparkle,
  PhArrowLeft,
} from '@phosphor-icons/vue'
import { mcpCatalog, mcpIcon } from '../lib/mcps'
import McpCard from './McpCard.vue'
import ProviderSettings from './ProviderSettings.vue'
import { useChatStore } from '../stores/chat'
import { prefersReducedMotion } from '../lib/motion'

// El selector de modelo (pestania General) lee la config del provider vigente del
// store y le delega los cambios: aplicar recablea el backend (setProvider) y cargar
// modelos consulta el endpoint (listModels). El resto del panel es presentacional.
const chat = useChatStore()

gsap.registerPlugin(Flip)

// Full-screen settings panel (frontend-only, no backend): tabs on the left and
// content on the right, covering the full viewport (not a floating modal).
// The MCPs tab shows the marketplace-style list with hardcoded data. It is
// presentational: emits `close` and leaves the open state to the view that
// mounts it.
const emit = defineEmits<{ close: [] }>()

type TabId = 'general' | 'mcps' | 'skills'
const tabs = [
  { id: 'general', label: 'General', icon: PhGear },
  { id: 'mcps', label: 'MCPs', icon: PhPlugs },
  { id: 'skills', label: 'Skills', icon: PhSparkle },
] as const

const active = ref<TabId>('general')

// MCP detail sub-view: clicking a card expands it into a detail where the image
// spans the full width and the title moves up and grows. The transition is a
// GSAP Flip on the shared image and title (matched by data-flip-id).
const selectedId = ref<string | null>(null)
const selected = computed(
  () => mcpCatalog.find((entry) => entry.id === selectedId.value) ?? null,
)

// Records the shared image/title, applies the state change, then Flips from the
// recorded layout to the new one. This is a shared-element morph (the same idea
// as the View Transitions API): the destination layout is committed instantly
// and only the image and title animate, via transforms, from their old box to
// the new one. We avoid `absolute` so the surrounding content does not jump,
// and `scale: true` makes the size change morph smoothly instead of reflowing.
// Honors prefers-reduced-motion by skipping the animation.
async function flipTo(id: string, mutate: () => void) {
  if (prefersReducedMotion()) {
    mutate()
    return
  }
  const selector = `[data-flip-id="mcp-img-${id}"], [data-flip-id="mcp-title-${id}"]`
  const state = Flip.getState(selector)
  mutate()
  await nextTick()
  // `targets` must point at the NEW (post-swap) elements: the list and detail
  // are different DOM nodes, so without it Flip would animate the detached old
  // nodes and the morph would look instant.
  //
  // Lift the morphing image/title above everything else for the duration: when
  // collapsing back, the full list reappears and the image flies down to its
  // card, so without a raised z-index the sibling cards would paint over it.
  Flip.from(state, {
    targets: selector,
    duration: 0.5,
    ease: 'power2.inOut',
    scale: true,
    onStart: () => gsap.set(selector, { zIndex: 50 }),
    onComplete: () => gsap.set(selector, { clearProps: 'zIndex' }),
  })
}

function openDetail(id: string) {
  flipTo(id, () => {
    selectedId.value = id
  })
}

function closeDetail() {
  const id = selectedId.value
  if (!id) return
  flipTo(id, () => {
    selectedId.value = null
  })
}

function selectTab(id: TabId) {
  active.value = id
  selectedId.value = null
}

// Escape backs out of the detail first; otherwise it closes the panel.
function onKeydown(e: KeyboardEvent) {
  if (e.key !== 'Escape') return
  if (selectedId.value) closeDetail()
  else emit('close')
}
onMounted(() => window.addEventListener('keydown', onKeydown))
onUnmounted(() => window.removeEventListener('keydown', onKeydown))
</script>

<template>
  <div
    role="dialog"
    aria-modal="true"
    aria-label="Configuracion"
    class="fixed inset-0 z-40 flex bg-paper"
  >
    <!-- Tabs column. -->
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

    <!-- Content panel. -->
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
          <!-- Detail sub-view: image full-width, title on top, description below. -->
          <div v-if="selected" class="flex flex-col">
            <button
              type="button"
              aria-label="Back to MCPs"
              class="flex w-fit items-center gap-1.5 rounded-full py-1 pr-3 text-sm opacity-60 transition hover:opacity-100 active:scale-[0.97]"
              @click="closeDetail"
            >
              <PhArrowLeft :size="18" weight="regular" />
              MCPs
            </button>

            <h2
              :data-flip-id="`mcp-title-${selected.id}`"
              class="relative mt-4 text-2xl tracking-tight"
            >
              {{ selected.name }}
            </h2>

            <img
              :src="selected.image ?? mcpIcon(selected)"
              :alt="selected.name"
              :data-flip-id="`mcp-img-${selected.id}`"
              class="relative mt-5 aspect-[16/9] w-full rounded-soft object-cover"
            />

            <p class="mt-5 text-sm leading-relaxed opacity-70">
              {{ selected.description }}
            </p>
          </div>

          <!-- List sub-view. -->
          <template v-else>
            <h2 class="text-lg tracking-tight">MCPs</h2>
            <p class="mt-1 text-sm opacity-50">
              Servidores disponibles para conectar con atenea.
            </p>
            <!-- Same width as the chat column (max-w-3xl), centered. -->
            <div class="mx-auto mt-6 flex w-full max-w-3xl flex-col gap-4">
              <McpCard
                v-for="entry in mcpCatalog"
                :key="entry.id"
                :entry="entry"
                @select="openDetail"
              />
            </div>
          </template>
        </template>

        <template v-else>
          <h2 class="text-lg tracking-tight">Skills</h2>
          <p class="mt-3 text-sm opacity-50">Skills coming soon.</p>
        </template>
      </div>
    </section>
  </div>
</template>

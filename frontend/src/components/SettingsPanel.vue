<script lang="ts" setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { PhX, PhGear, PhPlugs, PhSparkle } from '@phosphor-icons/vue'
import { mcpCatalog } from '../lib/mcps'
import McpCard from './McpCard.vue'

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

// Escape closes the panel, like any desktop dialog.
function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') emit('close')
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
        class="flex items-center gap-2 rounded-full px-4 py-2.5 text-left text-sm transition"
        :class="active === tab.id ? 'bg-black/[0.06] font-medium' : 'hover:bg-black/[0.04]'"
        @click="active = tab.id"
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
        class="absolute right-4 top-4 flex h-9 w-9 items-center justify-center rounded-full transition hover:bg-black/[0.05]"
        @click="emit('close')"
      >
        <PhX :size="20" weight="regular" />
      </button>

      <div class="mx-auto w-full max-w-3xl px-8 py-10">
        <template v-if="active === 'general'">
          <h2 class="text-lg tracking-tight">General</h2>
          <p class="mt-3 text-sm opacity-50">Preferencias generales coming soon.</p>
        </template>

        <template v-else-if="active === 'mcps'">
          <h2 class="text-lg tracking-tight">MCPs</h2>
          <p class="mt-1 text-sm opacity-50">
            Servidores disponibles para conectar con atenea.
          </p>
          <!-- Same width as the chat column (max-w-3xl), centered. -->
          <div class="mx-auto mt-6 flex w-full max-w-3xl flex-col gap-4">
            <McpCard v-for="entry in mcpCatalog" :key="entry.id" :entry="entry" />
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

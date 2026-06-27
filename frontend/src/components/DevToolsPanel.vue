<script lang="ts" setup>
import { ref, onMounted } from 'vue'
import { PhX, PhGitBranch, PhSpinnerGap } from '@phosphor-icons/vue'
import { useGitStore } from '../stores/git'

// Panel de herramientas de desarrollo: barra de tabs (hoy solo Git) y el
// contenido de la tab activa. La tab Git es el MVP de commit: mensaje, generar
// mensaje con el modelo, y listas de cambios staged/untracked. El estado de git
// vive en su store (inyectable por la devtool DevEventPanel para iterar la UI sin
// repo). Presentacional respecto del open/close (lo controla la vista via el store
// de UI); aqui solo emitimos close.
// ponytail: una sola tab por ahora; agregar terminal/browser es sumar a `tabs`.
const emit = defineEmits<{ close: [] }>()

const tabs = [{ id: 'git', label: 'Git', icon: PhGitBranch }] as const
const active = ref<(typeof tabs)[number]['id']>('git')

const git = useGitStore()
onMounted(git.loadStatus)
</script>

<template>
  <aside
    aria-label="Developer tools"
    class="flex h-full w-80 shrink-0 flex-col border-l border-black/5 bg-black/[0.015]"
  >
    <!-- Barra de tabs + cerrar. -->
    <nav
      role="tablist"
      aria-label="Developer tools"
      class="flex items-center gap-1 border-b border-black/5 px-2 py-2"
    >
      <button
        v-for="tab in tabs"
        :key="tab.id"
        type="button"
        role="tab"
        :aria-selected="active === tab.id ? 'true' : 'false'"
        class="flex items-center gap-2 rounded-full px-3 py-1.5 text-sm transition active:scale-[0.97]"
        :class="
          active === tab.id ? 'bg-black/[0.06] font-medium' : 'hover:bg-black/[0.04]'
        "
        @click="active = tab.id"
      >
        <component :is="tab.icon" :size="16" weight="regular" />
        {{ tab.label }}
      </button>
      <button
        type="button"
        aria-label="Cerrar herramientas"
        class="ml-auto flex h-8 w-8 items-center justify-center rounded-full transition hover:bg-black/[0.05] active:scale-95"
        @click="emit('close')"
      >
        <PhX :size="18" weight="regular" />
      </button>
    </nav>

    <!-- Tab Git. -->
    <div
      v-if="active === 'git'"
      class="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-3"
    >
      <!-- Mensaje del commit + acciones. -->
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
            <PhSpinnerGap v-if="git.generating" :size="16" class="animate-spin" />
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
        <div
          v-for="change in git.status?.staged ?? []"
          :key="change.path"
          data-staged
          class="flex items-center gap-2 rounded-md px-1 py-1 text-sm"
        >
          <span class="w-4 shrink-0 text-center text-xs opacity-50">{{ change.status }}</span>
          <span class="truncate">{{ change.path }}</span>
        </div>
      </section>

      <!-- Untracked. -->
      <section class="flex flex-col gap-1">
        <h3 class="px-1 text-xs uppercase tracking-wide opacity-50">
          Untracked ({{ git.status?.untracked?.length ?? 0 }})
        </h3>
        <p v-if="!git.status?.untracked?.length" class="px-1 text-sm opacity-40">
          Sin archivos nuevos
        </p>
        <div
          v-for="change in git.status?.untracked ?? []"
          :key="change.path"
          data-untracked
          class="flex items-center gap-2 rounded-md px-1 py-1 text-sm"
        >
          <span class="w-4 shrink-0 text-center text-xs opacity-50">{{ change.status }}</span>
          <span class="truncate">{{ change.path }}</span>
        </div>
      </section>
    </div>
  </aside>
</template>

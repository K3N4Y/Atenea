<script lang="ts" setup>
import { computed } from 'vue'
import { PhClipboardText, PhCheck } from '@phosphor-icons/vue'
import type { PlanState } from './types'

// PlanCard es el plan minimizado: una tarjeta en el flujo de la conversacion
// (como una tool card) que resume el plan vigente. Toda la tarjeta expande el
// plan; el boton Aceptar permite aprobarlo sin abrir la vista expandida.
// Presentacional: emite expand/accept y recibe el plan por prop.
const props = defineProps<{ plan: PlanState }>()
const emit = defineEmits<{ expand: []; accept: [] }>()

const title = computed(() => props.plan.title || 'Plan')
</script>

<template>
  <div
    data-action="expand"
    aria-label="Expandir plan"
    class="mt-5 flex w-full cursor-pointer items-center gap-3 rounded-soft border border-black/5 bg-black/[0.04] px-4 py-3 text-sm transition hover:bg-black/[0.06] active:scale-[0.97]"
    @click="emit('expand')"
  >
    <PhClipboardText :size="18" weight="regular" class="shrink-0 opacity-70" />
    <span class="min-w-0 flex-1 truncate text-left font-medium">{{
      title
    }}</span>
    <button
      type="button"
      data-action="accept"
      class="flex items-center gap-1.5 rounded-full bg-accent px-3 py-1 text-xs text-paper transition hover:opacity-90 active:scale-[0.97]"
      @click.stop="emit('accept')"
    >
      <PhCheck :size="14" weight="bold" />
      Aceptar
    </button>
  </div>
</template>

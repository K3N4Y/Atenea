<script lang="ts" setup>
import { computed } from 'vue'
import { PhClipboardText, PhArrowsOutSimple } from '@phosphor-icons/vue'
import type { PlanState } from '../stores/chat'

// PlanCard es el plan minimizado: una tarjeta en el flujo de la conversacion
// (como una tool card) que resume el plan vigente y deja expandirlo. No ofrece
// aceptar / solicitar cambio: esas acciones viven en la vista expandida
// (PlanView). Presentacional: emite expand y recibe el plan por prop.
const props = defineProps<{ plan: PlanState }>()
const emit = defineEmits<{ expand: [] }>()

const title = computed(() => props.plan.title || 'Plan')
</script>

<template>
  <button
    type="button"
    data-action="expand"
    aria-label="Expandir plan"
    class="flex w-full items-center gap-3 rounded-soft border border-black/5 bg-black/[0.04] px-4 py-3 text-sm transition hover:bg-black/[0.06] active:scale-[0.97]"
    @click="emit('expand')"
  >
    <PhClipboardText :size="18" weight="regular" class="shrink-0 opacity-70" />
    <span class="min-w-0 flex-1 truncate text-left font-medium">{{
      title
    }}</span>
    <span class="flex shrink-0 items-center gap-1.5 opacity-70">
      <PhArrowsOutSimple :size="16" weight="regular" />
      Expandir
    </span>
  </button>
</template>

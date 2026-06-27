<script lang="ts" setup>
import { ref, computed } from 'vue'
import { PhCheck, PhPencilSimple, PhArrowsInSimple } from '@phosphor-icons/vue'
import MarkdownContent from './MarkdownContent.vue'
import type { PlanState } from '../stores/chat'

// Vista expandida del plan (estilo Cursor): el agente lo presenta via la tool
// present_plan y aqui se muestra el markdown completo con las acciones: aceptar
// (ejecuta), solicitar cambio (el usuario describe que cambiar) y minimizar (lo
// colapsa a una tarjeta en la conversacion, ver PlanCard). Es un overlay sobre la
// columna del chat (no tapa la sidebar). Presentacional: emite accept /
// request-change / minimize y recibe el plan por prop.
const props = defineProps<{ plan: PlanState }>()
const emit = defineEmits<{
  accept: []
  'request-change': [string]
  minimize: []
}>()

const title = computed(() => props.plan.title || 'Plan')

// Panel de feedback inline: oculto por defecto, se abre con "Solicitar cambio".
const requesting = ref(false)
const feedback = ref('')

function submitChange() {
  const t = feedback.value.trim()
  if (!t) return
  emit('request-change', t)
  feedback.value = ''
  requesting.value = false
}

function cancelChange() {
  requesting.value = false
  feedback.value = ''
}
</script>

<template>
  <div
    role="dialog"
    aria-label="Plan"
    class="absolute inset-0 z-30 flex flex-col bg-paper"
  >
    <!-- Barra superior: titulo a la izquierda, acciones a la derecha. -->
    <header class="flex items-center gap-3 border-b border-black/5 px-8 py-4">
      <h2 class="min-w-0 flex-1 truncate text-lg tracking-tight">
        {{ title }}
      </h2>

      <button
        type="button"
        data-action="minimize"
        aria-label="Minimizar plan"
        class="flex items-center gap-1.5 rounded-full bg-black/[0.06] px-4 py-2 text-sm transition hover:bg-black/[0.09] active:scale-[0.97]"
        @click="emit('minimize')"
      >
        <PhArrowsInSimple :size="16" weight="regular" />
        Minimizar
      </button>
      <button
        type="button"
        data-action="request-change"
        class="flex items-center gap-1.5 rounded-full bg-black/[0.06] px-4 py-2 text-sm transition hover:bg-black/[0.09] active:scale-[0.97]"
        :class="requesting ? 'bg-black/[0.09]' : ''"
        @click="requesting = !requesting"
      >
        <PhPencilSimple :size="16" weight="regular" />
        Solicitar cambio
      </button>
      <button
        type="button"
        data-action="accept"
        class="flex items-center gap-1.5 rounded-full bg-accent px-4 py-2 text-sm text-paper transition hover:opacity-90 active:scale-[0.97]"
        @click="emit('accept')"
      >
        <PhCheck :size="16" weight="bold" />
        Aceptar
      </button>
    </header>

    <!-- Panel de feedback inline (bajo la cabecera). -->
    <div v-if="requesting" class="border-b border-black/5 px-8 py-4">
      <div class="mx-auto w-full max-w-3xl">
        <textarea
          v-model="feedback"
          rows="3"
          aria-label="Describe el cambio"
          placeholder="Describe el cambio que quieres en el plan"
          class="w-full resize-none rounded-soft bg-black/[0.04] p-3 leading-relaxed placeholder:opacity-40 focus:outline-none focus:ring-2 focus:ring-accent/20"
        ></textarea>
        <div class="mt-2 flex items-center justify-end gap-2">
          <button
            type="button"
            data-action="cancel-change"
            class="rounded-full px-4 py-2 text-sm opacity-60 transition hover:bg-black/[0.04] hover:opacity-100 active:scale-[0.97]"
            @click="cancelChange"
          >
            Cancelar
          </button>
          <button
            type="button"
            data-action="submit-change"
            class="rounded-full bg-accent px-4 py-2 text-sm text-paper transition hover:opacity-90 active:scale-[0.97]"
            @click="submitChange"
          >
            Enviar cambio
          </button>
        </div>
      </div>
    </div>

    <!-- Cuerpo: markdown del plan, scrollable y centrado. -->
    <div class="flex-1 overflow-y-auto">
      <div class="mx-auto w-full max-w-3xl px-8 py-8">
        <MarkdownContent :text="plan.markdown" />
      </div>
    </div>
  </div>
</template>

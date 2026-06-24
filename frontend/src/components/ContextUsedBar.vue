<script lang="ts" setup>
import { computed } from 'vue'
import type { Usage } from '../stores/chat'
import {
  contextWindowFor,
  contextPercent,
  formatTokens,
} from '../lib/contextWindow'

// Indicador del contexto usado en el header: porcentaje + barra de progreso +
// tokens compactos, escalado contra la ventana del modelo. Presentational:
// recibe usage (camelCase) y model por prop; sin usage no pinta nada. Solo
// tokens, sin costos.
//
// Voz y microcopy (identidad §11): discreto, sin alarmar; el accent se usa con
// mesura para la barra, el resto va con opacidad baja.
const props = defineProps<{ usage: Usage | null; model: string }>()

const pct = computed(() =>
  contextPercent(props.usage?.inputTokens ?? 0, props.model),
)
const window = computed(() => contextWindowFor(props.model))

// Desglose completo para el tooltip; el texto visible solo lleva in/out.
const title = computed(() => {
  const u = props.usage
  if (!u) return ''
  return [
    `${formatTokens(u.inputTokens)} in / ${formatTokens(window.value)} ventana`,
    `${formatTokens(u.outputTokens)} out`,
    `${formatTokens(u.reasoningTokens)} reasoning`,
    `${formatTokens(u.cacheReadTokens)} cache read`,
    `${formatTokens(u.cacheWriteTokens)} cache write`,
  ].join(' · ')
})
</script>

<template>
  <div
    v-if="usage"
    class="flex items-center gap-2 text-xs opacity-70"
    :title="title"
  >
    <div
      role="progressbar"
      :aria-valuenow="pct"
      aria-valuemin="0"
      aria-valuemax="100"
      :title="title"
      class="h-1.5 w-16 overflow-hidden rounded-soft bg-black/[0.08]"
    >
      <div class="h-full rounded-soft bg-accent" :style="{ width: pct + '%' }"></div>
    </div>
    <span class="tabular-nums">{{ pct }}%</span>
    <span class="tabular-nums opacity-80">
      {{ formatTokens(usage.inputTokens) }} in · {{ formatTokens(usage.outputTokens) }} out
    </span>
  </div>
</template>

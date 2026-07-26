<script lang="ts" setup>
import { computed } from 'vue'
import type { Usage } from './types'
import { contextPercent, formatTokens } from './contextWindow'

// Indicador del contexto usado en el header: porcentaje + barra de progreso +
// tokens compactos, escalado contra la ventana que declara el adapter activo.
// Presentational: recibe usage (camelCase) y contextWindow por prop; sin usage no
// pinta nada. Solo tokens, sin costos.
//
// Con contextWindow en 0 nadie declara la ventana de ese modelo: se muestran los
// tokens sin barra ni porcentaje, que es mas honesto que escalar contra un numero
// inventado.
//
// Voz y microcopy (identidad §11): discreto, sin alarmar; el accent se usa con
// mesura para la barra, el resto va con opacidad baja.
const props = defineProps<{ usage: Usage | null; contextWindow: number }>()

const scaled = computed(() => props.contextWindow > 0)
const pct = computed(() =>
  contextPercent(props.usage?.inputTokens ?? 0, props.contextWindow),
)

// Desglose completo para el tooltip; el texto visible solo lleva in/out.
const title = computed(() => {
  const u = props.usage
  if (!u) return ''
  return [
    scaled.value
      ? `${formatTokens(u.inputTokens)} in / ${formatTokens(props.contextWindow)} ventana`
      : `${formatTokens(u.inputTokens)} in`,
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
    <template v-if="scaled">
      <div
        role="progressbar"
        :aria-valuenow="pct"
        aria-valuemin="0"
        aria-valuemax="100"
        :title="title"
        class="h-1.5 w-16 overflow-hidden rounded-soft bg-black/[0.08]"
      >
        <div
          class="h-full rounded-soft bg-accent"
          :style="{ width: pct + '%' }"
        ></div>
      </div>
      <span class="tabular-nums">{{ pct }}%</span>
    </template>
    <span class="tabular-nums opacity-80">
      {{ formatTokens(usage.inputTokens) }} in ·
      {{ formatTokens(usage.outputTokens) }} out
    </span>
  </div>
</template>

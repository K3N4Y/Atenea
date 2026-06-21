<script lang="ts" setup>
import { computed } from 'vue'
import { PhFile, PhCircleNotch, PhCheck, PhX } from '@phosphor-icons/vue'
import type { ToolItem } from '../stores/chat'
import { basename } from '../lib/path'

const props = defineProps<{ item: ToolItem }>()

// Tool Read (identidad §10): "Reading"/"Read" + solo el nombre del archivo.
const isRead = computed(() => props.item.name === 'read')

function inputPath(input: unknown): string {
  if (!input || typeof input !== 'object') return ''
  const o = input as Record<string, unknown>
  const v = o.path ?? o.file_path ?? o.filename ?? o.file
  return typeof v === 'string' ? v : ''
}
const fileName = computed(() => basename(inputPath(props.item.input)))
const readLabel = computed(() => (props.item.status === 'running' ? 'Reading' : 'Read'))
</script>

<template>
  <div v-if="isRead" class="flex items-center gap-2 text-sm opacity-70">
    <PhFile :size="16" weight="regular" />
    <span>{{ readLabel }}</span>
    <span v-if="fileName" class="opacity-90">{{ fileName }}</span>
  </div>

  <!-- Resto de tools (edit/diff/echo...): bloque con su propio fondo (§8). -->
  <div v-else class="rounded-soft bg-black/[0.04] px-4 py-3 text-sm">
    <div class="flex items-center gap-2">
      <PhCircleNotch
        v-if="item.status === 'running'"
        :size="16"
        weight="bold"
        class="animate-spin text-accent"
      />
      <PhCheck v-else-if="item.status === 'success'" :size="16" weight="bold" class="opacity-50" />
      <PhX v-else :size="16" weight="bold" class="text-accent" />
      <span class="font-medium">{{ item.name || 'tool' }}</span>
      <span class="opacity-50">{{ item.status }}</span>
    </div>
    <pre
      v-if="item.output"
      class="mt-2 overflow-x-auto whitespace-pre-wrap break-words text-xs opacity-80"
    >{{ item.output }}</pre>
    <p v-if="item.error" class="mt-2 text-xs text-accent">{{ item.error }}</p>
  </div>
</template>

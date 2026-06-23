<script lang="ts" setup>
import { computed } from 'vue'
import type { McpEntry } from '../lib/mcps'
import { mcpIcon } from '../lib/mcps'

// Horizontal card of the MCP marketplace (identity §8: content flows over
// Blanco Papel). Rounded image on the left, name on top and description
// below. Presentational: receives the entry via prop.
const props = defineProps<{ entry: McpEntry }>()

// If the entry brings its own image, it is shown keeping its original aspect
// ratio (object-contain + auto width, bounded height). Otherwise it falls
// back to a generated square avatar.
const hasImage = computed(() => Boolean(props.entry.image))
const src = computed(() => props.entry.image ?? mcpIcon(props.entry))
</script>

<template>
  <article
    class="flex items-center gap-5 rounded-soft border border-black/5 bg-black/[0.02] p-4 transition hover:bg-black/[0.04]"
  >
    <img
      :src="src"
      :alt="props.entry.name"
      class="h-20 shrink-0 rounded-2xl"
      :class="hasImage ? 'w-auto max-w-40 object-contain' : 'w-20 object-cover'"
    />
    <div class="min-w-0">
      <h3 class="truncate text-base font-medium">{{ props.entry.name }}</h3>
      <p class="mt-1.5 text-sm leading-relaxed opacity-60">{{ props.entry.description }}</p>
    </div>
  </article>
</template>

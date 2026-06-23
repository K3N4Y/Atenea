<script lang="ts" setup>
import type { McpEntry } from '../lib/mcps'
import { mcpIcon } from '../lib/mcps'

// Horizontal card of the MCP marketplace (identity §8: content flows over
// Blanco Papel). Rounded image on the left, name on top, description below.
// The whole card is clickable and emits `select` so the parent expands it into
// the detail view.
//
// It stays an <article role="button"> rather than a real <button>: when
// collapsing back from the detail, GSAP Flip transforms the card image to start
// at the (much larger) detail size, and a <button> clips/traps that oversized
// image, breaking the reverse morph. data-flip-id pairs the image and title
// with their detail counterparts for that morph.
const props = defineProps<{ entry: McpEntry }>()
const emit = defineEmits<{ select: [string] }>()
</script>

<template>
  <article
    role="button"
    tabindex="0"
    class="flex cursor-pointer items-center gap-5 rounded-soft border border-black/5 bg-black/[0.02] p-4 transition hover:bg-black/[0.04]"
    @click="emit('select', props.entry.id)"
    @keydown.enter="emit('select', props.entry.id)"
    @keydown.space.prevent="emit('select', props.entry.id)"
  >
    <img
      :src="props.entry.image ?? mcpIcon(props.entry)"
      :alt="props.entry.name"
      :data-flip-id="`mcp-img-${props.entry.id}`"
      class="relative h-20 shrink-0 rounded-2xl"
      :class="props.entry.image ? 'w-auto max-w-40 object-contain' : 'w-20 object-cover'"
    />
    <div class="min-w-0">
      <h3
        :data-flip-id="`mcp-title-${props.entry.id}`"
        class="relative truncate text-base font-medium"
      >
        {{ props.entry.name }}
      </h3>
      <p class="mt-1.5 text-sm leading-relaxed opacity-60">{{ props.entry.description }}</p>
    </div>
  </article>
</template>

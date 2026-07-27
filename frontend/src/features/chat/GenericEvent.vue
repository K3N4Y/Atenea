<script lang="ts" setup>
import type { EventItem } from './types'

defineProps<{ item: EventItem }>()
</script>

<template>
  <div class="text-sm text-zinc-500" data-testid="generic-event">
    <span class="font-mono">[{{ item.eventKind }}]</span>
    {{ item.text }}
  </div>
</template>

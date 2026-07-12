<script lang="ts" setup>
import type { ToolItem } from '../stores/chat'
import ToolCall from './ToolCall.vue'

defineProps<{ items: ToolItem[] }>()
const emit = defineEmits<{ approve: [string]; deny: [string] }>()
</script>

<template>
  <section
    data-test="activity-group"
    aria-label="Agent activity"
    class="min-w-0"
  >
    <div
      v-for="(item, index) in items"
      :key="item.id"
      data-test="activity-row-container"
      class="relative min-w-0"
    >
      <span
        v-if="index < items.length - 1"
        data-test="activity-rail"
        aria-hidden="true"
        class="absolute bottom-[-10px] left-[9px] top-[18px] w-px bg-black/15"
      ></span>
      <ToolCall
        :item="item"
        @approve="emit('approve', $event)"
        @deny="emit('deny', $event)"
      />
    </div>
  </section>
</template>

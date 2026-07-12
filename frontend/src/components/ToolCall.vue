<script lang="ts" setup>
import { computed, ref } from 'vue'
import {
  PhCaretRight,
  PhCheck,
  PhCircleNotch,
  PhDiamond,
  PhX,
} from '@phosphor-icons/vue'
import type { ToolItem } from '../stores/chat'
import { activityPresentation } from '../lib/activityPresentation'
import DiffView from './DiffView.vue'

const props = defineProps<{ item: ToolItem }>()
const emit = defineEmits<{ approve: [string]; deny: [string] }>()

const expanded = ref(false)
const presentation = computed(() => activityPresentation(props.item))
const isPending = computed(() => props.item.status === 'pending')
const isDiff = computed(
  () =>
    (props.item.name === 'edit' || props.item.name === 'write') &&
    Boolean(props.item.diff),
)

function toggleExpanded(): void {
  if (presentation.value.expandable) expanded.value = !expanded.value
}
</script>

<template>
  <article
    class="relative min-w-0 py-1.5 pl-8 text-sm"
    data-test="activity-row"
  >
    <span
      class="absolute left-0 top-2.5 z-10 flex size-5 items-center justify-center rounded-full bg-paper"
      :class="{
        'text-red-600': item.status === 'failed',
        'text-amber-600': item.status === 'pending',
        'text-black/55': item.status === 'success',
        'text-accent': item.status === 'running',
      }"
      :data-status="item.status"
      aria-hidden="true"
    >
      <PhCircleNotch
        v-if="item.status === 'running'"
        :size="14"
        weight="bold"
        class="animate-spin [animation-duration:0.7s]"
      />
      <PhDiamond
        v-else-if="item.status === 'pending'"
        :size="13"
        weight="fill"
      />
      <PhCheck v-else-if="item.status === 'success'" :size="14" weight="bold" />
      <PhX v-else :size="14" weight="bold" />
    </span>

    <button
      v-if="presentation.expandable && !isPending"
      type="button"
      data-test="activity-summary"
      class="group flex w-full min-w-0 items-center gap-2 rounded px-1 py-0.5 text-left outline-none transition hover:bg-black/[0.035] focus-visible:ring-2 focus-visible:ring-accent/60"
      :aria-expanded="expanded"
      :aria-label="presentation.accessibleLabel"
      @click="toggleExpanded"
    >
      <PhCaretRight
        :size="12"
        weight="bold"
        class="shrink-0 opacity-35 transition-transform duration-150 ease-snappy group-hover:opacity-65"
        :class="{ 'rotate-90': expanded }"
        aria-hidden="true"
      />
      <span class="shrink-0 font-semibold">{{ presentation.action }}</span>
      <span
        v-if="presentation.target"
        data-test="activity-target"
        class="min-w-0 truncate opacity-75"
        :title="presentation.target"
        >{{ presentation.target }}</span
      >
      <span
        v-if="presentation.compactResult"
        class="ml-auto shrink-0 text-xs opacity-50"
        >{{ presentation.compactResult }}</span
      >
    </button>

    <div
      v-else
      class="flex min-w-0 items-center gap-2 px-1 py-0.5"
      :aria-label="presentation.accessibleLabel"
    >
      <span class="w-3 shrink-0" aria-hidden="true"></span>
      <span class="shrink-0 font-semibold">{{ presentation.action }}</span>
      <span
        v-if="presentation.target"
        data-test="activity-target"
        class="min-w-0 truncate opacity-75"
        :title="presentation.target"
        >{{ presentation.target }}</span
      >
      <span
        v-if="presentation.compactResult"
        class="ml-auto shrink-0 text-xs opacity-50"
        >{{ presentation.compactResult }}</span
      >
    </div>

    <div v-if="isPending" class="mt-2 flex flex-wrap gap-2 pl-6">
      <button
        type="button"
        data-action="approve"
        class="rounded-soft bg-accent px-3 py-1 text-xs font-medium text-white transition hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/60 focus-visible:ring-offset-2 active:scale-[0.97]"
        @click="emit('approve', item.callID)"
      >
        Aprobar
      </button>
      <button
        type="button"
        data-action="deny"
        class="rounded-soft bg-black/[0.06] px-3 py-1 text-xs font-medium transition hover:bg-black/[0.1] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-black/30 focus-visible:ring-offset-2 active:scale-[0.97]"
        @click="emit('deny', item.callID)"
      >
        Denegar
      </button>
    </div>

    <div v-if="expanded" class="mt-2 pl-6">
      <DiffView v-if="isDiff" :diff="item.diff" />
      <p
        v-else-if="item.error"
        class="whitespace-pre-wrap break-words text-xs text-red-700"
      >
        {{ item.error }}
      </p>
      <pre
        v-else-if="item.output"
        class="overflow-x-auto whitespace-pre-wrap break-words text-xs opacity-80"
        >{{ item.output }}</pre
      >
    </div>
  </article>
</template>

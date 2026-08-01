<script lang="ts" setup>
import { computed, ref, watch } from 'vue'
import {
  PhFile,
  PhCircleNotch,
  PhCheck,
  PhX,
  PhWarning,
  PhCaretRight,
} from '@phosphor-icons/vue'
import type { ToolItem } from './types'
import { basename } from '../../lib/path'
import DiffView from '../../components/DiffView.vue'

const props = defineProps<{ item: ToolItem }>()

// edit/write carry a UI-only unified diff. Without one, the component falls
// back to plain output.
const isDiff = computed(
  () =>
    (props.item.name === 'edit' || props.item.name === 'write') &&
    !!props.item.diff,
)

// approve/deny carry the callID up (MessageList -> ChatView -> store): the
// component stays presentational, without touching the store.
const emit = defineEmits<{ approve: [string]; deny: [string] }>()

// Tool Read (identity §10): "Reading"/"Read" plus only the file name.
const isRead = computed(() => props.item.name === 'read')

function inputPath(input: unknown): string {
  if (!input || typeof input !== 'object') return ''
  const o = input as Record<string, unknown>
  const v = o.path ?? o.file_path ?? o.filename ?? o.file
  return typeof v === 'string' ? v : ''
}
const fileName = computed(() => basename(inputPath(props.item.input)))
const readLabel = computed(() =>
  props.item.status === 'running' ? 'Reading' : 'Read',
)

// Tool Skill shows only the name, not the complete SKILL.md contents.
const isSkill = computed(() => props.item.name === 'skill')

function inputName(input: unknown): string {
  if (!input || typeof input !== 'object') return ''
  const o = input as Record<string, unknown>
  return typeof o.name === 'string' ? o.name : ''
}
const skillName = computed(() => inputName(props.item.input))

// Tool Glob shows only the pattern, not the file list.
const isGlob = computed(() => props.item.name === 'glob')

function inputPattern(input: unknown): string {
  if (!input || typeof input !== 'object') return ''
  const o = input as Record<string, unknown>
  return typeof o.pattern === 'string' ? o.pattern : ''
}
const globPattern = computed(() => inputPattern(props.item.input))

// Command the model wants to run (bash): shown next to the permission buttons
// so the user knows what they are approving.
function inputCommand(input: unknown): string {
  if (!input || typeof input !== 'object') return ''
  const o = input as Record<string, unknown>
  const v = o.command ?? o.cmd
  return typeof v === 'string' ? v : ''
}
const command = computed(() => inputCommand(props.item.input))
const isPending = computed(() => props.item.status === 'pending')
const isBash = computed(() => props.item.name === 'bash')
const canExpandBash = computed(
  () => isBash.value && !isPending.value && props.item.status !== 'running',
)
const expanded = ref(false)

watch(
  () => props.item.status,
  (status, previous) => {
    if (isBash.value && status !== previous) expanded.value = false
  },
)
</script>

<template>
  <div v-if="isRead" class="flex items-center gap-2 text-sm opacity-70">
    <PhFile :size="16" weight="regular" />
    <span>{{ readLabel }}</span>
    <span v-if="fileName" class="opacity-90">{{ fileName }}</span>
  </div>

  <div v-else-if="isSkill" class="flex items-center gap-2 text-sm opacity-70">
    <PhCheck
      v-if="item.status === 'success'"
      :size="16"
      weight="bold"
      class="opacity-50"
    />
    <PhCircleNotch
      v-else-if="item.status === 'running'"
      :size="16"
      weight="bold"
      class="animate-spin text-accent [animation-duration:0.7s]"
    />
    <PhX v-else :size="16" weight="bold" class="text-accent" />
    <span class="font-medium">skill</span>
    <span v-if="skillName" class="opacity-90">{{ skillName }}</span>
  </div>

  <div v-else-if="isGlob" class="flex items-center gap-2 text-sm opacity-70">
    <span class="font-medium">glob</span>
    <span v-if="globPattern" class="opacity-90">{{ globPattern }}</span>
  </div>

  <!-- Remaining tools (edit/diff/echo...) use their own background (§8). -->
  <div v-else class="rounded-soft bg-black/[0.04] px-4 py-3 text-sm">
    <component
      :is="canExpandBash ? 'button' : 'div'"
      :type="canExpandBash ? 'button' : undefined"
      class="flex w-full items-center gap-2 text-left"
      :class="{ 'transition hover:opacity-80': canExpandBash }"
      :aria-expanded="canExpandBash ? expanded : undefined"
      @click="canExpandBash && (expanded = !expanded)"
    >
      <PhCaretRight
        v-if="canExpandBash"
        :size="14"
        weight="bold"
        class="shrink-0 transition-transform duration-200 ease-snappy"
        :class="{ 'rotate-90': expanded }"
      />
      <!-- A short status-icon crossfade keeps state transitions smooth. -->
      <Transition name="tool-icon" mode="out-in">
        <PhCircleNotch
          v-if="item.status === 'running'"
          key="running"
          :size="16"
          weight="bold"
          class="animate-spin text-accent [animation-duration:0.7s]"
        />
        <PhWarning
          v-else-if="isPending"
          key="pending"
          :size="16"
          weight="bold"
          class="text-accent"
        />
        <PhCheck
          v-else-if="item.status === 'success'"
          key="success"
          :size="16"
          weight="bold"
          class="opacity-50"
        />
        <PhX v-else key="fail" :size="16" weight="bold" class="text-accent" />
      </Transition>
      <span class="font-medium">{{ item.name || 'tool' }}</span>
      <span class="opacity-50">{{ item.status }}</span>
      <code
        v-if="isBash && !isPending && command"
        class="min-w-0 truncate opacity-80"
        >{{ command }}</code
      >
    </component>

    <!-- Ask-before-run: shows the command and asks for approval before running. -->
    <template v-if="isPending">
      <pre
        v-if="command"
        class="mt-2 overflow-x-auto whitespace-pre-wrap break-words text-xs opacity-80"
        >{{ command }}</pre
      >
      <div class="mt-3 flex gap-2">
        <button
          type="button"
          data-action="approve"
          class="rounded-soft bg-accent px-3 py-1 text-xs font-medium text-white transition hover:opacity-90 active:scale-[0.97]"
          @click="emit('approve', item.callID)"
        >
          Aprobar
        </button>
        <button
          type="button"
          data-action="deny"
          class="rounded-soft bg-black/[0.06] px-3 py-1 text-xs font-medium transition hover:bg-black/[0.1] active:scale-[0.97]"
          @click="emit('deny', item.callID)"
        >
          Denegar
        </button>
      </div>
    </template>

    <template v-if="!isBash || expanded">
      <DiffView v-if="isDiff" :diff="item.diff" class="mt-2" />
      <pre
        v-else-if="item.output"
        class="mt-2 overflow-x-auto whitespace-pre-wrap break-words text-xs opacity-80"
        >{{ item.output }}</pre
      >
      <p v-if="item.error" class="mt-2 text-xs text-accent">{{ item.error }}</p>
    </template>
  </div>
</template>

<style scoped>
/* Status icon crossfade: opacity only, avoiding expensive WebKit blur. */
.tool-icon-enter-active,
.tool-icon-leave-active {
  transition: opacity 0.12s var(--ease-snappy);
}
.tool-icon-enter-from,
.tool-icon-leave-to {
  opacity: 0;
}
</style>

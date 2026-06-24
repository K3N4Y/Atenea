<script lang="ts" setup>
import { computed } from 'vue'
import {
  PhFile,
  PhCircleNotch,
  PhCheck,
  PhX,
  PhWarning,
} from '@phosphor-icons/vue'
import type { ToolItem } from '../stores/chat'
import { basename } from '../lib/path'
import DiffView from './DiffView.vue'

const props = defineProps<{ item: ToolItem }>()

// edit/write traen un diff unificado (solo-UI): se renderiza coloreado con
// DiffView en vez del output plano. Sin diff (sesiones viejas, bash) cae al <pre>.
const isDiff = computed(
  () => (props.item.name === 'edit' || props.item.name === 'write') && !!props.item.diff,
)

// approve/deny carry the callID up (MessageList -> ChatView -> store): the
// component stays presentational, without touching the store.
const emit = defineEmits<{ approve: [string]; deny: [string] }>()

// Tool Read (identidad §10): "Reading"/"Read" + solo el nombre del archivo.
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

// Command the model wants to run (bash): shown next to the permission buttons
// so the user knows what they are approving.
function inputCommand(input: unknown): string {
  if (!input || typeof input !== 'object') return ''
  const v = (input as Record<string, unknown>).command
  return typeof v === 'string' ? v : ''
}
const command = computed(() => inputCommand(props.item.input))
const isPending = computed(() => props.item.status === 'pending')
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
      <PhWarning
        v-else-if="isPending"
        :size="16"
        weight="bold"
        class="text-accent"
      />
      <PhCheck
        v-else-if="item.status === 'success'"
        :size="16"
        weight="bold"
        class="opacity-50"
      />
      <PhX v-else :size="16" weight="bold" class="text-accent" />
      <span class="font-medium">{{ item.name || 'tool' }}</span>
      <span class="opacity-50">{{ item.status }}</span>
    </div>

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
          class="rounded-soft bg-accent px-3 py-1 text-xs font-medium text-white transition hover:opacity-90"
          @click="emit('approve', item.callID)"
        >
          Aprobar
        </button>
        <button
          type="button"
          data-action="deny"
          class="rounded-soft bg-black/[0.06] px-3 py-1 text-xs font-medium transition hover:bg-black/[0.1]"
          @click="emit('deny', item.callID)"
        >
          Denegar
        </button>
      </div>
    </template>

    <DiffView v-if="isDiff" :diff="item.diff" class="mt-2" />
    <pre
      v-else-if="item.output"
      class="mt-2 overflow-x-auto whitespace-pre-wrap break-words text-xs opacity-80"
      >{{ item.output }}</pre
    >
    <p v-if="item.error" class="mt-2 text-xs text-accent">{{ item.error }}</p>
  </div>
</template>

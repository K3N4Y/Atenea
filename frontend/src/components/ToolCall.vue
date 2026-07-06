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

// Tool Skill: muestra solo el nombre, no el contenido completo del SKILL.md.
const isSkill = computed(() => props.item.name === 'skill')

function inputName(input: unknown): string {
  if (!input || typeof input !== 'object') return ''
  const o = input as Record<string, unknown>
  return typeof o.name === 'string' ? o.name : ''
}
const skillName = computed(() => inputName(props.item.input))

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

  <div v-else-if="isSkill" class="flex items-center gap-2 text-sm opacity-70">
    <PhCheck v-if="item.status === 'success'" :size="16" weight="bold" class="opacity-50" />
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

  <!-- Resto de tools (edit/diff/echo...): bloque con su propio fondo (§8). -->
  <div v-else class="rounded-soft bg-black/[0.04] px-4 py-3 text-sm">
    <div class="flex items-center gap-2">
      <!-- Crossfade corto del icono de estado (~120ms): el swap instantaneo entre
           spinner/warning/check/x se siente brusco. mode="out-in" sobre un unico
           icono por estado (gateado por :key) mantiene un solo nodo en el flex. -->
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

    <DiffView v-if="isDiff" :diff="item.diff" class="mt-2" />
    <pre
      v-else-if="item.output"
      class="mt-2 overflow-x-auto whitespace-pre-wrap break-words text-xs opacity-80"
      >{{ item.output }}</pre
    >
    <p v-if="item.error" class="mt-2 text-xs text-accent">{{ item.error }}</p>
  </div>
</template>

<style scoped>
/* Crossfade del icono de estado (ver template): solo opacidad, sin blur (caro en
   webkit). ease-snappy = misma curva que el resto de la app. */
.tool-icon-enter-active,
.tool-icon-leave-active {
  transition: opacity 0.12s var(--ease-snappy);
}
.tool-icon-enter-from,
.tool-icon-leave-to {
  opacity: 0;
}
</style>

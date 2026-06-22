<script lang="ts" setup>
import { ref, computed } from 'vue'
import { PhBrain, PhCaretRight, PhCaretDown } from '@phosphor-icons/vue'
import type { ReasoningItem } from '../stores/chat'
import { formatThinkingDuration } from '../lib/duration'
import { useSmoothText } from '../lib/useSmoothText'

// Streaming de pensamiento (identidad §9): durante la generacion se revela suave
// caracter a caracter (useSmoothText) y solo se ven las ultimas 4 lineas; al
// terminar la escritura colapsa a una linea "Thought <tiempo>" que el usuario
// puede expandir para ver el contenido completo.
const props = defineProps<{ item: ReasoningItem }>()
const expanded = ref(false)
const { visible, done } = useSmoothText(
  () => props.item.text,
  () => props.item.streaming,
)

const preview = computed(() =>
  visible.value.split('\n').filter((l) => l.trim().length > 0).slice(-4).join('\n'),
)
const doneLabel = computed(() => `Thought ${formatThinkingDuration(props.item.durationMs ?? 0)}`)
</script>

<template>
  <div class="text-sm opacity-70">
    <template v-if="!done">
      <div class="mb-1 flex items-center gap-2">
        <PhBrain :size="16" weight="regular" class="animate-pulse text-accent" />
        <span>Thinking</span>
      </div>
      <p class="whitespace-pre-wrap break-words pl-6 opacity-80">{{ preview }}</p>
    </template>

    <template v-else>
      <button
        type="button"
        class="flex items-center gap-2 text-left transition hover:opacity-100"
        :aria-expanded="expanded"
        @click="expanded = !expanded"
      >
        <component :is="expanded ? PhCaretDown : PhCaretRight" :size="14" weight="bold" />
        <span>{{ doneLabel }}</span>
      </button>
      <p v-if="expanded" class="mt-2 whitespace-pre-wrap break-words pl-6 opacity-80">{{ item.text }}</p>
    </template>
  </div>
</template>

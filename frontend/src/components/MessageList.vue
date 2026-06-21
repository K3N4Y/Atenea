<script lang="ts" setup>
import { ref, computed, watch, nextTick } from 'vue'
import type { TurnItem } from '../stores/chat'
import UserMessage from './UserMessage.vue'
import AssistantMessage from './AssistantMessage.vue'
import ThinkingBlock from './ThinkingBlock.vue'
import ToolCall from './ToolCall.vue'

// Flujo continuo y plano de la conversacion (identidad §8). Despacha cada item
// del log a su componente segun el tipo. Recibe los items del store via prop
// para mantenerse presentacional.
const props = defineProps<{ items: TurnItem[] }>()

const scroller = ref<HTMLElement | null>(null)

function scrollToBottom() {
  const el = scroller.value
  if (el) el.scrollTop = el.scrollHeight
}

// Firma del ultimo item: cambia tanto al crecer el log como al avanzar el
// streaming (texto/pensamiento que se acumula, tool que cambia de estado).
const tail = computed(() => {
  const last = props.items.at(-1)
  if (!last) return ''
  if (last.kind === 'tool') return `${last.status}:${last.output}`
  return last.text
})

// Auto-scroll al fondo. El flush 'post' espera al render del DOM.
watch(
  () => [props.items.length, tail.value] as const,
  () => nextTick(scrollToBottom),
  { flush: 'post' },
)
</script>

<template>
  <div ref="scroller" class="flex-1 overflow-y-auto">
    <div class="mx-auto flex w-full max-w-3xl flex-col gap-5 px-6 py-10">
      <template v-if="props.items.length">
        <template v-for="item in props.items" :key="item.id">
          <UserMessage v-if="item.kind === 'user'" :item="item" />
          <AssistantMessage v-else-if="item.kind === 'assistant'" :item="item" />
          <ThinkingBlock v-else-if="item.kind === 'reasoning'" :item="item" />
          <ToolCall v-else-if="item.kind === 'tool'" :item="item" />
        </template>
      </template>

      <div
        v-else
        class="flex min-h-[60vh] flex-col items-center justify-center text-center"
      >
        <p class="text-2xl tracking-tight opacity-90">atenea</p>
        <p class="mt-2 text-sm opacity-50">Ask anything to get started.</p>
      </div>
    </div>
  </div>
</template>

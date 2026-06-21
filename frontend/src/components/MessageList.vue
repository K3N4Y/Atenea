<script lang="ts" setup>
import { ref, watch, nextTick } from 'vue'
import type { ChatMessage } from '../stores/chat'
import MessageBubble from './MessageBubble.vue'

// Flujo continuo y plano de la conversacion (identidad §8). Recibe los mensajes
// del store via prop para mantenerse presentacional.
const props = defineProps<{ messages: ChatMessage[] }>()

const scroller = ref<HTMLElement | null>(null)

function scrollToBottom() {
  const el = scroller.value
  if (el) el.scrollTop = el.scrollHeight
}

// Auto-scroll cuando crece el log o cuando el ultimo mensaje sigue creciendo
// (streaming de Text.Delta). El flush 'post' espera al render del DOM.
watch(
  () => [props.messages.length, props.messages.at(-1)?.text] as const,
  () => nextTick(scrollToBottom),
  { flush: 'post' },
)
</script>

<template>
  <div ref="scroller" class="flex-1 overflow-y-auto">
    <div class="mx-auto flex w-full max-w-3xl flex-col gap-6 px-6 py-10">
      <template v-if="props.messages.length">
        <MessageBubble
          v-for="m in props.messages"
          :key="m.id"
          :message="m"
        />
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

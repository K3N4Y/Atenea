<script lang="ts" setup>
import { ref, computed, watch, nextTick } from 'vue'
import type { Component } from 'vue'
import gsap from 'gsap'
import type { TurnItem } from '../stores/chat'
import { prefersReducedMotion } from '../lib/motion'
import UserMessage from './UserMessage.vue'
import AssistantMessage from './AssistantMessage.vue'
import ThinkingBlock from './ThinkingBlock.vue'
import ToolCall from './ToolCall.vue'

// Flujo continuo y plano de la conversacion (identidad §8). Despacha cada item
// del log a su componente segun el tipo. Recibe los items del store via prop
// para mantenerse presentacional.
const props = defineProps<{ items: TurnItem[] }>()

// Forwards the permission decisions emitted by ToolCall up to the store (via
// ChatView); the other components of the registry do not emit these events.
const emit = defineEmits<{ approve: [string]; deny: [string] }>()

const registry: Record<TurnItem['kind'], Component> = {
  user: UserMessage,
  assistant: AssistantMessage,
  reasoning: ThinkingBlock,
  tool: ToolCall,
}

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

// Entrada suave de cada item nuevo (GSAP): aparece con un leve ascenso. La
// clave por id evita reanimar el item en streaming, que solo crece. Respeta
// prefers-reduced-motion saltando la animacion.
function onEnter(el: Element, done: () => void) {
  if (prefersReducedMotion()) {
    done()
    return
  }
  gsap.fromTo(
    el,
    { opacity: 0, y: 8 },
    { opacity: 1, y: 0, duration: 0.35, ease: 'power2.out', onComplete: done },
  )
}
</script>

<template>
  <div
    ref="scroller"
    role="log"
    aria-live="polite"
    aria-label="Conversation"
    class="flex-1 overflow-y-auto"
  >
    <div class="mx-auto w-full max-w-3xl px-6 py-10">
      <TransitionGroup
        v-if="props.items.length"
        tag="div"
        class="flex flex-col gap-5"
        :css="false"
        @enter="onEnter"
      >
        <component
          :is="registry[item.kind]"
          v-for="item in props.items"
          :key="item.id"
          :item="item"
          @approve="emit('approve', $event)"
          @deny="emit('deny', $event)"
        />
      </TransitionGroup>

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

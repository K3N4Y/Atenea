<script lang="ts" setup>
import type { ChatMessage } from '../stores/chat'

// Anatomia del mensaje (identidad §8): el usuario lleva un fondo sutil sin
// borde; la IA se renderiza directamente sobre el Blanco Papel, sin contenedor.
// En el MVP el contenido es texto plano; el render de Markdown llega en Fase 3.
const props = defineProps<{ message: ChatMessage }>()
</script>

<template>
  <div
    class="flex w-full"
    :class="props.message.role === 'user' ? 'justify-end' : 'justify-start'"
  >
    <div
      v-if="props.message.role === 'user'"
      class="max-w-[80%] whitespace-pre-wrap break-words rounded-soft bg-black/[0.04] px-5 py-3 leading-relaxed"
    >{{ props.message.text }}</div>

    <div
      v-else
      class="w-full whitespace-pre-wrap break-words leading-relaxed"
    >{{ props.message.text
      }}<span
        v-if="props.message.streaming"
        class="ml-0.5 inline-block h-[1.05em] w-[2px] translate-y-[2px] animate-pulse bg-accent align-middle"
        aria-hidden="true"
      ></span>
    </div>
  </div>
</template>

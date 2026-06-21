<script lang="ts" setup>
import type { AssistantItem } from '../stores/chat'
import MarkdownContent from './MarkdownContent.vue'

// Respuesta de la IA (identidad §8): se renderiza directamente sobre el Blanco
// Papel, sin contenedor propio. Durante el streaming se muestra texto plano
// con un caret de acento (evita reparsear Markdown en cada delta); al finalizar
// se renderiza el Markdown completo.
defineProps<{ item: AssistantItem }>()
</script>

<template>
  <div class="w-full">
    <template v-if="item.streaming">
      <span class="whitespace-pre-wrap break-words leading-relaxed">{{ item.text }}</span>
      <span
        class="ml-0.5 inline-block h-[1.05em] w-[2px] animate-pulse bg-accent align-middle"
        aria-hidden="true"
      ></span>
    </template>
    <MarkdownContent v-else :text="item.text" />
  </div>
</template>

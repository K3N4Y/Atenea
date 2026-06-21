<script lang="ts" setup>
import type { AssistantItem } from '../stores/chat'
import MarkdownContent from './MarkdownContent.vue'

// Respuesta de la IA (identidad §8): se renderiza directamente sobre el Blanco
// Papel, sin contenedor propio. Durante el streaming muestra un caret de acento.
defineProps<{ item: AssistantItem }>()
</script>

<template>
  <div class="w-full">
    <MarkdownContent :text="item.text" />
    <span
      v-if="item.streaming"
      class="mt-1 inline-block h-[1.05em] w-[2px] animate-pulse bg-accent align-middle"
      aria-hidden="true"
    ></span>
  </div>
</template>

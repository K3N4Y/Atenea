<script lang="ts" setup>
import type { AssistantItem } from '../stores/chat'
import MarkdownContent from './MarkdownContent.vue'
import { useSmoothText } from '../lib/useSmoothText'

// Respuesta de la IA (identidad §8): se renderiza directamente sobre el Blanco
// Papel, sin contenedor propio. Durante el streaming se muestra texto plano
// revelado caracter a caracter (useSmoothText) con un caret de acento (evita
// reparsear Markdown en cada delta); al terminar la escritura se renderiza el
// Markdown completo. El swap espera a `done`, no a Text.Ended, para no cortar
// la animacion con un flash de Markdown.
const props = defineProps<{ item: AssistantItem }>()
const { visible, done } = useSmoothText(
  () => props.item.text,
  () => props.item.streaming,
)
</script>

<template>
  <div class="w-full">
    <template v-if="!done">
      <span class="whitespace-pre-wrap break-words leading-relaxed">{{
        visible
      }}</span>
      <span
        class="ml-0.5 inline-block h-[1.05em] w-[2px] animate-caret-blink bg-accent align-middle"
        aria-hidden="true"
      ></span>
    </template>
    <MarkdownContent v-else :text="item.text" />
  </div>
</template>

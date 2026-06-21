<script lang="ts" setup>
import type { UserItem } from '../stores/chat'

// Mensaje del usuario (identidad §8): fondo sutil, sin borde, redondeado 24px.
defineProps<{ item: UserItem }>()
</script>

<template>
  <div class="flex w-full justify-end">
    <div
      class="max-w-[80%] whitespace-pre-wrap break-words rounded-soft bg-black/[0.04] px-5 py-3 leading-relaxed"
    >{{ item.text }}</div>
  </div>
</template>

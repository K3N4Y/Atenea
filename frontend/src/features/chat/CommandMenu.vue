<script lang="ts" setup>
import type { Command } from '../../lib/command'

// CommandMenu: la lista flotante del slash-menu de comandos del composer.
// Presentacional: recibe los comandos (items) y el indice activo, emite select con
// el nombre al elegir y hover al pasar el mouse (para sincronizar el indice con el
// teclado). Usa mousedown.prevent en vez de click: asi no roba el foco del textarea
// antes de que el composer inserte el comando. Espejo de MentionMenu (archivos).
defineProps<{ items: Command[]; activeIndex: number }>()
const emit = defineEmits<{ select: [name: string]; hover: [index: number] }>()
</script>

<template>
  <ul
    role="listbox"
    aria-label="Comandos"
    class="max-h-56 overflow-y-auto rounded-soft border border-black/10 bg-paper py-1 shadow-lg"
  >
    <li
      v-for="(item, i) in items"
      :key="item.name"
      role="option"
      :aria-selected="i === activeIndex"
      class="flex cursor-pointer items-baseline gap-2 px-3 py-1.5 text-sm transition"
      :class="i === activeIndex ? 'bg-accent/10' : 'hover:bg-black/[0.04]'"
      @mousedown.prevent="emit('select', item.name)"
      @mouseenter="emit('hover', i)"
    >
      <span class="shrink-0 font-medium">/{{ item.name }}</span>
      <span class="min-w-0 flex-1 truncate text-xs opacity-50">{{
        item.description
      }}</span>
    </li>
  </ul>
</template>

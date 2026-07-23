<script lang="ts" setup>
import { basename } from '../../lib/path'

// MentionMenu: la lista flotante del @-menu de archivos del composer.
// Presentacional: recibe las rutas (items) y el indice activo, emite select al
// elegir y hover al pasar el mouse (para sincronizar el indice con el teclado).
// Usa mousedown.prevent en vez de click: asi no roba el foco del textarea antes
// de que el composer inserte la ruta.
defineProps<{ items: string[]; activeIndex: number }>()
const emit = defineEmits<{ select: [path: string]; hover: [index: number] }>()
</script>

<template>
  <ul
    role="listbox"
    aria-label="Archivos"
    class="max-h-56 overflow-y-auto rounded-soft border border-black/10 bg-paper py-1 shadow-lg"
  >
    <li
      v-for="(item, i) in items"
      :key="item"
      role="option"
      :aria-selected="i === activeIndex"
      class="flex cursor-pointer items-baseline gap-2 px-3 py-1.5 text-sm transition"
      :class="i === activeIndex ? 'bg-accent/10' : 'hover:bg-black/[0.04]'"
      @mousedown.prevent="emit('select', item)"
      @mouseenter="emit('hover', i)"
    >
      <span class="shrink-0 font-medium">{{ basename(item) }}</span>
      <span class="min-w-0 flex-1 truncate text-xs opacity-50">{{ item }}</span>
    </li>
  </ul>
</template>

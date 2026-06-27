<script lang="ts" setup>
import { PhCircle, PhCircleNotch, PhCheckCircle } from '@phosphor-icons/vue'
import type { TodoItem } from '../stores/chat'

// TodoList es el checklist de tareas en vivo (estilo Codex): una tarjeta flotante
// arriba a la derecha que pinta los todos del agente. Presentacional: recibe la
// lista por prop. Lista vacia => no renderiza nada (el v-if de la raiz). El estado
// se distingue por icono y por data-status (para tachar el completado).
defineProps<{ todos: TodoItem[] }>()
</script>

<template>
  <div
    v-if="todos.length"
    aria-label="Tareas"
    class="pointer-events-auto max-h-[60vh] w-64 overflow-auto rounded-soft border border-black/10 bg-paper/95 p-3 text-sm shadow-lg backdrop-blur"
  >
    <ul class="flex flex-col gap-1.5">
      <li
        v-for="(todo, i) in todos"
        :key="i"
        :data-status="todo.status"
        class="flex items-start gap-2"
      >
        <PhCheckCircle
          v-if="todo.status === 'completed'"
          :size="16"
          weight="fill"
          class="mt-0.5 shrink-0 opacity-50"
        />
        <PhCircleNotch
          v-else-if="todo.status === 'in_progress'"
          :size="16"
          weight="bold"
          class="mt-0.5 shrink-0 animate-spin text-accent"
        />
        <PhCircle v-else :size="16" class="mt-0.5 shrink-0 opacity-40" />
        <span
          class="min-w-0 flex-1"
          :class="
            todo.status === 'completed'
              ? 'line-through opacity-50'
              : todo.status === 'in_progress'
                ? 'font-medium'
                : ''
          "
          >{{ todo.content }}</span
        >
      </li>
    </ul>
  </div>
</template>

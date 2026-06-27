<script lang="ts" setup>
import { PhWarningCircle, PhX } from '@phosphor-icons/vue'

// Surfaces a chat error that the store tracks in `errorText` (provider failure
// or a cut stream) which until now was swallowed. Presentational: receives the
// message via prop and emits `dismiss`; the store/view own the state.
//
// Voz y microcopy (identidad §11): visible but calm, never alarming. Uses the
// orange accent sparingly (same as a tool failure in ToolCall) instead of an
// aggressive red, and keeps the user in control with a dismiss affordance.
defineProps<{ message: string | null }>()
const emit = defineEmits<{ dismiss: [] }>()
</script>

<template>
  <div
    v-if="message"
    role="alert"
    class="flex items-start gap-2 rounded-soft border border-accent/20 bg-accent/[0.06] px-4 py-3 text-sm"
  >
    <PhWarningCircle
      :size="18"
      weight="regular"
      class="mt-0.5 shrink-0 text-accent"
    />
    <p class="min-w-0 flex-1 break-words leading-relaxed opacity-80">
      {{ message }}
    </p>
    <button
      type="button"
      aria-label="Dismiss error"
      class="-mr-1 -mt-0.5 flex h-7 w-7 shrink-0 items-center justify-center rounded-full transition hover:bg-black/[0.05] active:scale-95"
      @click="emit('dismiss')"
    >
      <PhX :size="16" weight="regular" />
    </button>
  </div>
</template>

<script lang="ts" setup>
import { ref, computed, nextTick } from 'vue'
import gsap from 'gsap'
import { PhArrowUp, PhStop } from '@phosphor-icons/vue'
import { prefersReducedMotion } from '../lib/motion'

// Composer del MVP: textarea que crece con el contenido + boton pildora. El
// naranja de acento se reserva para enviar (identidad §3). Es presentacional:
// emite send/stop y recibe `running` por prop.
const props = defineProps<{ running: boolean }>()
const emit = defineEmits<{ send: [text: string]; stop: [] }>()

const text = ref('')
const textarea = ref<HTMLTextAreaElement | null>(null)
const sendButton = ref<HTMLElement | null>(null)

const canSend = computed(() => text.value.trim().length > 0)

const MAX_HEIGHT = 200

function autoGrow() {
  const el = textarea.value
  if (!el) return
  el.style.height = 'auto'
  el.style.height = `${Math.min(el.scrollHeight, MAX_HEIGHT)}px`
}

function submit() {
  if (!canSend.value) return
  // Microinteraccion de envio (GSAP): un leve rebote en el boton.
  if (sendButton.value && !prefersReducedMotion()) {
    gsap.fromTo(sendButton.value, { scale: 0.85 }, { scale: 1, duration: 0.3, ease: 'back.out(3)' })
  }
  emit('send', text.value)
  text.value = ''
  nextTick(autoGrow)
}

function onKeydown(e: KeyboardEvent) {
  // Enter envia; Shift+Enter inserta salto de linea.
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    submit()
  }
}
</script>

<template>
  <div class="px-6 pb-6 pt-2">
    <div class="mx-auto w-full max-w-3xl">
      <p
        v-if="props.running"
        role="status"
        class="mb-2 flex items-center gap-2 pl-2 text-xs opacity-60"
      >
        <span class="h-1.5 w-1.5 animate-pulse rounded-full bg-accent"></span>
        Working · you can stop anytime
      </p>

      <div
        class="flex items-end gap-2 rounded-soft bg-black/[0.04] p-2 pl-4 transition focus-within:ring-2 focus-within:ring-accent/20"
      >
        <textarea
          ref="textarea"
          v-model="text"
          rows="1"
          aria-label="Message atenea"
          placeholder="Message atenea"
          class="max-h-[200px] flex-1 resize-none bg-transparent py-2 leading-relaxed placeholder:opacity-40 focus:outline-none"
          @input="autoGrow"
          @keydown="onKeydown"
        ></textarea>

        <button
          v-if="props.running"
          type="button"
          aria-label="Stop"
          class="flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-accent text-paper transition hover:opacity-90"
          @click="emit('stop')"
        >
          <PhStop :size="20" weight="fill" />
        </button>
        <button
          v-else
          ref="sendButton"
          type="button"
          aria-label="Send"
          :disabled="!canSend"
          class="flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-accent text-paper transition hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-30"
          @click="submit"
        >
          <PhArrowUp :size="20" weight="bold" />
        </button>
      </div>
    </div>
  </div>
</template>

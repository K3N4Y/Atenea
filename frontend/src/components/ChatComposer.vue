<script lang="ts" setup>
import { ref, computed, nextTick } from 'vue'
import gsap from 'gsap'
import { PhArrowUp, PhStop } from '@phosphor-icons/vue'
import { prefersReducedMotion } from '../lib/motion'
import { detectMention, filterFiles, applyMention } from '../lib/mention'
import type { MentionQuery } from '../lib/mention'
import MentionMenu from './MentionMenu.vue'

// Composer del MVP: textarea que crece con el contenido + boton pildora. El
// naranja de acento se reserva para enviar (identidad §3). Es presentacional:
// emite send/stop y recibe `running` por prop. `files` alimenta el @-menu de
// archivos del workspace (la vista lo pasa desde el store).
const props = withDefaults(
  defineProps<{
    running: boolean
    mode?: 'normal' | 'plan'
    files?: string[]
  }>(),
  {
    mode: 'normal',
    files: () => [],
  },
)
const emit = defineEmits<{
  send: [text: string]
  stop: []
  'toggle-mode': []
}>()

const text = ref('')
const textarea = ref<HTMLTextAreaElement | null>(null)
const sendButton = ref<HTMLElement | null>(null)

const canSend = computed(() => text.value.trim().length > 0)

const MAX_HEIGHT = 200

// @-menciones de archivos: detectMention lee el token bajo el caret, filterFiles
// ordena candidatos del workspace (props.files) y applyMention inserta la ruta
// elegida. El menu se cierra poniendo la mencion inactiva; al volver a escribir
// (input) se recalcula y reabre. MENU_LIMIT acota cuantos candidatos se muestran.
const MENU_LIMIT = 8
const INACTIVE: MentionQuery = { active: false, query: '', start: -1, end: -1 }
const mention = ref<MentionQuery>(INACTIVE)
const activeIndex = ref(0)

const suggestions = computed(() =>
  mention.value.active
    ? filterFiles(props.files, mention.value.query, MENU_LIMIT)
    : [],
)
const menuOpen = computed(
  () => mention.value.active && suggestions.value.length > 0,
)

function autoGrow() {
  const el = textarea.value
  if (!el) return
  el.style.height = 'auto'
  el.style.height = `${Math.min(el.scrollHeight, MAX_HEIGHT)}px`
}

// refreshMention recalcula el token @ bajo el caret tras escribir o mover el
// cursor; reinicia la opcion activa al tope de la lista filtrada.
function refreshMention() {
  const el = textarea.value
  const caret = el
    ? (el.selectionStart ?? text.value.length)
    : text.value.length
  mention.value = detectMention(text.value, caret)
  activeIndex.value = 0
}

function onInput() {
  autoGrow()
  refreshMention()
}

function closeMenu() {
  mention.value = INACTIVE
}

function onBlur() {
  // Cerrar al salir del composer. Las opciones usan mousedown.prevent, asi que
  // elegir una NO dispara blur antes de insertar.
  closeMenu()
}

function moveActive(delta: number) {
  const n = suggestions.value.length
  if (n === 0) return
  activeIndex.value = (activeIndex.value + delta + n) % n
}

function onHover(i: number) {
  activeIndex.value = i
}

// choose inserta la ruta elegida en el token @, cierra el menu y deja el caret
// justo despues, listo para seguir escribiendo.
function choose(path: string) {
  const { text: next, caret } = applyMention(text.value, mention.value, path)
  text.value = next
  closeMenu()
  nextTick(() => {
    autoGrow()
    const el = textarea.value
    if (el) {
      el.focus()
      el.selectionStart = el.selectionEnd = caret
    }
  })
}

function submit() {
  if (!canSend.value) return
  // Microinteraccion de envio (GSAP): un leve rebote en el boton.
  if (sendButton.value && !prefersReducedMotion()) {
    gsap.fromTo(
      sendButton.value,
      { scale: 0.85 },
      { scale: 1, duration: 0.3, ease: 'back.out(3)' },
    )
  }
  emit('send', text.value)
  text.value = ''
  nextTick(autoGrow)
}

function onKeydown(e: KeyboardEvent) {
  // Con el @-menu abierto el teclado lo controla: flechas navegan, Enter/Tab
  // eligen, Escape cierra. Solo si esta cerrado, Enter envia.
  if (menuOpen.value) {
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        moveActive(1)
        return
      case 'ArrowUp':
        e.preventDefault()
        moveActive(-1)
        return
      case 'Enter':
      case 'Tab':
        e.preventDefault()
        choose(suggestions.value[activeIndex.value])
        return
      case 'Escape':
        e.preventDefault()
        closeMenu()
        return
    }
  }
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

      <!-- Toggle de modo: alterna entre envio normal y modo plan (el agente
           planifica antes de ejecutar). El acento marca el modo plan activo. -->
      <div class="mb-2 pl-2">
        <button
          type="button"
          data-action="toggle-mode"
          :aria-pressed="props.mode === 'plan'"
          class="rounded-full px-3 py-1 text-xs transition"
          :class="
            props.mode === 'plan'
              ? 'bg-accent text-paper'
              : 'bg-black/[0.06] hover:bg-black/[0.09]'
          "
          @click="emit('toggle-mode')"
        >
          Plan
        </button>
      </div>

      <div
        class="relative flex items-end gap-2 rounded-soft bg-black/[0.04] p-2 pl-4 transition focus-within:ring-2 focus-within:ring-accent/20"
      >
        <!-- @-menu de archivos: flota sobre el composer mientras se escribe un
             token @ con candidatos del workspace. -->
        <MentionMenu
          v-if="menuOpen"
          :items="suggestions"
          :active-index="activeIndex"
          class="absolute inset-x-0 bottom-full z-30 mb-2"
          @select="choose"
          @hover="onHover"
        />
        <textarea
          ref="textarea"
          v-model="text"
          rows="1"
          aria-label="Message atenea"
          placeholder="Message atenea"
          :aria-expanded="menuOpen"
          class="max-h-[200px] flex-1 resize-none bg-transparent py-2 leading-relaxed placeholder:opacity-40 focus:outline-none"
          @input="onInput"
          @keydown="onKeydown"
          @click="refreshMention"
          @blur="onBlur"
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

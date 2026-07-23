<script lang="ts" setup>
import { ref, computed, nextTick } from 'vue'
import gsap from 'gsap'
import { PhArrowUp, PhStop } from '@phosphor-icons/vue'
import { prefersReducedMotion } from './motion'
import { detectMention, filterFiles, applyMention } from './mention'
import type { MentionQuery } from './mention'
import { detectCommand, filterCommands, applyCommand } from './command'
import type { CommandQuery, Command } from './command'
import MentionMenu from './MentionMenu.vue'
import CommandMenu from './CommandMenu.vue'

// Composer del MVP: textarea que crece con el contenido + boton pildora. El
// naranja de acento se reserva para enviar (identidad §3). Es presentacional:
// emite send/stop y recibe `running` por prop. `files` alimenta el @-menu de
// archivos del workspace y `commands` el slash-menu de comandos (la vista los pasa
// desde el store).
const props = withDefaults(
  defineProps<{
    running: boolean
    mode?: 'normal' | 'plan'
    files?: string[]
    commands?: Command[]
  }>(),
  {
    mode: 'normal',
    files: () => [],
    commands: () => [],
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

// Dos menus flotantes sobre el composer comparten el mismo patron: detectan un
// token bajo el caret, filtran candidatos y, al elegir, reemplazan el token. Son
// mutuamente excluyentes por construccion (el @-menu necesita un token "@"; el
// slash-menu necesita "/" como primer caracter), asi que como mucho hay uno abierto.
//   - @-menciones de archivos: detectMention + filterFiles(props.files) + applyMention.
//   - slash-commands: detectCommand + filterCommands(props.commands) + applyCommand.
// El teclado (flechas/Enter/Tab/Escape) opera sobre el que este abierto via menuOpen
// y activeIndex compartido. MENU_LIMIT acota cuantos candidatos se muestran.
const MENU_LIMIT = 8
const INACTIVE_MENTION: MentionQuery = {
  active: false,
  query: '',
  start: -1,
  end: -1,
}
const INACTIVE_COMMAND: CommandQuery = {
  active: false,
  query: '',
  start: -1,
  end: -1,
}
const mention = ref<MentionQuery>(INACTIVE_MENTION)
const command = ref<CommandQuery>(INACTIVE_COMMAND)
const activeIndex = ref(0)

const fileSuggestions = computed(() =>
  mention.value.active
    ? filterFiles(props.files, mention.value.query, MENU_LIMIT)
    : [],
)
const commandSuggestions = computed(() =>
  command.value.active
    ? filterCommands(props.commands, command.value.query, MENU_LIMIT)
    : [],
)
const mentionMenuOpen = computed(
  () => mention.value.active && fileSuggestions.value.length > 0,
)
const commandMenuOpen = computed(
  () => command.value.active && commandSuggestions.value.length > 0,
)
const menuOpen = computed(() => mentionMenuOpen.value || commandMenuOpen.value)
// Cuantas opciones tiene el menu abierto, para acotar la navegacion con flechas.
const activeCount = computed(() =>
  commandMenuOpen.value
    ? commandSuggestions.value.length
    : fileSuggestions.value.length,
)

function autoGrow() {
  const el = textarea.value
  if (!el) return
  el.style.height = 'auto'
  el.style.height = `${Math.min(el.scrollHeight, MAX_HEIGHT)}px`
}

// refreshMenus recalcula ambos tokens (@ y /) bajo el caret tras escribir o mover
// el cursor; reinicia la opcion activa al tope de la lista filtrada. Ambos detectores
// son baratos y mutuamente excluyentes, asi que recalcular los dos es seguro.
function refreshMenus() {
  const el = textarea.value
  const caret = el
    ? (el.selectionStart ?? text.value.length)
    : text.value.length
  mention.value = detectMention(text.value, caret)
  command.value = detectCommand(text.value, caret)
  activeIndex.value = 0
}

function onInput() {
  autoGrow()
  refreshMenus()
}

function closeMenu() {
  mention.value = INACTIVE_MENTION
  command.value = INACTIVE_COMMAND
}

function onBlur() {
  // Cerrar al salir del composer. Las opciones usan mousedown.prevent, asi que
  // elegir una NO dispara blur antes de insertar.
  closeMenu()
}

function moveActive(delta: number) {
  const n = activeCount.value
  if (n === 0) return
  activeIndex.value = (activeIndex.value + delta + n) % n
}

function onHover(i: number) {
  activeIndex.value = i
}

// replaceText fija el texto nuevo, cierra los menus y deja el caret en su sitio,
// listo para seguir escribiendo. Lo comparten la insercion de archivo y la de comando.
function replaceText(next: string, caret: number) {
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

// chooseFile inserta la ruta elegida en el token @; chooseCommand inserta el
// "/comando " elegido en el token /. chooseActive elige sobre el menu abierto.
function chooseFile(path: string) {
  const { text: next, caret } = applyMention(text.value, mention.value, path)
  replaceText(next, caret)
}

function chooseCommand(name: string) {
  const { text: next, caret } = applyCommand(text.value, command.value, name)
  replaceText(next, caret)
}

function chooseActive() {
  if (commandMenuOpen.value) {
    chooseCommand(commandSuggestions.value[activeIndex.value].name)
  } else if (mentionMenuOpen.value) {
    chooseFile(fileSuggestions.value[activeIndex.value])
  }
}

function submit() {
  if (!canSend.value) return
  // Microinteraccion de envio (GSAP): un leve rebote en el boton.
  if (sendButton.value && !prefersReducedMotion()) {
    gsap.fromTo(
      sendButton.value,
      { scale: 0.85 },
      { scale: 1, duration: 0.3, ease: 'back.out(1.5)' },
    )
  }
  emit('send', text.value)
  text.value = ''
  nextTick(autoGrow)
}

function onKeydown(e: KeyboardEvent) {
  // Con un menu abierto (@ o /) el teclado lo controla: flechas navegan, Enter/Tab
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
        chooseActive()
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
             token @ con candidatos del workspace. Popover origin-aware: crece
             hacia arriba desde el composer, asi que escala desde abajo
             (origin-bottom). La transicion solo corre al abrir/cerrar: el v-if
             togglea por menuOpen, no por keystroke (escribir solo cambia las
             props del componente ya montado, no lo remonta). -->
        <Transition
          enter-active-class="transition duration-150 ease-snappy"
          enter-from-class="opacity-0 scale-[0.97]"
          leave-active-class="transition duration-[120ms] ease-snappy"
          leave-to-class="opacity-0 scale-[0.97]"
        >
          <MentionMenu
            v-if="mentionMenuOpen"
            :items="fileSuggestions"
            :active-index="activeIndex"
            class="absolute inset-x-0 bottom-full z-30 mb-2 origin-bottom"
            @select="chooseFile"
            @hover="onHover"
          />
        </Transition>
        <!-- slash-menu de comandos: flota sobre el composer mientras se escribe un
             "/" al inicio del mensaje con los comandos disponibles. Mismo patron
             que el @-menu: popover origin-bottom, anima solo open/close. -->
        <Transition
          enter-active-class="transition duration-150 ease-snappy"
          enter-from-class="opacity-0 scale-[0.97]"
          leave-active-class="transition duration-[120ms] ease-snappy"
          leave-to-class="opacity-0 scale-[0.97]"
        >
          <CommandMenu
            v-if="commandMenuOpen"
            :items="commandSuggestions"
            :active-index="activeIndex"
            class="absolute inset-x-0 bottom-full z-30 mb-2 origin-bottom"
            @select="chooseCommand"
            @hover="onHover"
          />
        </Transition>
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
          @click="refreshMenus"
          @blur="onBlur"
        ></textarea>

        <button
          v-if="props.running"
          type="button"
          aria-label="Stop"
          class="flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-accent text-paper transition hover:opacity-90 active:scale-95"
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
          class="flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-accent text-paper transition hover:opacity-90 active:scale-95 disabled:cursor-not-allowed disabled:opacity-30"
          @click="submit"
        >
          <PhArrowUp :size="20" weight="bold" />
        </button>
      </div>
    </div>
  </div>
</template>

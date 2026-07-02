<script lang="ts" setup>
import { ref, computed, watch } from 'vue'
import {
  PhCloud,
  PhDesktop,
  PhArrowClockwise,
  PhCheck,
} from '@phosphor-icons/vue'

// Selector de provider del panel de ajustes (pestania General). Presentacional: la
// config vigente llega por props y emite `apply` (kind, baseURL, model) y
// `list-models` (baseURL); el panel cablea esos eventos al store
// (setProvider/listModels). OpenRouter usa la key del entorno; local apunta a un
// endpoint OpenAI-compatible (LM Studio, Ollama) sin secreto. Convencion del repo:
// data-* como selectores de test, eventos hacia arriba (como WorkspacePicker).
const props = withDefaults(
  defineProps<{
    providerKind?: string
    baseURL?: string
    model?: string
    availableModels?: string[]
  }>(),
  { providerKind: '', baseURL: '', model: '', availableModels: () => [] },
)

const emit = defineEmits<{
  apply: [kind: string, baseURL: string, model: string]
  'list-models': [baseURL: string]
}>()

// Presets de endpoints locales conocidos: LM Studio y Ollama exponen una API
// OpenAI-compatible en estos puertos por defecto.
const presets = [
  { id: 'lmstudio', label: 'LM Studio', url: 'http://localhost:1234/v1' },
  { id: 'ollama', label: 'Ollama', url: 'http://localhost:11434/v1' },
] as const

// Estado local del formulario: editar no toca el store hasta "Aplicar". Arranca de
// las props (la config vigente) y se re-sincroniza si cambian (al reabrir el panel
// o tras aplicar). El kind cae a openrouter si la config aun no fija uno (demo/vacio).
const kind = ref(props.providerKind === 'local' ? 'local' : 'openrouter')
const url = ref(props.baseURL)
const selectedModel = ref(props.model)

watch(
  () => [props.providerKind, props.baseURL, props.model],
  ([k, b, m]) => {
    kind.value = k === 'local' ? 'local' : 'openrouter'
    url.value = b
    selectedModel.value = m
  },
)

const isLocal = computed(() => kind.value === 'local')

function choosePreset(presetUrl: string): void {
  url.value = presetUrl
}

function chooseModel(id: string): void {
  selectedModel.value = id
}

function requestModels(): void {
  emit('list-models', url.value)
}

function apply(): void {
  emit('apply', kind.value, url.value, selectedModel.value)
}
</script>

<template>
  <div class="flex flex-col gap-6">
    <div>
      <h2 class="text-lg tracking-tight">Modelo</h2>
      <p class="mt-1 text-sm opacity-50">
        Elige el proveedor del modelo. OpenRouter usa tu API key del entorno; un
        proveedor local (LM Studio, Ollama) corre en tu maquina, sin clave.
      </p>
    </div>

    <!-- Eleccion de proveedor: OpenRouter (nube) vs Local (maquina). -->
    <div class="flex gap-3">
      <button
        type="button"
        data-provider-option="openrouter"
        :aria-pressed="kind === 'openrouter' ? 'true' : 'false'"
        class="flex flex-1 items-center gap-2 rounded-soft border px-4 py-3 text-left text-sm transition active:scale-[0.99]"
        :class="
          kind === 'openrouter'
            ? 'border-accent/40 bg-accent/10 text-accent'
            : 'border-black/10 opacity-70 hover:bg-black/[0.04] hover:opacity-100'
        "
        @click="kind = 'openrouter'"
      >
        <PhCloud :size="18" weight="regular" class="shrink-0" />
        <span class="min-w-0">
          <span class="block font-medium">OpenRouter</span>
          <span class="block text-xs opacity-60">Gateway en la nube</span>
        </span>
      </button>

      <button
        type="button"
        data-provider-option="local"
        :aria-pressed="kind === 'local' ? 'true' : 'false'"
        class="flex flex-1 items-center gap-2 rounded-soft border px-4 py-3 text-left text-sm transition active:scale-[0.99]"
        :class="
          kind === 'local'
            ? 'border-accent/40 bg-accent/10 text-accent'
            : 'border-black/10 opacity-70 hover:bg-black/[0.04] hover:opacity-100'
        "
        @click="kind = 'local'"
      >
        <PhDesktop :size="18" weight="regular" class="shrink-0" />
        <span class="min-w-0">
          <span class="block font-medium">Local</span>
          <span class="block text-xs opacity-60">LM Studio, Ollama…</span>
        </span>
      </button>
    </div>

    <!-- Config del endpoint local: baseURL con presets y carga del catalogo. -->
    <div v-if="isLocal" class="flex flex-col gap-3">
      <label class="block">
        <span class="mb-1 block text-[11px] uppercase tracking-wide opacity-40">
          Endpoint (baseURL)
        </span>
        <input
          v-model="url"
          data-baseurl-input
          type="text"
          placeholder="http://localhost:1234/v1"
          class="w-full rounded-soft bg-black/[0.04] px-3 py-2 text-sm transition focus:outline-none focus:ring-2 focus:ring-accent/20"
        />
      </label>

      <div class="flex flex-wrap items-center gap-2">
        <button
          v-for="preset in presets"
          :key="preset.id"
          type="button"
          :data-preset="preset.id"
          class="rounded-full bg-black/[0.05] px-3 py-1 text-xs opacity-80 transition hover:bg-black/[0.08] hover:opacity-100 active:scale-[0.97]"
          @click="choosePreset(preset.url)"
        >
          {{ preset.label }}
        </button>
        <button
          type="button"
          data-list-models
          class="ml-auto flex items-center gap-1.5 rounded-full bg-black/[0.05] px-3 py-1 text-xs opacity-80 transition hover:bg-black/[0.08] hover:opacity-100 active:scale-[0.97]"
          @click="requestModels"
        >
          <PhArrowClockwise :size="14" weight="regular" />
          Cargar modelos
        </button>
      </div>

      <div v-if="availableModels.length" class="flex flex-wrap gap-2">
        <button
          v-for="m in availableModels"
          :key="m"
          type="button"
          :data-model-option="m"
          class="flex items-center gap-1.5 rounded-full px-3 py-1 text-xs transition active:scale-[0.97]"
          :class="
            m === selectedModel
              ? 'bg-accent/10 text-accent'
              : 'bg-black/[0.05] opacity-80 hover:bg-black/[0.08] hover:opacity-100'
          "
          @click="chooseModel(m)"
        >
          <PhCheck v-if="m === selectedModel" :size="12" weight="bold" />
          {{ m }}
        </button>
      </div>
    </div>

    <!-- Modelo activo: editable a mano (OpenRouter) o rellenado por los chips (local). -->
    <label class="block">
      <span class="mb-1 block text-[11px] uppercase tracking-wide opacity-40">
        Modelo
      </span>
      <input
        v-model="selectedModel"
        data-model-input
        type="text"
        placeholder="p. ej. openrouter/free o qwen2.5-coder"
        class="w-full rounded-soft bg-black/[0.04] px-3 py-2 text-sm transition focus:outline-none focus:ring-2 focus:ring-accent/20"
      />
    </label>

    <div>
      <button
        type="button"
        data-apply-provider
        class="rounded-full bg-accent px-5 py-2 text-sm font-medium text-white transition hover:opacity-90 active:scale-[0.98]"
        @click="apply"
      >
        Aplicar
      </button>
    </div>
  </div>
</template>

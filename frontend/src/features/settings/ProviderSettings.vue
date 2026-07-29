<script lang="ts" setup>
import { ref, computed } from 'vue'
import {
  PhCloud,
  PhDesktop,
  PhArrowClockwise,
  PhCheck,
  PhPlus,
  PhX,
} from '@phosphor-icons/vue'
import type { DeviceLogin, ProviderRow } from './provider'
import { CONNECT_DEVICE_CODE } from './provider'

// Selector de modelo del panel de ajustes (pestania General). Presentacional: el
// catalogo y la seleccion vigente llegan por props y las acciones salen como
// eventos (`select`, `connect`, `forget`, `declare`, `refresh`, `list-models`); el
// panel las cablea al store y baja el error resultante por `error`, igual que la
// pestania de MCPs.
//
// El catalogo es el MISMO que usa la app de terminal (providers.json), asi que
// elegir un modelo aca lo cambia alla y al reves. Convencion del repo: data-* como
// selectores de test, eventos hacia arriba (como WorkspacePicker).
const props = withDefaults(
  defineProps<{
    providers?: ProviderRow[]
    activeProviderID?: string
    activeModel?: string
    discoveredModels?: string[]
    refreshing?: boolean
    error?: string
    // pendingLogin is the device-code login in flight, if any. Nothing in it is a
    // secret: the code is single-use and worthless without the account it is
    // approved from, which is why it can be shown.
    pendingLogin?: DeviceLogin | null
    // startingLogin is the provider whose code is still being minted. That round
    // trip is the only part of the flow with nothing on screen, so the button that
    // opened it has to say so and stop taking clicks.
    startingLogin?: string
  }>(),
  {
    providers: () => [],
    activeProviderID: '',
    activeModel: '',
    discoveredModels: () => [],
    refreshing: false,
    error: '',
    pendingLogin: null,
    startingLogin: '',
  },
)

const emit = defineEmits<{
  select: [providerID: string, model: string]
  connect: [providerID: string, apiKey: string]
  login: [providerID: string]
  'cancel-login': [providerID: string]
  'open-login-page': [providerID: string]
  forget: [providerID: string]
  declare: [name: string, baseURL: string, model: string]
  refresh: []
  'list-models': [baseURL: string]
}>()

// A provider connected by logging in has no key to paste: it gets a button and,
// once a code exists, the code. Which of the two a row is comes from the backend —
// inferring it from the id here is how the UI ends up one release behind the
// catalog, showing a password field for something that has no password.
function isLogin(provider: ProviderRow): boolean {
  return provider.connectKind === CONNECT_DEVICE_CODE
}

function loginFor(provider: ProviderRow): DeviceLogin | null {
  if (props.pendingLogin?.providerID === provider.id) return props.pendingLogin
  return null
}

// deviceCodeDeadline says when the code dies, as a clock time. It is the same
// sentence the terminal panel writes, because a user who has both open must not be
// told two different things about one code — and an absolute instant is the reading
// that stays true whether or not anything redraws, which a countdown is not.
//
// Nothing is rendered when the authorization server named no expiry, or named one
// this build cannot parse: a deadline invented here would be worse than silence.
function deviceCodeDeadline(login: DeviceLogin | null): string {
  const expiresAt = login?.expiresAt
  if (!expiresAt) return ''
  const at = new Date(expiresAt)
  if (Number.isNaN(at.getTime())) return ''
  return ` the code expires at ${at.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}`
}

// Presets de endpoints locales conocidos: LM Studio y Ollama exponen una API
// OpenAI-compatible en estos puertos por defecto.
const presets = [
  { id: 'lmstudio', label: 'LM Studio', url: 'http://localhost:1234/v1' },
  { id: 'ollama', label: 'Ollama', url: 'http://localhost:11434/v1' },
] as const

// Una key por fila: escribirla no toca nada hasta pulsar Conectar, y no se
// persiste en el cliente (el backend la guarda cifrada donde ya guarda las demas).
const apiKeys = ref<Record<string, string>>({})

// Formulario de endpoint propio, cerrado por defecto: es el caso raro frente a
// elegir un provider del catalogo.
const adding = ref(false)
const endpointName = ref('')
const endpointURL = ref('')
const endpointModel = ref('')

const canDeclare = computed(
  () => endpointName.value.trim() !== '' && endpointURL.value.trim() !== '',
)

// Sin credencial en ninguna parte la app arranca sobre el fake sin red, que no es
// una fila del catalogo: entonces nada aparece elegido y hay que decir por que, o
// el usuario chatea con respuestas de guion creyendo que le habla un modelo.
const unconfigured = computed(
  () =>
    props.providers.length > 0 &&
    !props.providers.some((provider) => provider.id === props.activeProviderID),
)

function declare(): void {
  emit('declare', endpointName.value, endpointURL.value, endpointModel.value)
}

// resetForm lo llama el panel via ref cuando la declaracion sale bien: el
// formulario se vacia al cerrarse, no al pulsar.
function resetForm(): void {
  adding.value = false
  endpointName.value = ''
  endpointURL.value = ''
  endpointModel.value = ''
}
defineExpose({ resetForm })
</script>

<template>
  <div class="flex flex-col gap-6">
    <div>
      <h2 class="text-lg tracking-tight">Model</h2>
      <p class="mt-1 text-sm opacity-50">
        Pick the provider and model atenea talks to. API keys stay on this
        device, and the selection is shared with the terminal app.
      </p>
    </div>

    <p
      v-if="error"
      role="alert"
      data-provider-error
      class="text-sm text-red-700"
    >
      {{ error }}
    </p>

    <p
      v-if="unconfigured"
      data-provider-unconfigured
      class="rounded-soft bg-black/[0.04] px-4 py-3 text-sm opacity-70"
    >
      No provider connected — replies come from the offline demo. Add an API key
      below, or a local endpoint, and pick a model.
    </p>

    <!-- Una tarjeta por provider configurado. La activa se resalta; los modelos
         son chips y elegir uno lo activa. -->
    <div class="flex flex-col gap-3">
      <article
        v-for="provider in providers"
        :key="provider.id"
        :data-provider-row="provider.id"
        class="rounded-soft border p-4 transition"
        :class="
          provider.id === activeProviderID
            ? 'border-accent/40 bg-accent/[0.06]'
            : 'border-black/10'
        "
      >
        <header class="flex items-center gap-2">
          <component
            :is="provider.builtIn ? PhCloud : PhDesktop"
            :size="18"
            weight="regular"
            class="shrink-0 opacity-60"
          />
          <h3 class="min-w-0 truncate text-sm font-medium">
            {{ provider.name }}
          </h3>
          <span
            v-if="provider.connectable"
            :data-provider-key-state="provider.id"
            class="rounded-full px-2 py-0.5 text-[11px]"
            :class="
              provider.connected
                ? 'bg-emerald-600/10 text-emerald-700'
                : 'bg-black/[0.05] opacity-60'
            "
          >
            {{ provider.connected ? 'Key stored' : 'No key' }}
          </span>
          <button
            v-if="!provider.builtIn"
            type="button"
            :data-forget-provider="provider.id"
            :aria-label="`Remove ${provider.name}`"
            class="ml-auto flex h-7 w-7 shrink-0 items-center justify-center rounded-full transition hover:bg-black/[0.06] active:scale-95"
            @click="emit('forget', provider.id)"
          >
            <PhX :size="14" weight="regular" />
          </button>
        </header>

        <div v-if="provider.models?.length" class="mt-3 flex flex-wrap gap-2">
          <button
            v-for="model in provider.models"
            :key="model"
            type="button"
            :data-model-option="`${provider.id}:${model}`"
            :aria-pressed="
              provider.id === activeProviderID && model === activeModel
                ? 'true'
                : 'false'
            "
            class="flex items-center gap-1.5 rounded-full px-3 py-1 text-xs transition active:scale-[0.97]"
            :class="
              provider.id === activeProviderID && model === activeModel
                ? 'bg-accent/10 text-accent'
                : 'bg-black/[0.05] opacity-80 hover:bg-black/[0.08] hover:opacity-100'
            "
            @click="emit('select', provider.id, model)"
          >
            <PhCheck
              v-if="provider.id === activeProviderID && model === activeModel"
              :size="12"
              weight="bold"
            />
            {{ model }}
          </button>
        </div>
        <p v-else class="mt-3 text-xs opacity-50">
          No models yet — reload models to see what this endpoint serves.
        </p>

        <!-- A subscription is approved on another device, so this row shows the
             code and where to enter it instead of asking for a secret. Opening the
             browser is offered, never required: this machine may not have one. -->
        <div
          v-if="provider.connectable && isLogin(provider)"
          :data-login-form="provider.id"
          class="mt-3"
        >
          <template v-if="loginFor(provider)">
            <p class="text-sm opacity-70">
              Open
              <button
                type="button"
                :data-open-login-page="provider.id"
                class="text-accent underline underline-offset-2 transition hover:opacity-80"
                @click="emit('open-login-page', provider.id)"
              >
                {{ loginFor(provider)?.verificationURI }}
              </button>
              and enter this code:
            </p>
            <p
              :data-login-code="provider.id"
              class="mt-2 select-all font-mono text-2xl tracking-[0.2em]"
            >
              {{ loginFor(provider)?.userCode }}
            </p>
            <div class="mt-3 flex items-center gap-3">
              <span :data-login-status="provider.id" class="text-xs opacity-50"
                >Waiting for approval in your browser…{{
                  deviceCodeDeadline(loginFor(provider))
                }}</span
              >
              <button
                type="button"
                :data-cancel-login="provider.id"
                class="rounded-full bg-black/[0.05] px-3 py-1 text-xs opacity-80 transition hover:bg-black/[0.08] hover:opacity-100 active:scale-[0.97]"
                @click="emit('cancel-login', provider.id)"
              >
                Cancel
              </button>
            </div>
          </template>
          <button
            v-else
            type="button"
            :data-start-login="provider.id"
            :disabled="startingLogin === provider.id"
            class="rounded-full bg-ink px-4 py-2 text-sm text-paper transition hover:opacity-90 active:scale-[0.98] disabled:opacity-40"
            @click="emit('login', provider.id)"
          >
            <template v-if="startingLogin === provider.id"
              >Signing in…</template
            >
            <template v-else-if="provider.connected">Sign in again</template>
            <template v-else>Sign in with ChatGPT</template>
          </button>
        </div>

        <!-- Pasting the key is the only way to connect a key provider from the
             desktop: there is no /connect here to type it into. -->
        <form
          v-if="provider.connectable && !isLogin(provider)"
          :data-connect-form="provider.id"
          class="mt-3 flex items-center gap-2"
          @submit.prevent="
            emit('connect', provider.id, apiKeys[provider.id] ?? '')
          "
        >
          <input
            v-model="apiKeys[provider.id]"
            :data-api-key-input="provider.id"
            type="password"
            autocomplete="off"
            :placeholder="provider.connected ? 'Replace API key' : 'API key'"
            class="min-w-0 flex-1 rounded-soft bg-black/[0.04] px-3 py-2 text-sm transition focus:outline-none focus:ring-2 focus:ring-accent/20"
          />
          <button
            type="submit"
            :data-connect-provider="provider.id"
            :disabled="!(apiKeys[provider.id] ?? '').trim()"
            class="shrink-0 rounded-full bg-ink px-4 py-2 text-sm text-paper transition hover:opacity-90 active:scale-[0.98] disabled:opacity-40"
          >
            {{ provider.connected ? 'Replace' : 'Connect' }}
          </button>
        </form>
      </article>
    </div>

    <div class="flex flex-wrap items-center gap-2">
      <button
        type="button"
        data-refresh-models
        :disabled="refreshing"
        class="flex items-center gap-1.5 rounded-full bg-black/[0.05] px-3 py-1.5 text-xs opacity-80 transition hover:bg-black/[0.08] hover:opacity-100 active:scale-[0.97] disabled:opacity-40"
        @click="emit('refresh')"
      >
        <PhArrowClockwise :size="14" weight="regular" />
        {{ refreshing ? 'Reloading…' : 'Reload models' }}
      </button>
      <button
        type="button"
        data-add-endpoint
        :aria-expanded="adding ? 'true' : 'false'"
        class="ml-auto flex items-center gap-1.5 rounded-full bg-black/[0.05] px-3 py-1.5 text-xs opacity-80 transition hover:bg-black/[0.08] hover:opacity-100 active:scale-[0.97]"
        @click="adding = !adding"
      >
        <PhPlus :size="14" weight="regular" />
        Local endpoint
      </button>
    </div>

    <!-- Endpoint propio: un runtime en esta maquina (LM Studio, Ollama) o una
         gateway OpenAI-compatible. Sin secreto: no piden key. -->
    <form
      v-if="adding"
      data-endpoint-form
      class="grid gap-4 rounded-soft border border-black/5 bg-black/[0.02] p-5"
      @submit.prevent="declare"
    >
      <label class="grid gap-1.5 text-sm">
        Name
        <input
          v-model="endpointName"
          data-endpoint-name
          required
          placeholder="LM Studio"
          class="rounded-lg border border-black/10 bg-paper px-3 py-2"
        />
      </label>
      <label class="grid gap-1.5 text-sm">
        Endpoint <span class="opacity-50">(OpenAI-compatible base URL)</span>
        <input
          v-model="endpointURL"
          data-endpoint-url
          required
          placeholder="http://localhost:1234/v1"
          class="rounded-lg border border-black/10 bg-paper px-3 py-2 font-mono text-xs"
        />
      </label>
      <div class="flex flex-wrap items-center gap-2">
        <button
          v-for="preset in presets"
          :key="preset.id"
          type="button"
          :data-preset="preset.id"
          class="rounded-full bg-black/[0.05] px-3 py-1 text-xs opacity-80 transition hover:bg-black/[0.08] hover:opacity-100 active:scale-[0.97]"
          @click="endpointURL = preset.url"
        >
          {{ preset.label }}
        </button>
        <button
          type="button"
          data-list-models
          class="ml-auto flex items-center gap-1.5 rounded-full bg-black/[0.05] px-3 py-1 text-xs opacity-80 transition hover:bg-black/[0.08] hover:opacity-100 active:scale-[0.97]"
          @click="emit('list-models', endpointURL)"
        >
          <PhArrowClockwise :size="14" weight="regular" />
          Load models
        </button>
      </div>
      <div v-if="discoveredModels.length" class="flex flex-wrap gap-2">
        <button
          v-for="model in discoveredModels"
          :key="model"
          type="button"
          :data-discovered-model="model"
          class="flex items-center gap-1.5 rounded-full px-3 py-1 text-xs transition active:scale-[0.97]"
          :class="
            model === endpointModel
              ? 'bg-accent/10 text-accent'
              : 'bg-black/[0.05] opacity-80 hover:bg-black/[0.08] hover:opacity-100'
          "
          @click="endpointModel = model"
        >
          <PhCheck v-if="model === endpointModel" :size="12" weight="bold" />
          {{ model }}
        </button>
      </div>
      <label class="grid gap-1.5 text-sm">
        Model <span class="opacity-50">(optional)</span>
        <input
          v-model="endpointModel"
          data-endpoint-model
          placeholder="qwen2.5-coder"
          class="rounded-lg border border-black/10 bg-paper px-3 py-2"
        />
      </label>
      <button
        type="submit"
        data-declare-endpoint
        :disabled="!canDeclare"
        class="w-fit rounded-full bg-accent px-5 py-2 text-sm font-medium text-white transition hover:opacity-90 active:scale-[0.98] disabled:opacity-40"
      >
        Add endpoint
      </button>
    </form>
  </div>
</template>

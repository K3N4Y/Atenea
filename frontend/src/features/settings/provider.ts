import { ref } from 'vue'
import {
  ListModels,
  Model,
  ProviderConfig,
  SetProvider,
} from '../../../wailsjs/go/main/App'

// Owns provider selection and model discovery while allowing the chat Pinia
// store to expose and persist these refs under its existing public contract.
export function createProviderState() {
  const model = ref('')
  const providerKind = ref('')
  const baseURL = ref('')
  const availableModels = ref<string[]>([])

  async function loadModel(): Promise<void> {
    try {
      model.value = await Model()
    } catch {
      model.value = ''
    }
  }

  async function loadProvider(): Promise<void> {
    try {
      const config = await ProviderConfig()
      providerKind.value = config.kind
      baseURL.value = config.baseURL
      model.value = config.model
    } catch {
      // Preserve rehydrated configuration when the backend is unavailable.
    }
  }

  async function restoreProvider(): Promise<void> {
    if (providerKind.value) {
      try {
        await SetProvider(providerKind.value, baseURL.value, model.value)
        return
      } catch {
        // A stale persisted configuration falls back to the backend state.
      }
    }
    await loadProvider()
  }

  async function setProvider(
    kind: string,
    url: string,
    selectedModel: string,
  ): Promise<void> {
    await SetProvider(kind, url, selectedModel)
    providerKind.value = kind
    baseURL.value = url
    model.value = selectedModel
  }

  async function listModels(url: string): Promise<string[]> {
    try {
      availableModels.value = await ListModels(url)
    } catch {
      availableModels.value = []
    }
    return availableModels.value
  }

  return {
    model,
    providerKind,
    baseURL,
    availableModels,
    loadModel,
    loadProvider,
    restoreProvider,
    setProvider,
    listModels,
  }
}

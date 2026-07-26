import { ref } from 'vue'
import {
  ActiveProvider,
  ConnectProvider,
  DeclareEndpoint,
  ForgetProvider,
  ListModels,
  ProviderCatalog,
  RefreshModels,
  SelectModel,
} from '../../../wailsjs/go/main/App'
import type { main } from '../../../wailsjs/go/models'

export type ProviderRow = main.ProviderEntry

// Owns provider selection, connection and model discovery while allowing the chat
// Pinia store to expose these refs under its existing public contract.
//
// None of this is persisted in the browser: the selection lives in the shared
// providers.json the backend owns, which is also what the terminal app reads. The
// UI's job is to show it, not to remember it — a second copy in localStorage is
// how the two used to disagree.
export function createProviderState() {
  const model = ref('')
  const providerID = ref('')
  const providerName = ref('')
  const contextWindow = ref(0)
  const providers = ref<ProviderRow[]>([])

  async function loadProvider(): Promise<void> {
    try {
      const active = await ActiveProvider()
      providerID.value = active.providerID
      providerName.value = active.providerName
      model.value = active.model
      contextWindow.value = active.contextWindow
    } catch {
      // The backend is unavailable; leave what is on screen alone.
    }
  }

  async function loadProviders(): Promise<ProviderRow[]> {
    try {
      providers.value = await ProviderCatalog()
    } catch {
      providers.value = []
    }
    return providers.value
  }

  // refreshProviders asks every endpoint that supports discovery for its models.
  // A failing endpoint is a warning, not a failure: the rows that answered are in
  // the result, so the catalog is replaced either way.
  async function refreshProviders(): Promise<ProviderRow[]> {
    try {
      providers.value = await RefreshModels()
    } catch {
      await loadProviders()
    }
    return providers.value
  }

  async function selectModel(id: string, selected: string): Promise<void> {
    await SelectModel(id, selected)
    await loadProvider()
  }

  async function connectProvider(id: string, apiKey: string): Promise<void> {
    await ConnectProvider(id, apiKey)
    await Promise.all([loadProvider(), loadProviders()])
  }

  // declareEndpoint adds a local endpoint and returns its id. It does not select
  // it: choosing a model on it is the next step, which is the caller's to take.
  async function declareEndpoint(
    name: string,
    baseURL: string,
    selected: string,
  ): Promise<string> {
    const id = await DeclareEndpoint(name, baseURL, selected)
    await loadProviders()
    return id
  }

  async function forgetProvider(id: string): Promise<void> {
    await ForgetProvider(id)
    await loadProviders()
  }

  // listModels probes an endpoint before it is declared, so the "add endpoint"
  // form can offer the models that are there instead of asking for one by heart.
  async function listModels(baseURL: string): Promise<string[]> {
    return ListModels(baseURL)
  }

  return {
    model,
    providerID,
    providerName,
    contextWindow,
    providers,
    loadProvider,
    loadProviders,
    refreshProviders,
    selectModel,
    connectProvider,
    declareEndpoint,
    forgetProvider,
    listModels,
  }
}

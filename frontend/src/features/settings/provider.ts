import { ref } from 'vue'
import {
  ActiveProvider,
  AwaitProviderLogin,
  CancelProviderLogin,
  ConnectProvider,
  DeclareEndpoint,
  ForgetProvider,
  ListModels,
  OpenLoginPage,
  ProviderCatalog,
  RefreshModels,
  SelectModel,
  StartProviderLogin,
} from '../../../wailsjs/go/main/App'
import type { main } from '../../../wailsjs/go/models'

export type ProviderRow = main.ProviderEntry
export type DeviceLogin = main.DeviceLogin

// CONNECT_DEVICE_CODE mirrors the backend constant for the row that is connected
// by approving a code elsewhere. It is the only one named here because it is the
// only one branched on: everything else is a key field, and a second constant for
// the default arm would be a name nothing reads.
export const CONNECT_DEVICE_CODE = 'device_code'

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

  // pendingLogin is the device-code login on screen, if any. It lives here rather
  // than in the component because the wait outlives a component that unmounts, and
  // because cancelling has to reach the backend from wherever the UI ends.
  const pendingLogin = ref<DeviceLogin | null>(null)
  // startingLogin is the provider whose code is being minted right now. It is the
  // one window with nothing on screen to show that anything is happening, which is
  // exactly the window a second click lands in.
  const startingLogin = ref('')
  // loginAttempt numbers the attempts so a losing one cannot clean up after the one
  // that replaced it: the code on screen belongs to whoever painted it last.
  let loginAttempt = 0

  // startProviderLogin mints the code and shows it, then waits for the user to
  // approve it. The two halves are separate calls on purpose: the code has to be on
  // screen before anything blocks on a human.
  async function startProviderLogin(id: string): Promise<void> {
    // There is one login per provider, so every click after the first one is the
    // same login. A second that got through would mint a second code, retire the
    // first one server-side, and end with the first attempt's cleanup taking the
    // live code off screen while it is still approvable.
    if (startingLogin.value === id || pendingLogin.value?.providerID === id) {
      return
    }
    const attempt = ++loginAttempt
    startingLogin.value = id
    try {
      pendingLogin.value = await StartProviderLogin(id)
    } finally {
      if (startingLogin.value === id) startingLogin.value = ''
    }
    try {
      await AwaitProviderLogin(id)
    } finally {
      if (loginAttempt === attempt) pendingLogin.value = null
    }
    await Promise.all([loadProvider(), loadProviders()])
  }

  async function cancelProviderLogin(id: string): Promise<void> {
    pendingLogin.value = null
    await CancelProviderLogin(id)
  }

  // openLoginPage is an affordance, never a dependency: the code and the URL are on
  // screen, and the machine running atenea may have no browser to open.
  async function openLoginPage(id: string): Promise<void> {
    await OpenLoginPage(id)
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
    pendingLogin,
    startingLogin,
    loadProvider,
    loadProviders,
    refreshProviders,
    selectModel,
    connectProvider,
    startProviderLogin,
    cancelProviderLogin,
    openLoginPage,
    declareEndpoint,
    forgetProvider,
    listModels,
  }
}

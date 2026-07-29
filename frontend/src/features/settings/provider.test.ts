import { describe, it, expect, vi, beforeEach } from 'vitest'

vi.mock('../../../wailsjs/go/main/App', () => ({
  ActiveProvider: vi.fn(() =>
    Promise.resolve({
      providerID: 'openai-codex',
      providerName: 'OpenAI (ChatGPT subscription)',
      model: 'gpt-5.5',
      contextWindow: 400000,
    }),
  ),
  ProviderCatalog: vi.fn(() => Promise.resolve([])),
  RefreshModels: vi.fn(() => Promise.resolve([])),
  SelectModel: vi.fn(() => Promise.resolve()),
  ConnectProvider: vi.fn(() => Promise.resolve()),
  DeclareEndpoint: vi.fn(() => Promise.resolve('')),
  ForgetProvider: vi.fn(() => Promise.resolve()),
  ListModels: vi.fn(() => Promise.resolve([])),
  StartProviderLogin: vi.fn(),
  AwaitProviderLogin: vi.fn(),
  CancelProviderLogin: vi.fn(() => Promise.resolve()),
  OpenLoginPage: vi.fn(() => Promise.resolve()),
}))

import { createProviderState } from './provider'
import * as App from '../../../wailsjs/go/main/App'

const code = (userCode: string) => ({
  providerID: 'openai-codex',
  providerName: 'OpenAI (ChatGPT subscription)',
  userCode,
  verificationURI: 'https://auth.openai.com/codex/device',
  expiresAt: '2026-07-28T01:58:53Z',
})

// deferred hands back a promise plus the handle that settles it, so a test can
// hold a round trip open at the exact point the UI is racing with itself.
function deferred<T>() {
  let settle!: (value: T) => void
  const promise = new Promise<T>((resolve) => {
    settle = resolve
  })
  return { promise, settle }
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(App.ProviderCatalog).mockResolvedValue([])
})

describe('device-code login', () => {
  // The mint round trip is the one window with nothing on screen, so it is the one
  // a second click lands in. Two logins reaching the backend retires the first code
  // server-side and the losing attempt's cleanup then blanks the live one.
  it('ignores a second sign-in while the first code is still being minted', async () => {
    const minted = deferred<ReturnType<typeof code>>()
    vi.mocked(App.StartProviderLogin).mockReturnValue(minted.promise)
    vi.mocked(App.AwaitProviderLogin).mockReturnValue(new Promise(() => {}))
    const state = createProviderState()

    const first = state.startProviderLogin('openai-codex')
    await state.startProviderLogin('openai-codex')
    expect(App.StartProviderLogin).toHaveBeenCalledTimes(1)
    expect(state.startingLogin.value).toBe('openai-codex')

    minted.settle(code('V3H5-1MW96'))
    await Promise.resolve()
    await Promise.resolve()
    expect(state.pendingLogin.value?.userCode).toBe('V3H5-1MW96')
    expect(state.startingLogin.value).toBe('')

    // A click on the row that is already showing a code is the same login too.
    await state.startProviderLogin('openai-codex')
    expect(App.StartProviderLogin).toHaveBeenCalledTimes(1)
    void first
  })

  // A cancelled attempt still resolves, and it resolves after the retry has painted
  // its own code. Clearing on the way out would take an approvable code off screen
  // and put the sign-in button back while the login is live server-side.
  it('keeps the retried code on screen when the cancelled attempt ends late', async () => {
    const firstWait = deferred<void>()
    vi.mocked(App.StartProviderLogin)
      .mockResolvedValueOnce(code('AAAA-1111'))
      .mockResolvedValueOnce(code('BBBB-2222'))
    vi.mocked(App.AwaitProviderLogin)
      .mockReturnValueOnce(firstWait.promise)
      .mockReturnValueOnce(new Promise(() => {}))
    const state = createProviderState()

    void state.startProviderLogin('openai-codex')
    await Promise.resolve()
    await Promise.resolve()
    expect(state.pendingLogin.value?.userCode).toBe('AAAA-1111')

    await state.cancelProviderLogin('openai-codex')
    expect(App.CancelProviderLogin).toHaveBeenCalledWith('openai-codex')
    expect(state.pendingLogin.value).toBeNull()

    void state.startProviderLogin('openai-codex')
    await Promise.resolve()
    await Promise.resolve()
    expect(state.pendingLogin.value?.userCode).toBe('BBBB-2222')

    firstWait.settle()
    await Promise.resolve()
    await Promise.resolve()
    expect(state.pendingLogin.value?.userCode).toBe('BBBB-2222')
  })

  it('clears the code and reloads the catalog once the login is approved', async () => {
    vi.mocked(App.StartProviderLogin).mockResolvedValue(code('V3H5-1MW96'))
    vi.mocked(App.AwaitProviderLogin).mockResolvedValue(undefined)
    const state = createProviderState()

    await state.startProviderLogin('openai-codex')

    expect(state.pendingLogin.value).toBeNull()
    expect(state.startingLogin.value).toBe('')
    expect(App.ProviderCatalog).toHaveBeenCalled()
    expect(state.providerID.value).toBe('openai-codex')
  })

  // A mint that never produced a code must not leave the button dead: the row has
  // to be clickable again for the retry.
  it('frees the sign-in button when the code could not be minted', async () => {
    vi.mocked(App.StartProviderLogin).mockRejectedValue(
      new Error('deviceauth is unavailable'),
    )
    const state = createProviderState()

    await expect(state.startProviderLogin('openai-codex')).rejects.toThrow(
      'deviceauth is unavailable',
    )
    expect(state.startingLogin.value).toBe('')
    expect(state.pendingLogin.value).toBeNull()
  })
})

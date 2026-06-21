// @vitest-environment jsdom
import { describe, it, expect, beforeEach } from 'vitest'
import { createApp, nextTick } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import piniaPluginPersistedstate from 'pinia-plugin-persistedstate'
import { useUiStore } from './ui'

// Estado de UI persistente (identidad §4): si el usuario oculto la sidebar,
// debe seguir oculta al reabrir la app. La fuente de verdad del estado de UI es
// el frontend (a diferencia del historial de chats, que vive en el backend).
//
// Los plugins de pinia solo corren cuando pinia se instala en una app Vue, asi
// que el test monta una app minima (sin renderizar) para activar el plugin de
// persistencia, igual que main.ts.
function installPinia() {
  const app = createApp({ render: () => null })
  const pinia = createPinia()
  pinia.use(piniaPluginPersistedstate)
  app.use(pinia)
  setActivePinia(pinia)
}

beforeEach(() => {
  localStorage.clear()
})

describe('ui store: persistencia del estado de UI', () => {
  it('rehidrata sidebarCollapsed desde localStorage al iniciar', () => {
    localStorage.setItem('ui', JSON.stringify({ sidebarCollapsed: true }))
    installPinia()

    const ui = useUiStore()

    expect(ui.sidebarCollapsed).toBe(true)
  })

  it('persiste sidebarCollapsed en localStorage al cambiar', async () => {
    installPinia()
    const ui = useUiStore()

    ui.toggleSidebar()
    await nextTick()

    expect(ui.sidebarCollapsed).toBe(true)
    const stored = localStorage.getItem('ui')
    expect(stored).not.toBeNull()
    expect(JSON.parse(stored as string).sidebarCollapsed).toBe(true)
  })
})

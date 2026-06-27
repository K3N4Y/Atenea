import { ref } from 'vue'
import { defineStore } from 'pinia'

// Estado de UI puro (no es la fuente de verdad del historial, que vive en el
// backend). `persist: true` guarda este store en localStorage para que la
// sidebar oculta siga oculta al reabrir la app (identidad §4). Todo el store es
// estado de UI, asi que se persiste completo; futuras preferencias de vista
// entran aqui y se persisten igual.
export const useUiStore = defineStore(
  'ui',
  () => {
    const sidebarCollapsed = ref(false)

    function toggleSidebar() {
      sidebarCollapsed.value = !sidebarCollapsed.value
    }

    // Panel de herramientas de desarrollo (git, etc.): abierto/cerrado. Persiste
    // como el resto del estado de UI para que siga abierto al reabrir la app.
    const devPanelOpen = ref(false)

    function toggleDevPanel() {
      devPanelOpen.value = !devPanelOpen.value
    }

    return { sidebarCollapsed, toggleSidebar, devPanelOpen, toggleDevPanel }
  },
  { persist: true },
)

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

    // Ancho del panel de desarrollo (px), ajustable arrastrando su borde. Acotado
    // a [280, 640]: menos angosto que 280 rompe los botones y los paths; mas ancho
    // que 640 le come el espacio a la columna de chat.
    const devPanelWidth = ref(320)

    function setDevPanelWidth(px: number) {
      devPanelWidth.value = Math.min(640, Math.max(280, Math.round(px)))
    }

    return {
      sidebarCollapsed,
      toggleSidebar,
      devPanelOpen,
      toggleDevPanel,
      devPanelWidth,
      setDevPanelWidth,
    }
  },
  { persist: true },
)

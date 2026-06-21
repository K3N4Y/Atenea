import { ref } from 'vue'
import { defineStore } from 'pinia'

// Estado de UI puro (no es la fuente de verdad del historial, que vive en el
// backend). La persistencia de la sidebar entre sesiones (identidad seccion 4)
// llega en la Fase 4 con pinia-plugin-persistedstate.
export const useUiStore = defineStore('ui', () => {
  const sidebarCollapsed = ref(false)

  function toggleSidebar() {
    sidebarCollapsed.value = !sidebarCollapsed.value
  }

  return { sidebarCollapsed, toggleSidebar }
})

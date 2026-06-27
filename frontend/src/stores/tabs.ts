import { ref, computed } from 'vue'
import { defineStore } from 'pinia'

// Las tabs del panel de desarrollo son instancias abiertas, no un set fijo: se
// pueden agregar varias (varias terminales, otro git) y cerrar. Cada una tiene id
// propio para que su contenido (p.ej. el pty de una terminal) sea independiente.
// Se persiste la lista y la activa; las terminales se re-crean al recargar.
export type TabKind = 'git' | 'terminal'
export type Tab = { id: string; kind: TabKind; title: string }

const LABEL: Record<TabKind, string> = { git: 'Git', terminal: 'Terminal' }

export const useTabsStore = defineStore(
  'tabs',
  () => {
    const tabs = ref<Tab[]>([])
    const activeId = ref('')

    const active = computed(
      () => tabs.value.find((t) => t.id === activeId.value) ?? null,
    )

    // titulo = etiqueta + numero (cuantas de ese tipo hay + 1). ponytail: cosmetico,
    // no es un id; tras cerrar y reabrir el numero puede repetirse y da igual.
    function titleFor(kind: TabKind): string {
      const n = tabs.value.filter((t) => t.kind === kind).length + 1
      return `${LABEL[kind]} ${n}`
    }

    function addTab(kind: TabKind): Tab {
      const tab = { id: crypto.randomUUID(), kind, title: titleFor(kind) }
      tabs.value.push(tab)
      activeId.value = tab.id
      return tab
    }

    function closeTab(id: string) {
      const i = tabs.value.findIndex((t) => t.id === id)
      if (i === -1) return
      tabs.value.splice(i, 1)
      if (activeId.value === id) {
        activeId.value = tabs.value[Math.max(0, i - 1)]?.id ?? ''
      }
    }

    function setActive(id: string) {
      activeId.value = id
    }

    // ensureDefault: si no hay tabs (primer arranque o persistencia vacia), abre un
    // Git para no mostrar el panel en blanco.
    function ensureDefault() {
      if (tabs.value.length === 0) addTab('git')
    }

    return {
      tabs,
      activeId,
      active,
      addTab,
      closeTab,
      setActive,
      ensureDefault,
    }
  },
  { persist: true },
)

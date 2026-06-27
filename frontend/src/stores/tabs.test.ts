// @vitest-environment jsdom
import { describe, it, expect, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useTabsStore } from './tabs'

beforeEach(() => {
  setActivePinia(createPinia())
})

describe('tabs store', () => {
  it('addTab agrega y deja la nueva tab activa', () => {
    const tabs = useTabsStore()
    const t = tabs.addTab('terminal')
    expect(tabs.tabs).toHaveLength(1)
    expect(tabs.activeId).toBe(t.id)
    expect(tabs.active?.kind).toBe('terminal')
  })

  it('cada tab tiene id propio (varias terminales)', () => {
    const tabs = useTabsStore()
    const a = tabs.addTab('terminal')
    const b = tabs.addTab('terminal')
    expect(a.id).not.toBe(b.id)
    expect(tabs.tabs).toHaveLength(2)
  })

  it('closeTab la quita y activa una vecina', () => {
    const tabs = useTabsStore()
    const a = tabs.addTab('git')
    const b = tabs.addTab('terminal')
    expect(tabs.activeId).toBe(b.id)
    tabs.closeTab(b.id)
    expect(tabs.tabs).toHaveLength(1)
    expect(tabs.activeId).toBe(a.id) // cae a la vecina
  })

  it('cerrar la ultima deja sin activa', () => {
    const tabs = useTabsStore()
    const a = tabs.addTab('git')
    tabs.closeTab(a.id)
    expect(tabs.tabs).toHaveLength(0)
    expect(tabs.active).toBeNull()
  })

  it('ensureDefault arranca con un Git si esta vacio', () => {
    const tabs = useTabsStore()
    tabs.ensureDefault()
    expect(tabs.tabs).toHaveLength(1)
    expect(tabs.active?.kind).toBe('git')
    // idempotente: no agrega otra
    tabs.ensureDefault()
    expect(tabs.tabs).toHaveLength(1)
  })
})

// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { nextTick } from 'vue'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import AppSidebar from './AppSidebar.vue'
import { useUiStore } from '../stores/ui'

function mountSidebar(props: Record<string, unknown> = {}) {
  const pinia = createPinia()
  setActivePinia(pinia)
  return mount(AppSidebar, { props, global: { plugins: [pinia] } })
}

describe('AppSidebar', () => {
  it('el aside tiene una etiqueta accesible', () => {
    const wrapper = mountSidebar()
    expect(wrapper.find('aside').attributes('aria-label')).toBeTruthy()
  })

  it('emite new-chat al pulsar New chat', async () => {
    const wrapper = mountSidebar()
    await wrapper.find('button').trigger('click')
    expect(wrapper.emitted('new-chat')).toBeTruthy()
  })

  it('refleja el estado colapsado del store de UI', async () => {
    const wrapper = mountSidebar()
    const ui = useUiStore()

    expect(wrapper.find('aside').attributes('data-collapsed')).toBe('false')

    ui.sidebarCollapsed = true
    await nextTick()

    expect(wrapper.find('aside').attributes('data-collapsed')).toBe('true')
  })

  it('lista una fila por sesion mostrando su Title', () => {
    const wrapper = mountSidebar({
      sessions: [
        { ID: 's2', Title: 'segunda pregunta' },
        { ID: 's1', Title: 'primera pregunta' },
      ],
    })
    const rows = wrapper.findAll('[data-session-id]')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('segunda pregunta')
    expect(rows[1].text()).toContain('primera pregunta')
  })

  it('cae a un placeholder cuando el Title esta vacio', () => {
    const wrapper = mountSidebar({ sessions: [{ ID: 's1', Title: '' }] })
    const row = wrapper.find('[data-session-id="s1"]')
    expect(row.text().trim().length).toBeGreaterThan(0)
  })

  it('emite select-session con el id al pulsar una fila', async () => {
    const wrapper = mountSidebar({ sessions: [{ ID: 's1', Title: 'hola' }] })
    await wrapper.find('[data-session-id="s1"]').trigger('click')
    expect(wrapper.emitted('select-session')?.[0]).toEqual(['s1'])
  })

  it('marca la sesion activa con aria-current', () => {
    const wrapper = mountSidebar({
      sessions: [
        { ID: 's1', Title: 'uno' },
        { ID: 's2', Title: 'dos' },
      ],
      activeSessionId: 's2',
    })
    expect(wrapper.find('[data-session-id="s1"]').attributes('aria-current')).toBeFalsy()
    expect(wrapper.find('[data-session-id="s2"]').attributes('aria-current')).toBe('true')
  })

  it('sin sesiones no renderiza ninguna fila', () => {
    const wrapper = mountSidebar({ sessions: [] })
    expect(wrapper.findAll('[data-session-id]')).toHaveLength(0)
  })
})

// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { nextTick } from 'vue'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import AppSidebar from './AppSidebar.vue'
import { useUiStore } from '../stores/ui'

function mountSidebar() {
  const pinia = createPinia()
  setActivePinia(pinia)
  return mount(AppSidebar, { global: { plugins: [pinia] } })
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
})

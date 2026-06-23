// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import SettingsPanel from './SettingsPanel.vue'

function mcpsTabOf(wrapper: ReturnType<typeof mount>) {
  const tab = wrapper.findAll('[role="tab"]').find((t) => t.text() === 'MCPs')
  if (!tab) throw new Error('the MCPs tab does not exist')
  return tab
}

// Full-screen settings panel (frontend-only): tabs on the left and content
// on the right. The MCPs tab shows the marketplace-style list.
describe('SettingsPanel', () => {
  it('is a dialog with an accessible label', () => {
    const wrapper = mount(SettingsPanel)
    const dialog = wrapper.find('[role="dialog"]')
    expect(dialog.exists()).toBe(true)
    expect(dialog.attributes('aria-label')).toBeTruthy()
  })

  it('covers the full screen, with no modal backdrop', () => {
    const wrapper = mount(SettingsPanel)
    // Full screen: the dialog covers the viewport (fixed + inset-0)...
    const dialog = wrapper.find('[role="dialog"]')
    expect(dialog.classes()).toContain('fixed')
    expect(dialog.classes()).toContain('inset-0')
    // ...and there is no darkened background behind it (that was the modal).
    expect(wrapper.find('[data-backdrop]').exists()).toBe(false)
  })

  it('includes an MCPs tab', () => {
    const wrapper = mount(SettingsPanel)
    expect(mcpsTabOf(wrapper).exists()).toBe(true)
  })

  it('shows at least one card when activating the MCPs tab', async () => {
    const wrapper = mount(SettingsPanel)
    await mcpsTabOf(wrapper).trigger('click')
    expect(wrapper.findAll('article').length).toBeGreaterThan(0)
  })

  it('the MCPs list uses the chat column width and is centered', async () => {
    const wrapper = mount(SettingsPanel)
    await mcpsTabOf(wrapper).trigger('click')
    // The card container is the direct parent of each <article>.
    const list = wrapper.find('article').element.parentElement as HTMLElement
    // Same width as the chat column (max-w-3xl), centered.
    expect(list.classList.contains('max-w-3xl')).toBe(true)
    expect(list.classList.contains('mx-auto')).toBe(true)
  })

  it('marks the active tab with aria-selected', async () => {
    const wrapper = mount(SettingsPanel)
    const mcps = mcpsTabOf(wrapper)
    expect(mcps.attributes('aria-selected')).toBe('false')
    await mcps.trigger('click')
    expect(mcps.attributes('aria-selected')).toBe('true')
  })

  it('emits close when clicking the close button', async () => {
    const wrapper = mount(SettingsPanel)
    await wrapper.find('button[aria-label="Cerrar configuracion"]').trigger('click')
    expect(wrapper.emitted('close')).toBeTruthy()
  })

  it('emits close on the Escape key', async () => {
    const wrapper = mount(SettingsPanel)
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(wrapper.emitted('close')).toBeTruthy()
  })
})

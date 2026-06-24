// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { nextTick } from 'vue'
import { mount } from '@vue/test-utils'
import { Flip } from 'gsap/Flip'
import SettingsPanel from './SettingsPanel.vue'
import McpCard from './McpCard.vue'
import { mcpCatalog } from '../lib/mcps'

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
    expect(wrapper.findAllComponents(McpCard).length).toBeGreaterThan(0)
  })

  it('the MCPs list uses the chat column width and is centered', async () => {
    const wrapper = mount(SettingsPanel)
    await mcpsTabOf(wrapper).trigger('click')
    // The card container is the direct parent of each card.
    const list = wrapper.findComponent(McpCard).element
      .parentElement as HTMLElement
    // Same width as the chat column (max-w-3xl), centered.
    expect(list.classList.contains('max-w-3xl')).toBe(true)
    expect(list.classList.contains('mx-auto')).toBe(true)
  })

  it('opens the MCP detail view when a card is clicked, with a back control', async () => {
    const wrapper = mount(SettingsPanel)
    await mcpsTabOf(wrapper).trigger('click')
    const first = mcpCatalog[0]

    await wrapper.findComponent(McpCard).trigger('click')
    await nextTick()

    // The detail replaces the list: a back control appears and there are no
    // more cards.
    expect(wrapper.find('button[aria-label="Back to MCPs"]').exists()).toBe(
      true,
    )
    expect(wrapper.findAllComponents(McpCard).length).toBe(0)
    expect(wrapper.text()).toContain(first.name)
    expect(wrapper.text()).toContain(first.description)
  })

  it('flips toward the new detail elements (not the detached list nodes)', async () => {
    // When the list/detail nodes are swapped, Flip.from must receive `targets`
    // pointing at the new in-DOM elements; otherwise it animates the detached
    // old nodes and the morph looks instant.
    const fromSpy = vi
      .spyOn(Flip, 'from')
      .mockImplementation(() => ({}) as never)
    const wrapper = mount(SettingsPanel)
    await mcpsTabOf(wrapper).trigger('click')

    await wrapper.findComponent(McpCard).trigger('click')
    await nextTick()

    expect(fromSpy).toHaveBeenCalledTimes(1)
    const vars = fromSpy.mock.calls[0][1] as Record<string, unknown>
    expect(typeof vars.targets).toBe('string')
    expect(vars.targets as string).toContain('data-flip-id')
    fromSpy.mockRestore()
  })

  it('positions the morphing image so it can lift above the list while collapsing', async () => {
    // On the way back the whole list reappears; the image flying down to its
    // card must paint above the sibling cards, which needs a positioned element.
    const wrapper = mount(SettingsPanel)
    await mcpsTabOf(wrapper).trigger('click')
    await wrapper.findComponent(McpCard).trigger('click')
    await nextTick()

    expect(wrapper.find('img').classes()).toContain('relative')
  })

  it('animates the return with Flip toward the new list elements', async () => {
    const fromSpy = vi
      .spyOn(Flip, 'from')
      .mockImplementation(() => ({}) as never)
    const wrapper = mount(SettingsPanel)
    await mcpsTabOf(wrapper).trigger('click')
    await wrapper.findComponent(McpCard).trigger('click')
    await nextTick()
    fromSpy.mockClear()

    await wrapper.find('button[aria-label="Back to MCPs"]').trigger('click')
    await nextTick()

    expect(fromSpy).toHaveBeenCalledTimes(1)
    expect(
      typeof (fromSpy.mock.calls[0][1] as Record<string, unknown>).targets,
    ).toBe('string')
    fromSpy.mockRestore()
  })

  it('returns to the MCP list from the detail via the back control', async () => {
    const wrapper = mount(SettingsPanel)
    await mcpsTabOf(wrapper).trigger('click')
    await wrapper.findComponent(McpCard).trigger('click')
    await nextTick()

    await wrapper.find('button[aria-label="Back to MCPs"]').trigger('click')
    await nextTick()

    expect(wrapper.findAllComponents(McpCard).length).toBeGreaterThan(0)
    expect(wrapper.find('button[aria-label="Back to MCPs"]').exists()).toBe(
      false,
    )
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
    await wrapper
      .find('button[aria-label="Cerrar configuracion"]')
      .trigger('click')
    expect(wrapper.emitted('close')).toBeTruthy()
  })

  it('emits close on the Escape key', async () => {
    const wrapper = mount(SettingsPanel)
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(wrapper.emitted('close')).toBeTruthy()
  })
})

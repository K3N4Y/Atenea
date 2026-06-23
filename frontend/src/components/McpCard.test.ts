// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import McpCard from './McpCard.vue'

const entry = {
  id: 'github',
  name: 'GitHub',
  description: 'Lee y gestiona repositorios.',
  accent: '#1c1c1a',
}

// Horizontal card of the marketplace: rounded image on the left, name on top
// and description below. Presentational: receives the entry via prop.
describe('McpCard', () => {
  it('shows an image with the name as alt text', () => {
    const wrapper = mount(McpCard, { props: { entry } })
    const img = wrapper.find('img')
    expect(img.exists()).toBe(true)
    expect(img.attributes('alt')).toBe('GitHub')
    expect(img.attributes('src')).toMatch(/^data:image\/svg/)
  })

  it('shows the name and description of the MCP', () => {
    const wrapper = mount(McpCard, { props: { entry } })
    expect(wrapper.text()).toContain('GitHub')
    expect(wrapper.text()).toContain('Lee y gestiona repositorios.')
  })

  it('uses the entry own image when present, keeping its aspect ratio', () => {
    const wrapper = mount(McpCard, { props: { entry: { ...entry, image: '/x/github.png' } } })
    const img = wrapper.find('img')
    // Uses the own image, not the generated avatar.
    expect(img.attributes('src')).toBe('/x/github.png')
    // Keeps the aspect ratio (object-contain + auto width), not cropped to a
    // fixed square.
    expect(img.classes()).toContain('object-contain')
    expect(img.classes()).toContain('w-auto')
    expect(img.classes()).not.toContain('w-20')
  })

  it('falls back to the generated square avatar when the entry has no image', () => {
    const wrapper = mount(McpCard, { props: { entry } })
    const img = wrapper.find('img')
    expect(img.attributes('src')).toMatch(/^data:image\/svg/)
    expect(img.classes()).toContain('w-20')
    expect(img.classes()).toContain('object-cover')
  })

  it('emits select with the entry id when clicked', async () => {
    const wrapper = mount(McpCard, { props: { entry } })
    await wrapper.trigger('click')
    expect(wrapper.emitted('select')?.[0]).toEqual([entry.id])
  })

  it('is not a <button> (which would clip the image during the reverse Flip)', () => {
    // A <button> clips/traps the oversized image while collapsing back from the
    // detail; the card must be a non-clipping element. Keep it accessible via
    // role/tabindex instead.
    const wrapper = mount(McpCard, { props: { entry } })
    expect(wrapper.element.tagName).not.toBe('BUTTON')
    expect(wrapper.find('[role="button"]').exists()).toBe(true)
  })
})

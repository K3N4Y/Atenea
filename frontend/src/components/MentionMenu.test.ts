// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import MentionMenu from './MentionMenu.vue'

const files = ['app.go', 'internal/tool/glob.go']

describe('MentionMenu', () => {
  it('renderiza una opcion por item dentro de un listbox', () => {
    const wrapper = mount(MentionMenu, {
      props: { items: files, activeIndex: 0 },
    })
    expect(wrapper.find('[role="listbox"]').exists()).toBe(true)
    expect(wrapper.findAll('[role="option"]')).toHaveLength(2)
  })

  it('muestra el nombre de archivo de cada ruta', () => {
    const wrapper = mount(MentionMenu, {
      props: { items: files, activeIndex: 0 },
    })
    expect(wrapper.findAll('[role="option"]')[1].text()).toContain('glob.go')
  })

  it('marca la opcion activa con aria-selected', () => {
    const wrapper = mount(MentionMenu, {
      props: { items: files, activeIndex: 1 },
    })
    const opts = wrapper.findAll('[role="option"]')
    expect(opts[0].attributes('aria-selected')).toBe('false')
    expect(opts[1].attributes('aria-selected')).toBe('true')
  })

  it('mousedown en una opcion emite select con la ruta (no click: no roba el foco)', async () => {
    const wrapper = mount(MentionMenu, {
      props: { items: files, activeIndex: 0 },
    })
    await wrapper.findAll('[role="option"]')[1].trigger('mousedown')
    expect(wrapper.emitted('select')?.[0]).toEqual(['internal/tool/glob.go'])
  })

  it('hover sobre una opcion emite hover con su indice', async () => {
    const wrapper = mount(MentionMenu, {
      props: { items: files, activeIndex: 0 },
    })
    await wrapper.findAll('[role="option"]')[1].trigger('mouseenter')
    expect(wrapper.emitted('hover')?.[0]).toEqual([1])
  })
})

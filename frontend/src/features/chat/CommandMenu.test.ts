// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import CommandMenu from './CommandMenu.vue'
import type { Command } from './command'

const commands: Command[] = [
  { name: 'commit', description: 'arma el mensaje y commitea' },
  { name: 'code-review', description: 'Revision de codigo' },
]

describe('CommandMenu', () => {
  it('renderiza una opcion por item dentro de un listbox', () => {
    const wrapper = mount(CommandMenu, {
      props: { items: commands, activeIndex: 0 },
    })
    expect(wrapper.find('[role="listbox"]').exists()).toBe(true)
    expect(wrapper.findAll('[role="option"]')).toHaveLength(2)
  })

  it('muestra el nombre del comando y su descripcion', () => {
    const wrapper = mount(CommandMenu, {
      props: { items: commands, activeIndex: 0 },
    })
    const first = wrapper.findAll('[role="option"]')[0]
    expect(first.text()).toContain('commit')
    expect(first.text()).toContain('arma el mensaje y commitea')
  })

  it('marca la opcion activa con aria-selected', () => {
    const wrapper = mount(CommandMenu, {
      props: { items: commands, activeIndex: 1 },
    })
    const opts = wrapper.findAll('[role="option"]')
    expect(opts[0].attributes('aria-selected')).toBe('false')
    expect(opts[1].attributes('aria-selected')).toBe('true')
  })

  it('mousedown en una opcion emite select con el nombre (no click: no roba el foco)', async () => {
    const wrapper = mount(CommandMenu, {
      props: { items: commands, activeIndex: 0 },
    })
    await wrapper.findAll('[role="option"]')[1].trigger('mousedown')
    expect(wrapper.emitted('select')?.[0]).toEqual(['code-review'])
  })

  it('hover sobre una opcion emite hover con su indice', async () => {
    const wrapper = mount(CommandMenu, {
      props: { items: commands, activeIndex: 0 },
    })
    await wrapper.findAll('[role="option"]')[1].trigger('mouseenter')
    expect(wrapper.emitted('hover')?.[0]).toEqual([1])
  })
})

// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import WorkspacePicker from './WorkspacePicker.vue'
import type { WorkspaceOption } from './workspaces'

const options: WorkspaceOption[] = [
  { path: '/home/u/atenea', label: 'atenea' },
  { path: '/home/u/agentkanban', label: 'agentkanban' },
]

describe('WorkspacePicker', () => {
  it('muestra la carpeta vigente en el disparador y el menu arranca cerrado', () => {
    const wrapper = mount(WorkspacePicker, {
      props: { workspace: '/home/u/atenea', options },
    })
    const trigger = wrapper.find('[data-workspace-trigger]')
    expect(trigger.exists()).toBe(true)
    expect(trigger.text()).toContain('atenea')
    // Cerrado: ninguna opcion visible hasta abrir el menu.
    expect(wrapper.findAll('[data-workspace-option]')).toHaveLength(0)
  })

  it('abre el menu al pulsar el disparador y lista una opcion por carpeta', async () => {
    const wrapper = mount(WorkspacePicker, {
      props: { workspace: '/home/u/atenea', options },
    })
    await wrapper.find('[data-workspace-trigger]').trigger('click')
    const opts = wrapper.findAll('[data-workspace-option]')
    expect(opts).toHaveLength(2)
    expect(opts[1].text()).toContain('agentkanban')
  })

  it('marca la carpeta vigente como activa en el menu (aria-selected)', async () => {
    const wrapper = mount(WorkspacePicker, {
      props: { workspace: '/home/u/agentkanban', options },
    })
    await wrapper.find('[data-workspace-trigger]').trigger('click')
    const opts = wrapper.findAll('[data-workspace-option]')
    expect(opts[0].attributes('aria-selected')).toBe('false')
    expect(opts[1].attributes('aria-selected')).toBe('true')
  })

  it('elegir una carpeta emite select con su ruta y cierra el menu', async () => {
    const wrapper = mount(WorkspacePicker, {
      props: { workspace: '/home/u/atenea', options },
    })
    await wrapper.find('[data-workspace-trigger]').trigger('click')
    await wrapper.findAll('[data-workspace-option]')[1].trigger('click')
    expect(wrapper.emitted('select')?.[0]).toEqual(['/home/u/agentkanban'])
    // Tras elegir, el menu se cierra.
    expect(wrapper.findAll('[data-workspace-option]')).toHaveLength(0)
  })

  it('elegir "Browse folder" emite browse (abre el dialogo nativo)', async () => {
    const wrapper = mount(WorkspacePicker, {
      props: { workspace: '/home/u/atenea', options },
    })
    await wrapper.find('[data-workspace-trigger]').trigger('click')
    await wrapper.find('[data-browse-workspace]').trigger('click')
    expect(wrapper.emitted('browse')).toBeTruthy()
    expect(wrapper.emitted('select')).toBeUndefined()
  })
})

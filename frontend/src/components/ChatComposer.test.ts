// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import ChatComposer from './ChatComposer.vue'

describe('ChatComposer', () => {
  it('el textarea tiene una etiqueta accesible', () => {
    const wrapper = mount(ChatComposer, { props: { running: false } })
    expect(wrapper.find('textarea').attributes('aria-label')).toBeTruthy()
  })

  it('Enter emite send con el texto y limpia el input', async () => {
    const wrapper = mount(ChatComposer, { props: { running: false } })
    const ta = wrapper.find('textarea')

    await ta.setValue('hola')
    await ta.trigger('keydown', { key: 'Enter' })

    expect(wrapper.emitted('send')?.[0]).toEqual(['hola'])
    expect((ta.element as HTMLTextAreaElement).value).toBe('')
  })

  it('Shift+Enter no emite send (inserta salto de linea)', async () => {
    const wrapper = mount(ChatComposer, { props: { running: false } })
    const ta = wrapper.find('textarea')

    await ta.setValue('linea')
    await ta.trigger('keydown', { key: 'Enter', shiftKey: true })

    expect(wrapper.emitted('send')).toBeUndefined()
  })

  it('input vacio: el boton enviar esta deshabilitado y Enter no emite', async () => {
    const wrapper = mount(ChatComposer, { props: { running: false } })

    await wrapper.find('textarea').trigger('keydown', { key: 'Enter' })

    expect(wrapper.emitted('send')).toBeUndefined()
    expect(
      wrapper.find('button[aria-label="Send"]').attributes('disabled'),
    ).toBeDefined()
  })

  it('running: anuncia el estado (role=status) y emite stop al click', async () => {
    const wrapper = mount(ChatComposer, { props: { running: true } })

    expect(wrapper.find('[role="status"]').exists()).toBe(true)
    await wrapper.find('button[aria-label="Stop"]').trigger('click')

    expect(wrapper.emitted('stop')).toBeTruthy()
  })

  it('el toggle de modo emite toggle-mode al click', async () => {
    const wrapper = mount(ChatComposer, { props: { running: false } })

    await wrapper.get('[data-action="toggle-mode"]').trigger('click')

    expect(wrapper.emitted('toggle-mode')).toBeTruthy()
  })

  it('mode=plan: el toggle refleja aria-pressed=true', () => {
    const wrapper = mount(ChatComposer, {
      props: { running: false, mode: 'plan' },
    })

    expect(
      wrapper.get('[data-action="toggle-mode"]').attributes('aria-pressed'),
    ).toBe('true')
  })

  it('sin prop mode (default normal): el toggle expone aria-pressed=false', () => {
    const wrapper = mount(ChatComposer, { props: { running: false } })

    expect(
      wrapper.get('[data-action="toggle-mode"]').attributes('aria-pressed'),
    ).toBe('false')
  })

  it('el toggle de modo sigue activo durante una ejecucion (running=true): el usuario puede cambiar a plan mientras corre', async () => {
    const wrapper = mount(ChatComposer, {
      props: { running: true, mode: 'normal' },
    })

    const toggle = wrapper.get('[data-action="toggle-mode"]')
    expect(toggle.attributes('disabled')).toBeUndefined()
    await toggle.trigger('click')

    expect(wrapper.emitted('toggle-mode')).toBeTruthy()
  })
})

describe('ChatComposer @-menciones de archivos', () => {
  const files = ['app.go', 'internal/tool/glob.go', 'internal/tool/grep.go']

  it('escribir @ abre el menu con los archivos del workspace', async () => {
    const wrapper = mount(ChatComposer, { props: { running: false, files } })

    await wrapper.find('textarea').setValue('@')

    expect(wrapper.find('[role="listbox"]').exists()).toBe(true)
    expect(wrapper.findAll('[role="option"]').length).toBeGreaterThan(0)
  })

  it('al escribir tras @ filtra los candidatos', async () => {
    const wrapper = mount(ChatComposer, { props: { running: false, files } })

    await wrapper.find('textarea').setValue('@glob')

    const opts = wrapper.findAll('[role="option"]')
    expect(opts).toHaveLength(1)
    expect(opts[0].text()).toContain('glob.go')
  })

  it('Enter con el menu abierto inserta la ruta y NO envia', async () => {
    const wrapper = mount(ChatComposer, { props: { running: false, files } })
    const ta = wrapper.find('textarea')

    await ta.setValue('@glob')
    await ta.trigger('keydown', { key: 'Enter' })

    expect(wrapper.emitted('send')).toBeUndefined()
    expect((ta.element as HTMLTextAreaElement).value).toBe(
      '@internal/tool/glob.go ',
    )
    expect(wrapper.find('[role="listbox"]').exists()).toBe(false)
  })

  it('ArrowDown mueve la seleccion y Enter inserta la opcion activa', async () => {
    const wrapper = mount(ChatComposer, { props: { running: false, files } })
    const ta = wrapper.find('textarea')

    await ta.setValue('@internal/tool/g') // matchea glob.go y grep.go
    expect(wrapper.findAll('[role="option"]')).toHaveLength(2)

    await ta.trigger('keydown', { key: 'ArrowDown' }) // baja a la segunda
    await ta.trigger('keydown', { key: 'Enter' })

    expect((ta.element as HTMLTextAreaElement).value).toBe(
      '@internal/tool/grep.go ',
    )
  })

  it('Escape cierra el menu; un Enter posterior envia normal', async () => {
    const wrapper = mount(ChatComposer, { props: { running: false, files } })
    const ta = wrapper.find('textarea')

    await ta.setValue('@glob')
    await ta.trigger('keydown', { key: 'Escape' })
    expect(wrapper.find('[role="listbox"]').exists()).toBe(false)

    await ta.trigger('keydown', { key: 'Enter' })
    expect(wrapper.emitted('send')?.[0]).toEqual(['@glob'])
  })

  it('mousedown en una opcion inserta su ruta', async () => {
    const wrapper = mount(ChatComposer, { props: { running: false, files } })
    const ta = wrapper.find('textarea')

    await ta.setValue('@grep')
    await wrapper.findAll('[role="option"]')[0].trigger('mousedown')

    expect((ta.element as HTMLTextAreaElement).value).toBe(
      '@internal/tool/grep.go ',
    )
  })

  it('sin archivos: @ no abre menu y Enter envia normal', async () => {
    const wrapper = mount(ChatComposer, { props: { running: false } })
    const ta = wrapper.find('textarea')

    await ta.setValue('@x')
    expect(wrapper.find('[role="listbox"]').exists()).toBe(false)

    await ta.trigger('keydown', { key: 'Enter' })
    expect(wrapper.emitted('send')?.[0]).toEqual(['@x'])
  })
})

describe('ChatComposer slash-commands', () => {
  const commands = [
    { name: 'commit', description: 'arma el mensaje y commitea' },
    { name: 'code-review', description: 'Revision de codigo' },
    { name: 'deep-research', description: 'investigacion profunda' },
  ]

  it('escribir "/" al inicio abre el menu con los comandos', async () => {
    const wrapper = mount(ChatComposer, { props: { running: false, commands } })

    await wrapper.find('textarea').setValue('/')

    expect(wrapper.find('[role="listbox"]').exists()).toBe(true)
    expect(wrapper.findAll('[role="option"]').length).toBe(3)
  })

  it('al escribir tras "/" filtra los comandos', async () => {
    const wrapper = mount(ChatComposer, { props: { running: false, commands } })

    await wrapper.find('textarea').setValue('/comm')

    const opts = wrapper.findAll('[role="option"]')
    expect(opts).toHaveLength(1)
    expect(opts[0].text()).toContain('commit')
  })

  it('Enter con el menu abierto inserta "/comando " y NO envia', async () => {
    const wrapper = mount(ChatComposer, { props: { running: false, commands } })
    const ta = wrapper.find('textarea')

    await ta.setValue('/comm')
    await ta.trigger('keydown', { key: 'Enter' })

    expect(wrapper.emitted('send')).toBeUndefined()
    expect((ta.element as HTMLTextAreaElement).value).toBe('/commit ')
    expect(wrapper.find('[role="listbox"]').exists()).toBe(false)
  })

  it('ArrowDown mueve la seleccion y Enter inserta el comando activo', async () => {
    const wrapper = mount(ChatComposer, { props: { running: false, commands } })
    const ta = wrapper.find('textarea')

    await ta.setValue('/') // los tres comandos en el orden recibido
    await ta.trigger('keydown', { key: 'ArrowDown' }) // baja al segundo de la lista
    await ta.trigger('keydown', { key: 'Enter' })

    expect((ta.element as HTMLTextAreaElement).value).toBe('/code-review ')
  })

  it('Escape cierra el menu; un Enter posterior envia normal', async () => {
    const wrapper = mount(ChatComposer, { props: { running: false, commands } })
    const ta = wrapper.find('textarea')

    await ta.setValue('/comm')
    await ta.trigger('keydown', { key: 'Escape' })
    expect(wrapper.find('[role="listbox"]').exists()).toBe(false)

    await ta.trigger('keydown', { key: 'Enter' })
    expect(wrapper.emitted('send')?.[0]).toEqual(['/comm'])
  })

  it('mousedown en una opcion inserta su comando', async () => {
    const wrapper = mount(ChatComposer, { props: { running: false, commands } })
    const ta = wrapper.find('textarea')

    await ta.setValue('/code')
    await wrapper.findAll('[role="option"]')[0].trigger('mousedown')

    expect((ta.element as HTMLTextAreaElement).value).toBe('/code-review ')
  })

  it('"/" a mitad del mensaje NO abre el menu (el comando es todo el mensaje)', async () => {
    const wrapper = mount(ChatComposer, { props: { running: false, commands } })
    const ta = wrapper.find('textarea')

    await ta.setValue('hola /comm')
    expect(wrapper.find('[role="listbox"]').exists()).toBe(false)
  })

  it('sin comandos: "/" no abre menu y Enter envia normal', async () => {
    const wrapper = mount(ChatComposer, { props: { running: false } })
    const ta = wrapper.find('textarea')

    await ta.setValue('/commit')
    expect(wrapper.find('[role="listbox"]').exists()).toBe(false)

    await ta.trigger('keydown', { key: 'Enter' })
    expect(wrapper.emitted('send')?.[0]).toEqual(['/commit'])
  })
})

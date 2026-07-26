// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import ProviderSettings from './ProviderSettings.vue'

const row = (
  id: string,
  models: string[],
  extra: Partial<{
    name: string
    builtIn: boolean
    connectable: boolean
    connected: boolean
  }> = {},
) => ({
  id,
  name: extra.name ?? id,
  models,
  builtIn: extra.builtIn ?? true,
  connectable: extra.connectable ?? false,
  connected: extra.connected ?? false,
})

const cloud = row('anthropic', ['claude-opus-4-8', 'claude-sonnet-5'], {
  name: 'Anthropic',
  connectable: true,
})
const local = row('lm-studio', ['qwen'], {
  name: 'LM Studio',
  builtIn: false,
})

// ProviderSettings es presentacional: recibe el catalogo y la seleccion vigente por
// props y emite `select`, `connect`, `forget`, `declare`, `refresh` y `list-models`.
// El panel de ajustes las cablea al store y baja el error resultante. Convencion del
// repo: data-* como selectores de test y eventos hacia arriba (como WorkspacePicker).
describe('ProviderSettings', () => {
  it('pinta una fila por provider con sus modelos', () => {
    const wrapper = mount(ProviderSettings, {
      props: { providers: [cloud, local] },
    })

    expect(wrapper.find('[data-provider-row="anthropic"]').exists()).toBe(true)
    expect(wrapper.find('[data-provider-row="lm-studio"]').exists()).toBe(true)
    expect(
      wrapper.find('[data-model-option="anthropic:claude-sonnet-5"]').exists(),
    ).toBe(true)
  })

  it('marca el modelo activo del provider activo', () => {
    const wrapper = mount(ProviderSettings, {
      props: {
        providers: [cloud, local],
        activeProviderID: 'anthropic',
        activeModel: 'claude-sonnet-5',
      },
    })

    expect(
      wrapper
        .find('[data-model-option="anthropic:claude-sonnet-5"]')
        .attributes('aria-pressed'),
    ).toBe('true')
    // Un modelo homonimo de otro provider no se marca: el par (provider, modelo) es
    // lo que identifica una seleccion, no el nombre del modelo suelto.
    expect(
      wrapper
        .find('[data-model-option="anthropic:claude-opus-4-8"]')
        .attributes('aria-pressed'),
    ).toBe('false')
  })

  it('elegir un modelo emite select con su provider', async () => {
    const wrapper = mount(ProviderSettings, { props: { providers: [cloud] } })

    await wrapper
      .find('[data-model-option="anthropic:claude-opus-4-8"]')
      .trigger('click')

    expect(wrapper.emitted('select')?.[0]).toEqual([
      'anthropic',
      'claude-opus-4-8',
    ])
  })

  // El estado de la key es lo que distingue "no puedo elegir este provider" de "no
  // lo he elegido": sin decirlo, un fallo al seleccionar no tiene explicacion.
  it('muestra el estado de la key de los providers conectables', () => {
    const connected = row('openrouter', ['openrouter/free'], {
      connectable: true,
      connected: true,
    })
    const wrapper = mount(ProviderSettings, {
      props: { providers: [cloud, connected, local] },
    })

    expect(wrapper.find('[data-provider-key-state="anthropic"]').text()).toBe(
      'No key',
    )
    expect(wrapper.find('[data-provider-key-state="openrouter"]').text()).toBe(
      'Key stored',
    )
    // Un endpoint local no lleva key, asi que no muestra ni el estado ni el campo.
    expect(wrapper.find('[data-provider-key-state="lm-studio"]').exists()).toBe(
      false,
    )
    expect(wrapper.find('[data-api-key-input="lm-studio"]').exists()).toBe(
      false,
    )
  })

  it('guardar una key emite connect con el provider y la key', async () => {
    const wrapper = mount(ProviderSettings, { props: { providers: [cloud] } })

    await wrapper.find('[data-api-key-input="anthropic"]').setValue('sk-ant')
    await wrapper.find('[data-connect-form="anthropic"]').trigger('submit')

    expect(wrapper.emitted('connect')?.[0]).toEqual(['anthropic', 'sk-ant'])
  })

  // Solo los endpoints declarados por el usuario se pueden quitar: uno de fabrica
  // volveria en el proximo arranque, asi que ofrecerlo seria mentir.
  it('solo ofrece quitar los providers que no vienen de fabrica', async () => {
    const wrapper = mount(ProviderSettings, {
      props: { providers: [cloud, local] },
    })

    expect(wrapper.find('[data-forget-provider="anthropic"]').exists()).toBe(
      false,
    )
    await wrapper.find('[data-forget-provider="lm-studio"]').trigger('click')

    expect(wrapper.emitted('forget')?.[0]).toEqual(['lm-studio'])
  })

  it('un provider sin modelos lo dice en vez de quedar vacio', () => {
    const wrapper = mount(ProviderSettings, {
      props: { providers: [row('ollama', [], { builtIn: false })] },
    })

    expect(wrapper.text()).toContain('No models yet')
  })

  it('recargar modelos emite refresh y se deshabilita mientras corre', async () => {
    const wrapper = mount(ProviderSettings, { props: { providers: [cloud] } })

    await wrapper.find('[data-refresh-models]').trigger('click')
    expect(wrapper.emitted('refresh')).toHaveLength(1)

    await wrapper.setProps({ refreshing: true })
    expect(
      wrapper.find('[data-refresh-models]').attributes('disabled'),
    ).toBeDefined()
  })

  it('el formulario de endpoint arranca cerrado y se abre a pedido', async () => {
    const wrapper = mount(ProviderSettings, { props: { providers: [cloud] } })

    expect(wrapper.find('[data-endpoint-form]').exists()).toBe(false)

    await wrapper.find('[data-add-endpoint]').trigger('click')

    expect(wrapper.find('[data-endpoint-form]').exists()).toBe(true)
  })

  it('los presets rellenan el endpoint', async () => {
    const wrapper = mount(ProviderSettings, { props: { providers: [] } })
    await wrapper.find('[data-add-endpoint]').trigger('click')

    await wrapper.find('[data-preset="lmstudio"]').trigger('click')
    expect(
      (wrapper.find('[data-endpoint-url]').element as HTMLInputElement).value,
    ).toBe('http://localhost:1234/v1')

    await wrapper.find('[data-preset="ollama"]').trigger('click')
    expect(
      (wrapper.find('[data-endpoint-url]').element as HTMLInputElement).value,
    ).toBe('http://localhost:11434/v1')
  })

  it('"cargar modelos" emite list-models con el endpoint escrito', async () => {
    const wrapper = mount(ProviderSettings, { props: { providers: [] } })
    await wrapper.find('[data-add-endpoint]').trigger('click')
    await wrapper.find('[data-endpoint-url]').setValue('http://localhost:9/v1')

    await wrapper.find('[data-list-models]').trigger('click')

    expect(wrapper.emitted('list-models')?.[0]).toEqual([
      'http://localhost:9/v1',
    ])
  })

  it('elegir un modelo descubierto lo pone en el campo modelo', async () => {
    const wrapper = mount(ProviderSettings, {
      props: { providers: [], discoveredModels: ['qwen', 'llama'] },
    })
    await wrapper.find('[data-add-endpoint]').trigger('click')

    await wrapper.find('[data-discovered-model="llama"]').trigger('click')

    expect(
      (wrapper.find('[data-endpoint-model]').element as HTMLInputElement).value,
    ).toBe('llama')
  })

  it('guardar el endpoint emite declare con nombre, endpoint y modelo', async () => {
    const wrapper = mount(ProviderSettings, { props: { providers: [] } })
    await wrapper.find('[data-add-endpoint]').trigger('click')
    await wrapper.find('[data-endpoint-name]').setValue('LM Studio')
    await wrapper
      .find('[data-endpoint-url]')
      .setValue('http://localhost:1234/v1')
    await wrapper.find('[data-endpoint-model]').setValue('qwen')

    await wrapper.find('[data-endpoint-form]').trigger('submit')

    expect(wrapper.emitted('declare')?.[0]).toEqual([
      'LM Studio',
      'http://localhost:1234/v1',
      'qwen',
    ])
  })

  // Sin nombre o sin endpoint no hay nada que declarar, y el boton lo dice antes de
  // que el backend tenga que rechazarlo.
  it('no deja guardar un endpoint sin nombre o sin URL', async () => {
    const wrapper = mount(ProviderSettings, { props: { providers: [] } })
    await wrapper.find('[data-add-endpoint]').trigger('click')

    expect(
      wrapper.find('[data-declare-endpoint]').attributes('disabled'),
    ).toBeDefined()

    await wrapper.find('[data-endpoint-name]').setValue('LM Studio')
    expect(
      wrapper.find('[data-declare-endpoint]').attributes('disabled'),
    ).toBeDefined()

    await wrapper
      .find('[data-endpoint-url]')
      .setValue('http://localhost:1234/v1')
    expect(
      wrapper.find('[data-declare-endpoint]').attributes('disabled'),
    ).toBeUndefined()
  })

  // Sin credencial la app arranca sobre el fake sin red, que no es una fila del
  // catalogo: entonces no hay nada resaltado y hace falta decir por que, o el
  // usuario chatea con respuestas de guion creyendo que le habla un modelo.
  it('avisa cuando la seleccion activa no es ningun provider del catalogo', () => {
    const wrapper = mount(ProviderSettings, {
      props: { providers: [cloud], activeProviderID: 'demo' },
    })

    expect(wrapper.find('[data-provider-unconfigured]').text()).toContain(
      'offline demo',
    )
  })

  it('no avisa cuando hay un provider elegido', () => {
    const wrapper = mount(ProviderSettings, {
      props: { providers: [cloud], activeProviderID: 'anthropic' },
    })

    expect(wrapper.find('[data-provider-unconfigured]').exists()).toBe(false)
  })

  // Un catalogo vacio es "todavia no cargo", no "no hay provider": avisar ahi
  // pintaria el aviso un instante en cada apertura del panel.
  it('no avisa mientras el catalogo esta vacio', () => {
    const wrapper = mount(ProviderSettings, { props: { providers: [] } })

    expect(wrapper.find('[data-provider-unconfigured]').exists()).toBe(false)
  })

  it('muestra el error que le baja el panel', () => {
    const wrapper = mount(ProviderSettings, {
      props: { providers: [cloud], error: 'invalid API key' },
    })

    const alert = wrapper.find('[data-provider-error]')
    expect(alert.text()).toBe('invalid API key')
    expect(alert.attributes('role')).toBe('alert')
  })
})

// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import ProviderSettings from './ProviderSettings.vue'

// ProviderSettings es presentacional: recibe la config vigente por props y emite
// `apply` (kind, baseURL, model) y `list-models` (baseURL). El panel de ajustes lo
// cablea al store (setProvider/listModels). Convencion del repo: data-* como
// selectores de test y eventos hacia arriba (como WorkspacePicker).
describe('ProviderSettings', () => {
  it('refleja el provider vigente y muestra el baseURL en local', () => {
    const wrapper = mount(ProviderSettings, {
      props: {
        providerKind: 'local',
        baseURL: 'http://localhost:1234/v1',
        model: 'qwen',
        availableModels: [],
      },
    })

    expect(
      wrapper.find('[data-provider-option="local"]').attributes('aria-pressed'),
    ).toBe('true')
    expect(wrapper.find('[data-baseurl-input]').exists()).toBe(true)
  })

  it('oculta el baseURL en OpenRouter y lo revela al elegir local', async () => {
    const wrapper = mount(ProviderSettings, {
      props: {
        providerKind: 'openrouter',
        baseURL: '',
        model: 'openrouter/free',
        availableModels: [],
      },
    })

    expect(wrapper.find('[data-baseurl-input]').exists()).toBe(false)

    await wrapper.find('[data-provider-option="local"]').trigger('click')

    expect(wrapper.find('[data-baseurl-input]').exists()).toBe(true)
  })

  it('el preset de LM Studio rellena el baseURL', async () => {
    const wrapper = mount(ProviderSettings, {
      props: {
        providerKind: 'local',
        baseURL: '',
        model: '',
        availableModels: [],
      },
    })

    await wrapper.find('[data-preset="lmstudio"]').trigger('click')

    const input = wrapper.find('[data-baseurl-input]')
      .element as HTMLInputElement
    expect(input.value).toBe('http://localhost:1234/v1')
  })

  it('el preset de Ollama rellena el baseURL', async () => {
    const wrapper = mount(ProviderSettings, {
      props: {
        providerKind: 'local',
        baseURL: '',
        model: '',
        availableModels: [],
      },
    })

    await wrapper.find('[data-preset="ollama"]').trigger('click')

    const input = wrapper.find('[data-baseurl-input]')
      .element as HTMLInputElement
    expect(input.value).toBe('http://localhost:11434/v1')
  })

  it('"cargar modelos" emite list-models con el baseURL vigente', async () => {
    const wrapper = mount(ProviderSettings, {
      props: {
        providerKind: 'local',
        baseURL: 'http://localhost:1234/v1',
        model: '',
        availableModels: [],
      },
    })

    await wrapper.find('[data-list-models]').trigger('click')

    expect(wrapper.emitted('list-models')?.[0]).toEqual([
      'http://localhost:1234/v1',
    ])
  })

  it('lista los modelos disponibles y elegir uno lo pone en el campo modelo', async () => {
    const wrapper = mount(ProviderSettings, {
      props: {
        providerKind: 'local',
        baseURL: 'http://localhost:1234/v1',
        model: '',
        availableModels: ['qwen2.5-coder', 'llama-3.1'],
      },
    })

    const chips = wrapper.findAll('[data-model-option]')
    expect(chips.length).toBe(2)

    await wrapper.find('[data-model-option="qwen2.5-coder"]').trigger('click')

    const input = wrapper.find('[data-model-input]').element as HTMLInputElement
    expect(input.value).toBe('qwen2.5-coder')
  })

  it('aplicar emite apply con kind/baseURL/model vigentes (local)', async () => {
    const wrapper = mount(ProviderSettings, {
      props: {
        providerKind: 'local',
        baseURL: 'http://localhost:1234/v1',
        model: 'qwen',
        availableModels: [],
      },
    })

    await wrapper.find('[data-apply-provider]').trigger('click')

    expect(wrapper.emitted('apply')?.[0]).toEqual([
      'local',
      'http://localhost:1234/v1',
      'qwen',
    ])
  })

  it('aplicar en OpenRouter emite apply con kind openrouter y el modelo', async () => {
    const wrapper = mount(ProviderSettings, {
      props: {
        providerKind: 'openrouter',
        baseURL: '',
        model: 'openrouter/free',
        availableModels: [],
      },
    })

    await wrapper.find('[data-apply-provider]').trigger('click')

    expect(wrapper.emitted('apply')?.[0]).toEqual([
      'openrouter',
      '',
      'openrouter/free',
    ])
  })
})

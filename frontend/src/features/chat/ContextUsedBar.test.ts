// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import ContextUsedBar from './ContextUsedBar.vue'

// Indicador del contexto usado: porcentaje + barra de progreso, calculado contra
// la ventana del modelo. Presentational: recibe usage (camelCase) y model por
// prop; sin usage no pinta nada. Solo tokens, sin costos.
describe('ContextUsedBar', () => {
  it('no renderiza nada sin usage', () => {
    const wrapper = mount(ContextUsedBar, {
      props: { usage: null, model: 'anthropic/claude-opus-4.8' },
    })

    expect(wrapper.text()).toBe('')
    expect(wrapper.find('[role="progressbar"]').exists()).toBe(false)
  })

  it('muestra el porcentaje de contexto usado', () => {
    const wrapper = mount(ContextUsedBar, {
      props: {
        usage: {
          inputTokens: 100000,
          outputTokens: 0,
          reasoningTokens: 0,
          cacheReadTokens: 0,
          cacheWriteTokens: 0,
        },
        model: 'anthropic/claude-opus-4.8',
      },
    })

    expect(wrapper.text()).toContain('50%')
    const bar = wrapper.find('[role="progressbar"]')
    expect(bar.exists()).toBe(true)
    expect(bar.attributes('aria-valuenow')).toBe('50')
  })

  it('muestra los tokens de entrada y salida', () => {
    const wrapper = mount(ContextUsedBar, {
      props: {
        usage: {
          inputTokens: 1500,
          outputTokens: 500,
          reasoningTokens: 0,
          cacheReadTokens: 0,
          cacheWriteTokens: 0,
        },
        model: 'anthropic/claude-opus-4.8',
      },
    })

    const bar = wrapper.find('[role="progressbar"]')
    const text = wrapper.text() + ' ' + (bar.attributes('title') ?? '')
    expect(text).toContain('1.5k')
    expect(text).toContain('500')
  })

  it('clampa la barra al 100% cuando los tokens superan la ventana', () => {
    // 500k de input contra una ventana de 200k se acota a 100%: la barra no se
    // desborda ni muestra un porcentaje mayor a cien.
    const wrapper = mount(ContextUsedBar, {
      props: {
        usage: {
          inputTokens: 500000,
          outputTokens: 0,
          reasoningTokens: 0,
          cacheReadTokens: 0,
          cacheWriteTokens: 0,
        },
        model: 'anthropic/claude-opus-4.8',
      },
    })

    const bar = wrapper.find('[role="progressbar"]')
    expect(bar.attributes('aria-valuenow')).toBe('100')
    expect(wrapper.text()).toContain('100%')
  })

  it('un modelo desconocido usa la ventana por defecto', () => {
    // Sin entrada en el mapa, la barra escala contra DEFAULT_CONTEXT_WINDOW
    // (200k): 100k de input son la mitad, 50%.
    const wrapper = mount(ContextUsedBar, {
      props: {
        usage: {
          inputTokens: 100000,
          outputTokens: 0,
          reasoningTokens: 0,
          cacheReadTokens: 0,
          cacheWriteTokens: 0,
        },
        model: 'totally/unknown',
      },
    })

    expect(wrapper.text()).toContain('50%')
  })
})

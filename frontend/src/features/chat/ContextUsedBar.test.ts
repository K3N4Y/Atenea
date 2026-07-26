// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import ContextUsedBar from './ContextUsedBar.vue'

const usage = (inputTokens: number, outputTokens = 0) => ({
  inputTokens,
  outputTokens,
  reasoningTokens: 0,
  cacheReadTokens: 0,
  cacheWriteTokens: 0,
})

// Indicador del contexto usado: porcentaje + barra de progreso, escalado contra la
// ventana que declara el adapter activo. Presentational: recibe usage (camelCase) y
// contextWindow por prop; sin usage no pinta nada. Solo tokens, sin costos.
describe('ContextUsedBar', () => {
  it('no renderiza nada sin usage', () => {
    const wrapper = mount(ContextUsedBar, {
      props: { usage: null, contextWindow: 200000 },
    })

    expect(wrapper.text()).toBe('')
    expect(wrapper.find('[role="progressbar"]').exists()).toBe(false)
  })

  it('muestra el porcentaje de contexto usado', () => {
    const wrapper = mount(ContextUsedBar, {
      props: { usage: usage(100000), contextWindow: 200000 },
    })

    expect(wrapper.text()).toContain('50%')
    const bar = wrapper.find('[role="progressbar"]')
    expect(bar.exists()).toBe(true)
    expect(bar.attributes('aria-valuenow')).toBe('50')
  })

  it('muestra los tokens de entrada y salida', () => {
    const wrapper = mount(ContextUsedBar, {
      props: { usage: usage(1500, 500), contextWindow: 200000 },
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
      props: { usage: usage(500000), contextWindow: 200000 },
    })

    const bar = wrapper.find('[role="progressbar"]')
    expect(bar.attributes('aria-valuenow')).toBe('100')
    expect(wrapper.text()).toContain('100%')
  })

  // Con la ventana sin declarar (0) no hay escala honesta: se muestran los tokens
  // sin barra ni porcentaje, en vez de inventar un default que miente para todo
  // modelo que no lo tenga.
  it('sin ventana declarada muestra tokens pero no barra ni porcentaje', () => {
    const wrapper = mount(ContextUsedBar, {
      props: { usage: usage(100000, 250), contextWindow: 0 },
    })

    expect(wrapper.find('[role="progressbar"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('%')
    expect(wrapper.text()).toContain('100k in')
    expect(wrapper.text()).toContain('250 out')
  })
})

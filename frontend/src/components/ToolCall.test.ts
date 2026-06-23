// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import ToolCall from './ToolCall.vue'

const tool = (over: Record<string, unknown> = {}) => ({
  item: {
    kind: 'tool',
    id: 't1',
    callID: 'c1',
    name: 'echo',
    input: {},
    status: 'success',
    output: '',
    error: null,
    ...over,
  },
})

describe('ToolCall', () => {
  it('read en curso: "Reading" + solo el nombre del archivo (§10)', () => {
    const wrapper = mount(ToolCall, {
      props: tool({ name: 'read', status: 'running', input: { path: '/home/a/b/c.go' } }),
    })

    expect(wrapper.text()).toContain('Reading')
    expect(wrapper.text()).toContain('c.go')
    expect(wrapper.text()).not.toContain('/home/a/b')
  })

  it('read finalizado: "Read" + nombre del archivo', () => {
    const wrapper = mount(ToolCall, {
      props: tool({ name: 'read', status: 'success', input: { file_path: '/x/y/z.ts' } }),
    })

    expect(wrapper.text()).toContain('Read')
    expect(wrapper.text()).toContain('z.ts')
  })

  it('tool generica: muestra nombre y output', () => {
    const wrapper = mount(ToolCall, { props: tool({ name: 'echo', output: 'hola' }) })

    expect(wrapper.text()).toContain('echo')
    expect(wrapper.text()).toContain('hola')
  })

  it('tool fallida: muestra la causa del error', () => {
    const wrapper = mount(ToolCall, { props: tool({ status: 'failed', error: 'boom' }) })

    expect(wrapper.text()).toContain('boom')
  })

  it('pending: shows the command and Approve/Deny buttons', () => {
    const wrapper = mount(ToolCall, {
      props: tool({ name: 'bash', status: 'pending', input: { command: 'ls -la' } }),
    })

    expect(wrapper.text()).toContain('ls -la')
    expect(wrapper.get('[data-action="approve"]').text()).toContain('Aprobar')
    expect(wrapper.get('[data-action="deny"]').text()).toContain('Denegar')
  })

  it('pending: approving emits approve with the callID', async () => {
    const wrapper = mount(ToolCall, {
      props: tool({ name: 'bash', status: 'pending', callID: 'c1' }),
    })

    await wrapper.get('[data-action="approve"]').trigger('click')

    expect(wrapper.emitted('approve')?.[0]).toEqual(['c1'])
  })

  it('pending: denying emits deny with the callID', async () => {
    const wrapper = mount(ToolCall, {
      props: tool({ name: 'bash', status: 'pending', callID: 'c1' }),
    })

    await wrapper.get('[data-action="deny"]').trigger('click')

    expect(wrapper.emitted('deny')?.[0]).toEqual(['c1'])
  })
})

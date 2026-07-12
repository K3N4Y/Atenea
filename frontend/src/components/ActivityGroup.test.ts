// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import type { ToolItem } from '../stores/chat'
import ActivityGroup from './ActivityGroup.vue'

function tool(overrides: Partial<ToolItem> = {}): ToolItem {
  return {
    kind: 'tool',
    id: 't1',
    callID: 'c1',
    name: 'echo',
    input: {},
    status: 'success',
    output: '',
    error: null,
    diff: '',
    ...overrides,
  }
}

describe('ActivityGroup', () => {
  it('owns decorative rail segments only between ordered activity rows', () => {
    const wrapper = mount(ActivityGroup, {
      props: {
        items: [
          tool({ id: 't1', name: 'grep', input: { pattern: 'auth' } }),
          tool({ id: 't2', name: 'read', input: { path: 'internal/auth.go' } }),
          tool({ id: 't3', name: 'bash', status: 'running' }),
        ],
      },
    })

    expect(wrapper.findAll('[data-test="activity-rail"]')).toHaveLength(2)
    expect(
      wrapper
        .findAll('[data-test="activity-rail"]')[0]
        .attributes('aria-hidden'),
    ).toBe('true')
    expect(wrapper.findAll('[data-test="activity-row"]')).toHaveLength(3)
    expect(
      wrapper
        .findAll('[data-test="activity-row-container"]')
        .at(-1)
        ?.find('[data-test="activity-rail"]')
        .exists(),
    ).toBe(false)
    expect(wrapper.text()).toMatch(/grep.*read.*bash/s)
  })

  it('renders a single row without a dangling connector', () => {
    const wrapper = mount(ActivityGroup, { props: { items: [tool()] } })

    expect(wrapper.find('[data-test="activity-rail"]').exists()).toBe(false)
    expect(wrapper.findAll('[data-test="activity-row"]')).toHaveLength(1)
  })

  it('forwards permission decisions', async () => {
    const wrapper = mount(ActivityGroup, {
      props: {
        items: [
          tool({ status: 'pending', name: 'bash', callID: 'permission-1' }),
        ],
      },
    })

    await wrapper.get('[data-action="approve"]').trigger('click')
    await wrapper.get('[data-action="deny"]').trigger('click')

    expect(wrapper.emitted('approve')?.[0]).toEqual(['permission-1'])
    expect(wrapper.emitted('deny')?.[0]).toEqual(['permission-1'])
  })

  it('keeps mixed statuses aligned in one rail', () => {
    const wrapper = mount(ActivityGroup, {
      props: {
        items: [
          tool({ id: 't1', status: 'running' }),
          tool({ id: 't2', status: 'failed', error: 'boom' }),
          tool({ id: 't3', status: 'success' }),
        ],
      },
    })

    expect(wrapper.findAll('[data-test="activity-row"]')).toHaveLength(3)
    expect(wrapper.findAll('[data-status="running"]')).toHaveLength(1)
    expect(wrapper.findAll('[data-status="failed"]')).toHaveLength(1)
    expect(wrapper.findAll('[data-status="success"]')).toHaveLength(1)
  })
})

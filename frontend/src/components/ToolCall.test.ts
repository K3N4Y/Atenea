// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
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
    diff: '',
    ...over,
  },
})

const EDIT_DIFF = [
  '--- a/internal/auth.go',
  '+++ b/internal/auth.go',
  '@@ -1 +1,2 @@',
  '-old',
  '+new',
  '+extra',
  '',
].join('\n')

describe('ToolCall', () => {
  it('shows the action, complete target, and running marker', () => {
    const wrapper = mount(ToolCall, {
      props: tool({
        name: 'read',
        status: 'running',
        input: { path: 'internal/auth.go' },
      }),
    })

    expect(wrapper.text()).toContain('read')
    expect(wrapper.text()).toContain('internal/auth.go')
    expect(wrapper.get('[data-status="running"]').exists()).toBe(true)
  })

  it('collapses successful output by default and expands it independently', async () => {
    const wrapper = mount(ToolCall, {
      props: tool({ name: 'echo', output: 'full output' }),
    })

    const summary = wrapper.get('[data-test="activity-summary"]')
    expect(summary.attributes('aria-expanded')).toBe('false')
    expect(wrapper.text()).not.toContain('full output')

    await summary.trigger('click')

    expect(summary.attributes('aria-expanded')).toBe('true')
    expect(wrapper.text()).toContain('full output')
  })

  it('shows diff counts before expansion and DiffView after expansion', async () => {
    const wrapper = mount(ToolCall, {
      props: tool({
        name: 'edit',
        input: { path: 'internal/auth.go' },
        output: '[internal/auth.go#ab12]',
        diff: EDIT_DIFF,
      }),
    })

    expect(wrapper.text()).toContain('edit')
    expect(wrapper.text()).toContain('internal/auth.go')
    expect(wrapper.text()).toContain('+2 -1')
    expect(wrapper.findAll('[data-type="add"]')).toHaveLength(0)

    await wrapper.get('[data-test="activity-summary"]').trigger('click')

    expect(wrapper.findAll('[data-type="add"]')).toHaveLength(2)
    expect(wrapper.findAll('[data-type="del"]')).toHaveLength(1)
    expect(wrapper.find('pre').exists()).toBe(false)
  })

  it('falls back to collapsed plain output for legacy edit events without diff', async () => {
    const wrapper = mount(ToolCall, {
      props: tool({
        name: 'edit',
        input: { path: 'foo.go' },
        output: '[foo.go#ab12]',
      }),
    })

    expect(wrapper.find('pre').exists()).toBe(false)
    await wrapper.get('[data-test="activity-summary"]').trigger('click')
    expect(wrapper.get('pre').text()).toContain('[foo.go#ab12]')
  })

  it('shows a failed marker and excerpt while collapsing the full error', async () => {
    const wrapper = mount(ToolCall, {
      props: tool({
        status: 'failed',
        error: 'permission denied\nfull stack trace',
      }),
    })

    expect(wrapper.get('[data-status="failed"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('permission denied full stack trace')
    expect(wrapper.text()).not.toContain('permission denied\nfull stack trace')

    await wrapper.get('[data-test="activity-summary"]').trigger('click')
    expect(wrapper.text()).toContain('permission denied\nfull stack trace')
  })

  it('keeps long errors compact until the row is expanded', async () => {
    const error = `permission denied: ${'stack frame '.repeat(40).trim()}`
    const wrapper = mount(ToolCall, {
      props: tool({ status: 'failed', error }),
    })

    const summary = wrapper.get('[data-test="activity-summary"]')
    expect(summary.text().length).toBeLessThan(220)
    expect(summary.text()).toContain('…')

    await summary.trigger('click')
    expect(wrapper.text()).toContain(error)
  })

  it('uses a status-rich accessible label and title for the full target', () => {
    const wrapper = mount(ToolCall, {
      props: tool({
        name: 'bash',
        input: { command: 'go test ./internal/auth' },
        output: 'ok',
      }),
    })

    const summary = wrapper.get('[data-test="activity-summary"]')
    expect(summary.attributes('aria-label')).toContain(
      'bash, go test ./internal/auth, succeeded',
    )
    expect(
      wrapper.get('[data-test="activity-target"]').attributes('title'),
    ).toBe('go test ./internal/auth')
  })

  it('renders an empty successful tool as a non-interactive summary row', () => {
    const wrapper = mount(ToolCall, { props: tool({ name: 'custom' }) })

    expect(wrapper.text()).toContain('custom')
    expect(wrapper.find('[data-test="activity-summary"]').exists()).toBe(false)
    expect(wrapper.find('[data-status="success"]').exists()).toBe(true)
  })

  it('pending permission keeps command and Approve/Deny actions visible', () => {
    const wrapper = mount(ToolCall, {
      props: tool({
        name: 'bash',
        status: 'pending',
        input: { command: 'ls -la' },
      }),
    })

    expect(wrapper.get('[data-status="pending"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('ls -la')
    expect(wrapper.find('[data-test="activity-summary"]').exists()).toBe(false)
    expect(wrapper.get('[data-action="approve"]').text()).toContain('Aprobar')
    expect(wrapper.get('[data-action="deny"]').text()).toContain('Denegar')
  })

  it('pending approval emits approve with the callID', async () => {
    const wrapper = mount(ToolCall, {
      props: tool({ name: 'bash', status: 'pending', callID: 'c1' }),
    })

    await wrapper.get('[data-action="approve"]').trigger('click')

    expect(wrapper.emitted('approve')?.[0]).toEqual(['c1'])
  })

  it('pending denial emits deny with the callID', async () => {
    const wrapper = mount(ToolCall, {
      props: tool({ name: 'bash', status: 'pending', callID: 'c1' }),
    })

    await wrapper.get('[data-action="deny"]').trigger('click')

    expect(wrapper.emitted('deny')?.[0]).toEqual(['c1'])
  })
})

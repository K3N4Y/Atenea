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
    diff: '',
    ...over,
  },
})

const EDIT_DIFF = [
  '--- a/foo.go',
  '+++ b/foo.go',
  '@@ -1 +1 @@',
  '-a',
  '+b',
  '',
].join('\n')

describe('ToolCall', () => {
  it('shows "Reading" and only the file name while read is running (§10)', () => {
    const wrapper = mount(ToolCall, {
      props: tool({
        name: 'read',
        status: 'running',
        input: { path: '/home/a/b/c.go' },
      }),
    })

    expect(wrapper.text()).toContain('Reading')
    expect(wrapper.text()).toContain('c.go')
    expect(wrapper.text()).not.toContain('/home/a/b')
  })

  it('shows "Read" and the file name after read completes', () => {
    const wrapper = mount(ToolCall, {
      props: tool({
        name: 'read',
        status: 'success',
        input: { file_path: '/x/y/z.ts' },
      }),
    })

    expect(wrapper.text()).toContain('Read')
    expect(wrapper.text()).toContain('z.ts')
  })

  it('shows a generic tool name and output', () => {
    const wrapper = mount(ToolCall, {
      props: tool({ name: 'echo', output: 'hola' }),
    })

    expect(wrapper.text()).toContain('echo')
    expect(wrapper.text()).toContain('hola')
  })

  it.each([
    ['command', { command: 'printf command' }],
    ['cmd', { cmd: 'printf cmd' }],
  ])(
    'bash with %s stays collapsed until independently expanded',
    async (_, input) => {
      const wrapper = mount(ToolCall, {
        props: tool({ name: 'bash', input, output: 'tool output' }),
      })

      const disclosure = wrapper.get('button[aria-expanded="false"]')
      expect(disclosure.text()).toContain(Object.values(input)[0])
      expect(wrapper.text()).not.toContain('tool output')

      await disclosure.trigger('click')
      expect(disclosure.attributes('aria-expanded')).toBe('true')
      expect(wrapper.text()).toContain('tool output')

      await disclosure.trigger('click')
      expect(wrapper.text()).not.toContain('tool output')
    },
  )

  it('keeps separate Bash executions independently expanded', async () => {
    const first = mount(ToolCall, {
      props: tool({
        callID: 'c1',
        name: 'bash',
        input: { command: 'one' },
        output: 'first output',
      }),
    })
    const second = mount(ToolCall, {
      props: tool({
        callID: 'c2',
        name: 'bash',
        input: { command: 'two' },
        output: 'second output',
      }),
    })

    await first.get('button').trigger('click')

    expect(first.text()).toContain('first output')
    expect(second.text()).not.toContain('second output')
  })

  it('hides a Bash error until expanded', async () => {
    const wrapper = mount(ToolCall, {
      props: tool({
        name: 'bash',
        status: 'failed',
        input: { cmd: 'false' },
        error: 'exit 1',
      }),
    })

    expect(wrapper.text()).not.toContain('exit 1')
    await wrapper.get('button').trigger('click')
    expect(wrapper.text()).toContain('exit 1')
  })

  it('keeps running Bash inert and newly settled output collapsed', async () => {
    const running = tool({
      name: 'bash',
      status: 'running',
      input: { command: 'sleep 1' },
      output: 'early output',
    }).item
    const wrapper = mount(ToolCall, { props: { item: running } })

    expect(wrapper.find('button').exists()).toBe(false)
    await wrapper.get('.flex.w-full').trigger('click')
    expect(wrapper.text()).not.toContain('early output')

    await wrapper.setProps({
      item: { ...running, status: 'success', output: 'completed output' },
    })

    const disclosure = wrapper.get('button[aria-expanded="false"]')
    expect(wrapper.text()).not.toContain('completed output')
    await disclosure.trigger('click')
    expect(wrapper.text()).toContain('completed output')
  })

  it('renders an edit diff instead of plain output', () => {
    const wrapper = mount(ToolCall, {
      props: tool({ name: 'edit', output: '[foo.go#ab12]', diff: EDIT_DIFF }),
    })

    // DiffView renders typed rows and the file name.
    expect(wrapper.findAll('[data-type="add"]')).toHaveLength(1)
    expect(wrapper.findAll('[data-type="del"]')).toHaveLength(1)
    expect(wrapper.text()).toContain('foo.go')
    // The raw diff header is not rendered as plain output.
    expect(wrapper.find('pre').exists()).toBe(false)
  })

  it('renders a write diff', () => {
    const diff = [
      '--- a/n.txt',
      '+++ b/n.txt',
      '@@ -0,0 +1 @@',
      '+nuevo',
      '',
    ].join('\n')
    const wrapper = mount(ToolCall, {
      props: tool({ name: 'write', output: '[n.txt#cd34]', diff }),
    })

    expect(wrapper.findAll('[data-type="add"]')).toHaveLength(1)
  })

  it('falls back to plain output for a legacy edit without a diff', () => {
    const wrapper = mount(ToolCall, {
      props: tool({ name: 'edit', output: '[foo.go#ab12]', diff: '' }),
    })

    expect(wrapper.findAll('[data-type="add"]')).toHaveLength(0)
    expect(wrapper.find('pre').text()).toContain('[foo.go#ab12]')
  })

  it('shows the cause of a failed tool', () => {
    const wrapper = mount(ToolCall, {
      props: tool({ status: 'failed', error: 'boom' }),
    })

    expect(wrapper.text()).toContain('boom')
  })

  it('pending: shows the command and Approve/Deny buttons', () => {
    const wrapper = mount(ToolCall, {
      props: tool({
        name: 'bash',
        status: 'pending',
        input: { command: 'ls -la' },
      }),
    })

    expect(wrapper.text()).toContain('ls -la')
    expect(wrapper.get('[data-action="approve"]').text()).toContain('Aprobar')
    expect(wrapper.get('[data-action="deny"]').text()).toContain('Denegar')
    expect(wrapper.find('[aria-expanded]').exists()).toBe(false)
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

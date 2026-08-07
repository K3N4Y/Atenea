// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import LearningAudit from './LearningAudit.vue'

const candidate = {
  statement: 'Scope cache keys',
  scope: 'workspace caches',
  exceptions: 'Windows',
  evidence: [{ seq: 2, summary: 'root cause' }],
}
const run = {
  id: 'r1',
  workspace: '/w',
  sessionID: 's1',
  cutSeq: 2,
  status: 'ready',
  input: {
    messages: [{ seq: 1, role: 'tool', text: 'bash failed\nDiff:\n-old' }],
    truncated: true,
  },
  candidate,
  providerID: 'p',
  model: 'm',
  usage: { inputTokens: 10, outputTokens: 4, reasoningTokens: 1 },
  createdAt: '2026-01-01T00:00:00Z',
}

describe('LearningAudit', () => {
  it('focuses close, exposes audit provenance, and closes with Escape', async () => {
    const wrapper = mount(LearningAudit, {
      attachTo: document.body,
      props: { runs: [run], lessons: [], pending: new Set() } as never,
    })
    await flushPromises()
    expect(document.activeElement?.getAttribute('aria-label')).toBe(
      'Close learning audit',
    )
    expect(wrapper.text()).toContain('Session s1 through sequence 2')
    expect(wrapper.text()).toContain('evidence truncated')
    expect(wrapper.text()).toContain('bash failed')
    expect(
      wrapper.get('[aria-label="Show captured evidence for run r1"]').exists(),
    ).toBe(true)
    await wrapper.trigger('keydown', { key: 'Escape' })
    expect(wrapper.emitted('close')).toHaveLength(1)
    wrapper.unmount()
  })

  it('offers valid ready actions and emits edited approval', async () => {
    const wrapper = mount(LearningAudit, {
      props: { runs: [run], lessons: [], pending: new Set() } as never,
    })
    const statement = wrapper.get('[aria-label="Edit statement for run r1"]')
    await statement.setValue('Edited lesson')
    await wrapper
      .get('[aria-label="Edit and add candidate from run r1"]')
      .trigger('click')
    expect(wrapper.emitted('approve')?.[0]).toEqual([
      'r1',
      expect.objectContaining({ statement: 'Edited lesson' }),
    ])
    expect(wrapper.find('[aria-label="Retry learning run r1"]').exists()).toBe(
      false,
    )
  })

  it('disables duplicate actions and exposes lesson controls', () => {
    const lesson = {
      id: 'l1',
      workspace: '/w',
      runID: 'r1',
      candidate,
      enabled: true,
      deleted: false,
      createdAt: '2026-01-01T00:00:00Z',
    }
    const wrapper = mount(LearningAudit, {
      props: {
        runs: [run],
        lessons: [lesson],
        pending: new Set(['approve:r1', 'lesson:l1']),
      } as never,
    })
    expect(
      wrapper
        .get('[aria-label="Add candidate from run r1"]')
        .attributes('disabled'),
    ).toBeDefined()
    expect(
      wrapper
        .get('[aria-label="Disable lesson Scope cache keys"]')
        .attributes('disabled'),
    ).toBeDefined()
  })
})

// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import ErrorNotice from './ErrorNotice.vue'

// Surfaces a chat error (provider failure / cut stream) that the store tracks
// in `errorText`. Presentational: receives the message via prop and emits
// `dismiss`; the store/view own the state.
describe('ErrorNotice', () => {
  it('renders the message when there is one', () => {
    const wrapper = mount(ErrorNotice, {
      props: { message: 'provider unavailable' },
    })
    expect(wrapper.text()).toContain('provider unavailable')
  })

  it('renders nothing when there is no message', () => {
    const wrapper = mount(ErrorNotice, { props: { message: null } })
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.text()).toBe('')
  })

  it('exposes the message through an assertive live region', () => {
    const wrapper = mount(ErrorNotice, { props: { message: 'boom' } })
    const alert = wrapper.find('[role="alert"]')
    expect(alert.exists()).toBe(true)
  })

  it('emits dismiss when the dismiss control is clicked', async () => {
    const wrapper = mount(ErrorNotice, { props: { message: 'boom' } })
    await wrapper.find('button[aria-label="Dismiss error"]').trigger('click')
    expect(wrapper.emitted('dismiss')).toBeTruthy()
  })
})

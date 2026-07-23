// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import UserMessage from './UserMessage.vue'

describe('UserMessage', () => {
  it('renderiza el texto del usuario en una burbuja redondeada', () => {
    const wrapper = mount(UserMessage, {
      props: { item: { kind: 'user', id: 'u1', text: 'hola mundo' } },
    })

    expect(wrapper.text()).toContain('hola mundo')
    expect(wrapper.html()).toContain('rounded-soft')
  })
})

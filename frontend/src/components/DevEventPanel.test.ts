// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'

// El panel usa el store, que toca la frontera Wails. La fakeamos como en
// chat.test.ts: aqui solo importa que un click empuje el evento canned al store.
vi.mock('../../wailsjs/go/main/App', () => ({
  SendPrompt: vi.fn(),
  SendPlanPrompt: vi.fn(),
  AcceptPlan: vi.fn(),
  Stop: vi.fn(),
  ResolveToolPermission: vi.fn(),
  ListSessions: vi.fn(() => Promise.resolve([])),
  SessionHistory: vi.fn(() => Promise.resolve([])),
  DeleteSession: vi.fn(() => Promise.resolve()),
  Model: vi.fn(() => Promise.resolve('')),
  ListProjectFiles: vi.fn(() => Promise.resolve([])),
  ListCommands: vi.fn(() => Promise.resolve([])),
}))
vi.mock('../../wailsjs/runtime/runtime', () => ({
  EventsOn: vi.fn(() => () => {}),
}))

import DevEventPanel from './DevEventPanel.vue'
import { useChatStore } from '../stores/chat'

beforeEach(() => setActivePinia(createPinia()))

// DevEventPanel: herramienta de desarrollo que dispara SessionEvents canned al
// store (mismo applyEvent que usa EventsOn), para construir la UI sin agente.
describe('DevEventPanel (dev tools)', () => {
  it('el preset Todos empuja el checklist al store', async () => {
    const chat = useChatStore()
    const wrapper = mount(DevEventPanel)

    await wrapper.get('[data-dev="open"]').trigger('click')
    await wrapper.get('[data-dev="todos"]').trigger('click')

    expect(chat.todos).toHaveLength(3)
    expect(chat.todos[1].status).toBe('in_progress')
  })
})

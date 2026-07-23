// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import TodoList from './TodoList.vue'
import type { TodoItem } from './types'

const todos = (items: TodoItem[]) => ({ todos: items })

// TodoList es el checklist en vivo de arriba a la derecha (estilo Codex):
// presentacional, recibe los todos por prop.
describe('TodoList (checklist de tareas)', () => {
  it('lista vacia: no renderiza nada', () => {
    const wrapper = mount(TodoList, { props: todos([]) })

    expect(wrapper.html()).toBe('<!--v-if-->')
  })

  it('muestra el content de cada tarea', () => {
    const wrapper = mount(TodoList, {
      props: todos([
        { content: 'Escribir test', status: 'completed' },
        { content: 'Implementar tool', status: 'in_progress' },
        { content: 'Refactor', status: 'pending' },
      ]),
    })

    expect(wrapper.text()).toContain('Escribir test')
    expect(wrapper.text()).toContain('Implementar tool')
    expect(wrapper.text()).toContain('Refactor')
  })

  it('marca el item completado (data-status) para tacharlo', () => {
    const wrapper = mount(TodoList, {
      props: todos([{ content: 'Hecho', status: 'completed' }]),
    })

    expect(wrapper.find('[data-status="completed"]').exists()).toBe(true)
  })

  it('marca el item en curso con data-status in_progress', () => {
    const wrapper = mount(TodoList, {
      props: todos([{ content: 'En curso', status: 'in_progress' }]),
    })

    expect(wrapper.find('[data-status="in_progress"]').exists()).toBe(true)
  })
})

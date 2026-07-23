---
updated_at: 2026-07-22
summary: Frontend experience proposal based on the project visual identity.
---

# Frontend

This document brings together the interface and experience proposal for Atenea, based on the visual identity and UX defined in [visual-identity.md](visual-identity.md).

## Key principles

- Minimalism and visual cleanliness.
- Chat experience first, with zero friction.
- Soft, organic shapes with rounded edges.
- Flat structure, without traditional cards.
- Moderate use of orange as an accent.

## References

- [visual-identity.md](visual-identity.md)
- [visual-identity.md](visual-identity.md#1-concepto-principal)
- [visual-identity.md](visual-identity.md#2-principios-de-diseno-uxui)
- [visual-identity.md](visual-identity.md#3-paleta-de-colores)
- [visual-identity.md](visual-identity.md#4-layout-estructura-de-la-pantalla)
- [visual-identity.md](visual-identity.md#6-tipografia)
- [visual-identity.md](visual-identity.md#7-iconografia)
- [visual-identity.md](visual-identity.md#8-anatomia-del-mensaje-del-chat)
- [visual-identity.md](visual-identity.md#9-streaming-de-pensamiento-thinking-process)
- [visual-identity.md](visual-identity.md#10-tool-read)
- [visual-identity.md](visual-identity.md#11-voz-y-microcopy)

## UI/UX Direction

The interface should feel accessible, fluid, and simple, with a clean main chat, persistent sidebar, and clear status language to communicate progress and control to the user.

## Recommended libraries

### Core

- Vue 3
- TypeScript
- Vite
- Vue Router
- Pinia

### UI and Styles

- Tailwind CSS
- Phosphor Icons
- @fontsource/red-hat-mono

### Chat and content

- marked
- DOMPurify
- highlight.js or shiki

### UX and utilities

- @vueuse/core
- GSAP
- date-fns or dayjs

### Persistence

- pinia-plugin-persistedstate, **for UI state only** (collapsed sidebar, view preferences). Chat history is not persisted here: it lives in the backend (see [Persistence and source of truth](#persistencia-y-fuente-de-verdad)].

## Integration with the backend (Wails)

Atenea is a **Wails** desktop application (Go + webview), not a web SPA. The frontend does not talk to an HTTP server or do fetch/REST: it communicates with the Go backend using generated _bindings_ and Wails runtime events. Development phases are built on this surface, not on HTTP calls.

### Bindings (user actions)

Generated in `frontend/wailsjs/go/main/App`:

- `SendPrompt(sessionID, text)`: sends a prompt to the session.
- `Stop(sessionID)`: interrupts the generation in progress.

### Events (state and streaming)

Via `EventsOn` of the runtime (`frontend/wailsjs/runtime/runtime`). Channel `session:<id>` emits the durable log events in order of `Seq`:

- `Text.Started` / `Text.Delta` / `Text.Ended`: AI text streaming.
- `Reasoning.Started` / `Reasoning.Delta` / `Reasoning.Ended`: AI thinking (powers the identity thinking block §9).
- `Tool.Called` / `Tool.Success` / `Tool.Failed`: tool execution (feeds the identity tool states §10).
- `Step.Started` / `Step.Ended` / `Step.Failed`: life cycle of the agent step. `Role: user` (Empty Kind) promotes the user prompt to the log.

Additionally, channel `session:<id>:error` notifies hard errors of the run (supplier failure, step limit, stop).

> Today `App.vue` already cables a single session (`sessionID = 'main'`). The Pinia store must formalize this event→state mapping: messages, text streaming, reasoning block (last 4 lines + timer of §9) and state of each tool.

## Source organization

Frontend code is organized by product capability when state, presentation, and
tests change together:

```text
src/
├── features/       # Capability modules, for example Git
├── components/     # Presentation shared by multiple capabilities
├── stores/         # Cross-capability application state
├── views/          # Route-level composition
└── lib/            # Framework-independent shared utilities
```

A feature owns its local store, dedicated views, and colocated tests. Code stays
in `components`, `stores`, or `lib` when it is genuinely shared. For example,
`features/git` owns Git state and its full-screen diff, and `features/mcp` owns
server connection state and its menu. `features/terminal` owns the PTY adapter,
persistent terminal sessions, visual theme, and panel. The inline `DiffView`
remains shared because tool results also use it. `features/settings` composes
general, provider, and MCP configuration while consuming MCP through its public
store instead of taking ownership of that module. `features/workspace` owns
working-folder selection, restoration, its selector UI, and the derivation of
known folders from session summaries. Chat injects its session-reset and
history-refresh effects into the workspace module, then exposes the same
workspace ref and operations through its existing store interface. This keeps
the persisted `workspace` key compatible without duplicating state or adding a
second Pinia store. `features/sessions` owns session grouping, the history
sidebar, and the state and orchestration for listing, deleting, switching,
and rehydrating sessions. Chat injects its live-log rendering, subscription,
reset, and workspace-resource effects into that module while continuing to
expose the same refs and methods through its existing store interface. Shared
chat, session, tool, plan, todo, usage, and event contracts live in
`features/chat/types.ts`; feature modules depend on these contracts instead of
importing types from the chat store implementation. The chat feature
also owns its Pinia store, route-level `ChatView`, and their tests; the router
loads the view from this module. Chat-specific presentation—including the
composer and its menus, message renderers, planning UI, todo list, context bar,
and error notice—is colocated in the same feature. Generic Markdown and inline
diff rendering remain in shared components. Provider selection and model
discovery are implemented by `features/settings/provider.ts`; Chat exposes the
same refs and operations for compatibility and keeps their existing persisted
keys, but no longer owns that implementation.

## Persistence and source of truth

- **Chat History:** lives in the Go backend (SQLite, `internal/session/sqlitestore.go`), which is the **sole source of truth**. The frontend reads it and rehydrates it from there using bindings; does not duplicate it in `localStorage`.
- **UI State:** it is persisted by the frontend with `pinia-plugin-persistedstate` (e.g. the collapsed sidebar, which identity §4 requires remembering between sessions).

## Frontend development path

### Phase 1: application base

- Initialize the project with Vue 3, TypeScript and Vite.
- Configure Tailwind CSS, Pinia, Vue Router and the Red Hat Mono source.
- Create the base folder structure for features, shared components, cross-feature stores, views, and styles.

### Phase 2: MVP chat experience

- Build the main layout with a central chat and a persistent sidebar. AI responses in a continuous stream from `Text.*` events.

### Phase 3: rendering and visual states

- Integrate Markdown for AI responses.
- Add support for code blocks, tool states and progress states from `Tool.*` and `Step.*` events.
- Define thought visualization (`Reasoning.*` events), file reading and activity microcopy.

### Phase 4: persistence and refinement

- Persist only the UI state (collapsed sidebar, preferences) with `pinia-plugin-persistedstate`; chat history is read from the backend (see [Persistence and source of truth](#persistencia-y-fuente-de-verdad)).
- Add smooth animations with GSAP for transitions and microinteractions.
- Adjust spacing, typography, colors and components to align with the visual identity.

### Phase 5: quality and scalability

- Add unit tests for components and stores.
- Improve accessibility, responsiveness and performance.
- Prepare the app to integrate new agent capabilities without rewriting the UI.

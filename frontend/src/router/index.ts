import { createRouter, createWebHashHistory } from 'vue-router'
import ChatView from '../features/chat/ChatView.vue'

// Hash history: Atenea es una app de escritorio Wails (webview), no una SPA
// servida por HTTP, asi que evitamos depender del fallback de rutas del server.
const router = createRouter({
  history: createWebHashHistory(),
  routes: [{ path: '/', name: 'chat', component: ChatView }],
})

export default router

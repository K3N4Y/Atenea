import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import './styles/main.css'
import 'highlight.js/styles/github.css'
import '@fontsource/red-hat-mono/400.css'
import '@fontsource/red-hat-mono/500.css'
import '@fontsource/red-hat-mono/700.css'

createApp(App)
  .use(createPinia())
  .use(router)
  .mount('#app')

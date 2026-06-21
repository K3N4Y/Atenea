/// <reference types="vitest/config" />
import {defineConfig} from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [vue(), tailwindcss()],
  test: {
    // El store de chat es logica pura (mapeo evento->estado); no necesita DOM.
    environment: 'node',
    include: ['src/**/*.test.ts'],
  },
})

/// <reference types="vitest/config" />
import {defineConfig} from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [vue(), tailwindcss()],
  build: {
    rollupOptions: {
      output: {
        // Separa las dependencias pesadas en chunks propios: el bundle principal
        // queda pequeno y el codigo de terceros se cachea aparte (rendimiento).
        manualChunks: {
          gsap: ['gsap'],
          highlight: ['highlight.js'],
          markdown: ['marked', 'marked-highlight', 'dompurify'],
        },
      },
    },
  },
  test: {
    // Por defecto los tests corren en node (logica pura de stores/lib). Los
    // tests de componentes declaran `// @vitest-environment jsdom` por archivo.
    environment: 'node',
    include: ['src/**/*.test.ts'],
  },
})

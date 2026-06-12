import { fileURLToPath, URL } from 'node:url'

import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

// https://vite.dev/config/
export default defineConfig({
  // tailwindcss() is the Tailwind v4 Vite plugin — no separate postcss/tailwind.config needed.
  plugins: [vue(), tailwindcss()],
  resolve: {
    // Lets us import with '@/...' instead of long relative paths.
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
  },
})

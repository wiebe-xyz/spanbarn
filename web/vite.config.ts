import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  test: {
    globals: true,
    environment: 'jsdom',
    setupFiles: ['./src/test-setup.ts'],
  },
  server: {
    port: 3000,
    proxy: {
      '/api': 'http://localhost:8080',
      '/v1': 'http://localhost:8080',
    },
  },
})

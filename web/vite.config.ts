import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

// The Go server serves the built app; in development Vite proxies the API
// to a server on :8080 (see the Makefile's dev targets).
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist',
    assetsDir: 'assets',
    sourcemap: false,
    target: 'es2022',
  },
  server: {
    port: 5173,
    // Sherlock's UI actions are one list the server and the app share
    // (P6U-04): server/internal/sherlock/ui_actions.json.
    fs: { allow: ['..'] },
    proxy: {
      '/api': { target: 'http://127.0.0.1:8080', changeOrigin: false },
    },
  },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.test.{ts,tsx}'],
  },
})

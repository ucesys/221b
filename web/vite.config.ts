import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import type { Plugin } from 'vite'

// Every browser 221B supports reads WOFF2, so the fonts' WOFF fallbacks are
// left out of the build: they are never loaded, and the server embeds every
// file the build makes, so they would add about 250 KB to the image (P1-05).
const woff2Only: Plugin = {
  name: '221b-woff2-only',
  enforce: 'pre',
  transform(code, id) {
    if (!id.includes('/@fontsource/') || !id.endsWith('.css')) return
    return code.replace(/,\s*url\([^)]*\.woff\) format\('woff'\)/g, '')
  },
}

// The Go server serves the built app; in development Vite proxies the API
// to a server on :8080 (see the Makefile's dev targets).
export default defineConfig({
  plugins: [react(), woff2Only],
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

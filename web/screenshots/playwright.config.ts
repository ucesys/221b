import { defineConfig, devices } from '@playwright/test'

// The README's screenshots (readme.spec.ts), against a running 221B on the
// simulator: `make screenshots`.
export default defineConfig({
  testDir: '.',
  timeout: 90_000,
  expect: { timeout: 10_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list']],
  use: {
    ...devices['Desktop Chrome'],
    baseURL: process.env.E2E_BASE_URL ?? 'http://127.0.0.1:8080',
    viewport: { width: 1440, height: 900 },
  },
})

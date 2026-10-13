import { defineConfig, devices } from '@playwright/test'

// E2E_BASE_URL points at a running 221B: the simulator setup from
// `make e2e-sim`, or the chart in kind in CI. E2E_BROWSERS picks browsers
// (chromium, firefox, webkit); Chromium alone by default.
const baseURL = process.env.E2E_BASE_URL ?? 'http://127.0.0.1:8080'
const browsers = (process.env.E2E_BROWSERS ?? 'chromium').split(',').map((b) => b.trim())
const desktop = { chromium: devices['Desktop Chrome'], firefox: devices['Desktop Firefox'], webkit: devices['Desktop Safari'] } as const

export default defineConfig({
  testDir: './e2e',
  timeout: 60_000,
  expect: { timeout: 10_000 },
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  reporter: [['list'], ['html', { open: 'never' }]],
  use: {
    baseURL,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    acceptDownloads: true,
  },
  projects: browsers
    .filter((b): b is keyof typeof desktop => b in desktop)
    .map((name) => ({ name, use: { ...desktop[name], viewport: { width: 1440, height: 900 } } })),
})

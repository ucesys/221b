// The Settings dialog: the theme, kept in the browser, and the refresh rate,
// kept on the server and applied to every open console at the same moment.
import type { Page } from '@playwright/test'
import { expect, test } from './test'
import type { Settings } from '../src/api/types'
import { inKind, kubectl, sleep } from './cluster'

const SIM = process.env.E2E_SIM_URL ?? 'http://127.0.0.1:7443'
const NAMESPACE = process.env.E2E_NAMESPACE ?? '221b'
const CONFIGMAP = 'k8s-console-221b'

// The console's own state is what to wait for, not the page's load event,
// which Firefox in CI has been seen never firing for a second console open
// in the same browser.
async function open(page: Page) {
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
}

async function openSettings(page: Page) {
  await page.getByRole('button', { name: 'Settings' }).click()
  const settings = page.getByTestId('settings-page')
  await expect(settings).toBeVisible()
  return settings
}

/** What the server keeps: the ConfigMap's settings.json, through kubectl or the simulator. */
async function savedSettings(): Promise<string> {
  if (inKind) return kubectl('-n', NAMESPACE, 'get', 'configmap', CONFIGMAP, '-o', 'jsonpath={.data.settings\\.json}')
  const res = await fetch(`${SIM}/api/v1/namespaces/${NAMESPACE}/configmaps/${CONFIGMAP}`)
  const cm = (await res.json()) as { data?: Record<string, string> }
  return cm.data?.['settings.json'] ?? ''
}

test.afterEach(async ({ request }) => {
  // Leave the default rate for the other tests.
  await request.put('/api/v1/settings', { data: { refreshSeconds: 3 } })
})

test('switches theme in Settings and remembers it in this browser', async ({ page, context }) => {
  await open(page)
  const root = page.locator('.rk')
  const before = (await root.getAttribute('data-theme')) ?? 'light'
  const after = before === 'dark' ? 'light' : 'dark'
  const dialog = await openSettings(page)
  await dialog.getByRole('group', { name: 'Theme' }).getByRole('button', { name: after === 'dark' ? 'Dark' : 'Light' }).click()
  await expect(root).toHaveAttribute('data-theme', after)
  // A page of its own: Escape goes back to where it was opened from.
  expect(new URL(page.url()).hash).toBe('#/settings')
  await page.keyboard.press('Escape')
  await expect(page.getByTestId('settings-page')).toBeHidden()
  expect(new URL(page.url()).hash).toMatch(/^(#\/?)?$/)
  await expect(page.getByRole('button', { name: 'Settings' })).toBeFocused()
  // The choice is this browser's: another tab of it opens with the theme.
  // (A new tab rather than a reload: Firefox in CI has been seen never
  // finishing a reload of a page that just switched its color scheme.)
  const other = await context.newPage()
  await other.goto('/', { waitUntil: 'domcontentloaded' })
  await expect(other.locator('.rk')).toHaveAttribute('data-theme', after)
  await other.close()
})

test('the refresh rate is kept on the server and reaches every open console', async ({ browser, page, request }) => {
  await open(page)
  const other = await browser.newPage()
  await open(other)

  const dialog = await openSettings(page)
  const rates = dialog.getByRole('group', { name: 'Refresh rate' })
  await expect(rates.getByRole('button', { name: '3 s' })).toHaveAttribute('aria-pressed', 'true')
  await expect(dialog.getByTestId('settings-status')).toContainText('Every 3 s')
  await expect(dialog.getByTestId('settings-status')).toContainText('Kept on the server in its ConfigMap')

  await rates.getByRole('button', { name: '10 s' }).click()
  await expect(rates.getByRole('button', { name: '10 s' })).toHaveAttribute('aria-pressed', 'true')
  await expect(dialog.getByTestId('settings-status')).toContainText('Every 10 s')

  // The server has it, and so does its ConfigMap.
  const saved = (await (await request.get('/api/v1/settings')).json()) as Settings
  expect(saved.refreshSeconds).toBe(10)
  expect(saved.storage).toBe('configmap')
  expect(saved.changedAt).toBeTruthy()
  await expect.poll(savedSettings).toContain('"refreshSeconds":10')

  // The other console learns it through its stream, without a reload.
  const otherDialog = await openSettings(other)
  await expect(otherDialog.getByRole('group', { name: 'Refresh rate' }).getByRole('button', { name: '10 s' })).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByTestId('live')).toHaveAttribute('title', /every 10 s/)
  await other.close()
})

test('every open console refreshes at the same moment', async ({ browser, page }) => {
  await open(page)
  const other = await browser.newPage()
  await open(other)
  const clock = async (p: Page) => (await p.getByTestId('live').locator('.rk-num').textContent()) ?? ''
  await expect(page.getByTestId('live')).toContainText(/refreshed \d/)
  // Sample both clocks for a few ticks. The server stamps every refresh with
  // one time and sends it to every browser at once, so both show the same
  // sequence of times, give or take the tick a sample straddles.
  const a = new Set<string>()
  const b = new Set<string>()
  for (let i = 0; i < 16; i++) {
    await sleep(500)
    const [x, y] = await Promise.all([clock(page), clock(other)])
    a.add(x)
    b.add(y)
  }
  expect(a.size).toBeGreaterThan(1)
  const shared = [...a].filter((t) => b.has(t))
  expect(shared.length).toBeGreaterThanOrEqual(Math.max(a.size, b.size) - 1)
  await other.close()
})

test('refuses a rate out of range and a request from another site', async ({ request }) => {
  const bad = await request.put('/api/v1/settings', { data: { refreshSeconds: 0 } })
  expect(bad.status()).toBe(400)
  const forged = await request.put('/api/v1/settings', { data: { refreshSeconds: 5 }, headers: { Origin: 'http://evil.example', 'Sec-Fetch-Site': 'cross-site' } })
  expect(forged.status()).toBe(403)
  const settings = (await (await request.get('/api/v1/settings')).json()) as Settings
  expect(settings.refreshSeconds).toBe(3)
})

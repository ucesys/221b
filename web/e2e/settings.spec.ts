// The Settings page under Administration (RB-47): the refresh rate, kept on the
// server and applied to every open console at the same moment; and the theme,
// personal and kept in the browser, which is chosen from the account menu.
import type { Page } from '@playwright/test'
import { expect, test } from './test'
import type { Settings } from '../src/api/types'
import { sleep } from './cluster'

// The console's own state is what to wait for, not the page's load event,
// which Firefox in CI has been seen never firing for a second console open
// in the same browser.
async function open(page: Page) {
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
}

async function openSettings(page: Page) {
  await page.getByTestId('admin-link').click()
  await page.locator('[data-admin-page=settings]').click()
  const settings = page.getByTestId('settings-page')
  await expect(settings).toBeVisible()
  return settings
}

test.afterEach(async ({ request }) => {
  // Leave the default rate for the other tests.
  await request.put('/api/v1/settings', { data: { refreshSeconds: 3 } })
})

test('switches theme from the account menu and remembers it in this browser', async ({ page, context }) => {
  await open(page)
  const root = page.locator('.rk')
  const before = (await root.getAttribute('data-theme')) ?? 'light'
  const after = before === 'dark' ? 'light' : 'dark'
  // With sign-in off there is no account menu; a button beside "Sign-in off" switches it.
  const toggle = page.getByTestId('theme-toggle')
  if (await toggle.count()) await toggle.click()
  else {
    await page.getByTestId('account').click()
    await page.getByTestId(`theme-${after}`).click()
  }
  await expect(root).toHaveAttribute('data-theme', after)
  // The choice is this browser's: another tab of it opens with the theme.
  // (A new tab rather than a reload: Firefox in CI has been seen never
  // finishing a reload of a page that just switched its color scheme.)
  const other = await context.newPage()
  await other.goto('/', { waitUntil: 'domcontentloaded' })
  await expect(other.locator('.rk')).toHaveAttribute('data-theme', after)
  await other.close()
})

test('Settings is a page under Administration: Escape goes back, and an old link lands on it', async ({ page }) => {
  await open(page)
  const dialog = await openSettings(page)
  await expect(dialog).toBeVisible()
  expect(new URL(page.url()).hash).toBe('#/admin/settings')
  await page.keyboard.press('Escape')
  await expect(page.getByTestId('settings-page')).toBeHidden()
  expect(new URL(page.url()).hash).toMatch(/^(#\/?)?$/)
  await expect(page.getByTestId('admin-link')).toBeFocused()
  await page.goto('/#/settings/watson', { waitUntil: 'domcontentloaded' })
  await expect(page.getByTestId('settings-page')).toBeVisible()
  await expect(page).toHaveURL(/#\/admin\/settings\/watson$/)
  await expect(page.locator('[data-testid=settings-section][data-section=watson]')).toHaveAttribute('aria-current', 'page')
})

// Administration in tabs (CU-18, CU-19): Auth, AI with its groups Agents
// and AIOps, Credentials, Settings, Logs, Toolbox, Backup; an address of
// before leads to its page, and the bar shows the address now.
test('Administration has seven tabs, AI has the agents’ and AIOps’ pages, and the addresses of before lead there', async ({ page }) => {
  await open(page)
  await page.getByTestId('admin-link').click()
  await expect(page.getByTestId('admin-page')).toBeVisible()
  await expect(page.locator('[data-admin-page]')).toHaveText(['Auth', 'AI', /^Credentials/, 'Settings', 'Logs', 'Toolbox', 'Backup'])
  await page.locator('[data-admin-page=ai]').click()
  await expect(page).toHaveURL(/#\/admin\/ai$/)
  await expect(page.getByTestId('settings-group')).toHaveText(['Agents', 'AIOps'])
  await expect(page.locator('[data-testid=settings-section]')).toHaveCount(8)
  await expect(page.locator('[data-testid=settings-section][data-section=providers]')).toHaveAttribute('aria-current', 'page')
  await page.locator('[data-admin-page=settings]').click()
  await expect(page.locator('[data-testid=settings-section]')).toHaveCount(4)
  for (const [old, now] of <[string, string][]>[
    ['#/admin/roles', '#/admin/auth/roles'],
    ['#/admin/users', '#/admin/auth/users'],
    ['#/admin/settings/agents/sherlock', '#/admin/ai/agents/sherlock'],
    ['#/admin/settings/limits', '#/admin/ai/limits'],
    ['#/admin/settings/sla', '#/admin/ai/sla'],
    ['#/admin/settings/credentials', '#/admin/credentials'],
    ['#/admin/settings/apps', '#/admin/settings/repositories'],
  ]) {
    await page.goto('/' + old, { waitUntil: 'domcontentloaded' })
    await expect(page).toHaveURL(new RegExp(now.replace(/[/]/g, '\\/') + '$'))
  }
  await expect(page.locator('[data-admin-page=settings]')).toHaveAttribute('aria-current', 'page')
  await page.goto('/#/admin/settings/agents/sherlock', { waitUntil: 'domcontentloaded' })
  await expect(page.getByTestId('agent-drawer')).toBeVisible()
})

test('the refresh rate is kept on the server and reaches every open console', async ({ browser, page, request }) => {
  await open(page)
  const other = await browser.newPage()
  await open(other)

  const dialog = await openSettings(page)
  const rates = dialog.getByRole('group', { name: 'Refresh rate' })
  await expect(rates.getByRole('button', { name: '3 s' })).toHaveAttribute('aria-pressed', 'true')
  await expect(dialog.getByTestId('settings-status')).toContainText('Now every 3 s')
  await expect(dialog.getByTestId('settings-status')).toContainText('Kept on the server in its database')

  // Like every Settings section, it waits for Save (6.3 review).
  await rates.getByRole('button', { name: '10 s' }).click()
  await expect(rates.getByRole('button', { name: '10 s' })).toHaveAttribute('aria-pressed', 'true')
  await expect(dialog.getByTestId('settings-status')).toContainText('Now every 3 s')
  await dialog.getByTestId('settings-save').click()
  await expect(dialog.getByTestId('settings-status')).toContainText('Now every 10 s')

  // The server has it, kept in its database (DB-69).
  const saved = (await (await request.get('/api/v1/settings')).json()) as Settings
  expect(saved.refreshSeconds).toBe(10)
  expect(saved.storage).toBe('database')
  expect(saved.changedAt).toBeTruthy()

  // The Pending grace period: 2 minutes until changed, saved the same way.
  const grace = dialog.getByRole('group', { name: 'Alert on a pending pod after' })
  await expect(grace.getByRole('button', { name: '2 min' })).toHaveAttribute('aria-pressed', 'true')
  await grace.getByRole('button', { name: '5 min' }).click()
  await dialog.getByTestId('settings-save').click()
  await expect.poll(async () => ((await (await request.get('/api/v1/settings')).json()) as Settings).pendingGraceSeconds).toBe(300)
  await grace.getByRole('button', { name: '2 min' }).click()
  await dialog.getByTestId('settings-save').click()
  await expect.poll(async () => ((await (await request.get('/api/v1/settings')).json()) as Settings).pendingGraceSeconds).toBe(120)

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
  // The time of the last refresh is in the live indicator's tooltip.
  const clock = async (p: Page) => /Last refresh ([^.]+)\./.exec((await p.getByTestId('live').getAttribute('title')) ?? '')?.[1] ?? ''
  await expect(page.getByTestId('live')).toHaveAttribute('title', /Last refresh \d/)
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

test('the Fleets tab can be hidden for everyone; fleets stay on the Nodes tab (R2-03)', async ({ page, request }) => {
  await open(page)
  await expect(page.locator('[data-tab=fleets]')).toBeVisible()
  const dialog = await openSettings(page)
  const seg = dialog.getByTestId('settings-fleets-page')
  await seg.getByRole('button', { name: 'Hide' }).click()
  await dialog.getByTestId('settings-save').click()
  await expect(dialog.getByText('Saved.')).toBeVisible()
  try {
    expect(((await (await request.get('/api/v1/settings')).json()) as Settings).fleetsPage).toBe(false)
    await page.goto('/#/nodes')
    await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
    await expect(page.locator('[data-tab=fleets]')).toHaveCount(0)
    await expect(page.getByTestId('node-fleets')).toBeVisible()
  } finally {
    const d = await openSettings(page)
    await d.getByTestId('settings-fleets-page').getByRole('button', { name: 'Show' }).click()
    await d.getByTestId('settings-save').click()
    await expect(d.getByText('Saved.')).toBeVisible()
  }
  await page.goto('/#/nodes')
  await expect(page.locator('[data-tab=fleets]')).toBeVisible()
})

// Sign-in, sessions, roles and the audit log (P3-01, P3-03, P3-10). The
// suite's own session comes from e2e/signin.setup.ts as an administrator;
// these tests open fresh contexts to see what a stranger and a viewer get.
import type { Browser, Page } from '@playwright/test'
import { expect, test } from './test'
import { E2E_PASSWORD, E2E_USER, E2E_VIEWER as VIEWER } from './accounts'
import { cluster } from './cluster'

/** Signs a fresh context in as an account, through whichever provider the console has. */
async function signIn(browser: Browser, email: string, password: string): Promise<Page> {
  const context = await browser.newContext({ storageState: undefined })
  const page = await context.newPage()
  await page.goto('/#/pods', { waitUntil: 'domcontentloaded' })
  const oidc = page.getByTestId('signin-oidc')
  if (await oidc.count()) {
    await oidc.click()
    await page.waitForURL(/\/oidc\/auth/)
    await page.fill('input[name="email"]', email)
    await page.fill('input[name="password"]', password)
    await page.click('button[type="submit"]')
  } else {
    const form = page.getByTestId('signin-form')
    await form.locator('input[name="email"]').fill(email)
    await form.locator('input[name="password"]').fill(password)
    await form.getByTestId('signin-submit').click()
  }
  return page
}

test('without a session the API answers 401 and every page shows the sign-in page', async ({ browser, request }) => {
  const me = (await (await request.get('/api/v1/me')).json()) as { mode: string }
  test.skip(me.mode === 'none', 'sign-in is off on this console')
  const context = await browser.newContext({ storageState: undefined })
  const page = await context.newPage()
  for (const path of ['/', '/#/pods?ns=web', '/#/helm']) {
    await page.goto(path, { waitUntil: 'domcontentloaded' })
    await expect(page.getByRole('heading', { level: 1 })).toContainText('221B')
    await expect(page.getByTestId('cluster')).toHaveCount(0)
  }
  const api = await context.request.get('/api/v1/snapshot')
  expect(api.status()).toBe(401)
  expect(await api.json()).toMatchObject({ signin: '/signin' })
  for (const probe of ['/healthz', '/readyz', '/metrics']) expect((await context.request.get(probe)).status(), probe).toBe(200)
  await context.close()
})

test('a wrong password is refused, and the right one comes back to the page asked for', async ({ browser, request }) => {
  const me = (await (await request.get('/api/v1/me')).json()) as { mode: string }
  test.skip(me.mode === 'none', 'sign-in is off on this console')
  const page = await signIn(browser, E2E_USER, 'not-the-password')
  await expect(page.getByText(/Wrong email or password|do not match/)).toBeVisible()
  await page.context().close()

  const ok = await signIn(browser, E2E_USER, E2E_PASSWORD)
  await expect(ok.getByTestId('cluster')).toBeVisible({ timeout: 20_000 })
  await expect(ok).toHaveURL(/#\/pods$/)
  await ok.getByTestId('account').click()
  await expect(ok.getByTestId('account-role')).toHaveText('Administrator')
  await ok.context().close()
})

test('a viewer uses the tabs but cannot change the settings; an admin can (P3-03)', async ({ browser, request }) => {
  const me = (await (await request.get('/api/v1/me')).json()) as { mode: string }
  test.skip(me.mode === 'none', 'sign-in is off on this console')
  const page = await signIn(browser, VIEWER, E2E_PASSWORD)
  await expect(page.locator('[data-pod]').first()).toBeVisible({ timeout: 20_000 })
  await page.getByTestId('account').click()
  await expect(page.getByTestId('account-role')).toHaveText('Viewer')
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: 'Settings' }).click()
  const rate = page.getByRole('group', { name: 'Refresh rate' }).getByRole('button')
  await expect(rate.first()).toBeDisabled()
  const denied = await page.context().request.put('/api/v1/settings', { data: { refreshSeconds: 5 }, headers: { 'Sec-Fetch-Site': 'same-origin' } })
  expect(denied.status()).toBe(403)
  await page.context().close()
  // The suite's own session is an administrator's.
  const current = (await (await request.get('/api/v1/settings')).json()) as { refreshSeconds: number }
  const allowed = await request.put('/api/v1/settings', { data: { refreshSeconds: current.refreshSeconds }, headers: { 'Sec-Fetch-Site': 'same-origin' } })
  expect(allowed.status()).toBe(200)
})

test('signing out ends the session, and the audit log names who signed in and out (P3-10)', async ({ browser, request }) => {
  const me = (await (await request.get('/api/v1/me')).json()) as { mode: string }
  test.skip(me.mode === 'none', 'sign-in is off on this console')
  const page = await signIn(browser, VIEWER, E2E_PASSWORD)
  await expect(page.getByTestId('cluster')).toBeVisible({ timeout: 20_000 })
  await page.getByTestId('account').click()
  await page.getByTestId('signout').click()
  await expect(page).toHaveURL(/\/signin/)
  expect((await page.context().request.get('/api/v1/me')).status()).toBe(401)
  await page.context().close()

  await expect.poll(async () => (await cluster.auditLog()).filter((e) => e.action === 'signout').length, { timeout: 15_000 }).toBeGreaterThan(0)
  const records = await cluster.auditLog()
  const signin = records.filter((e) => e.action === 'signin' && (e.who as { email?: string }).email === VIEWER)
  const signout = records.filter((e) => e.action === 'signout' && (e.who as { email?: string }).email === VIEWER)
  expect(signin.length, 'one sign-in record per sign-in').toBeGreaterThan(0)
  expect(signout.length, 'one sign-out record').toBeGreaterThan(0)
  for (const r of [...signin, ...signout]) {
    expect(r).toMatchObject({ outcome: 'ok', who: { email: VIEWER } })
    expect(typeof r.time).toBe('string')
    expect(JSON.stringify(r)).not.toContain(E2E_PASSWORD)
  }
  if (me.mode === 'local') {
    // With local accounts the console sees the wrong password; a provider keeps that to itself.
    const failed = records.filter((e) => e.action === 'signin.failed')
    expect(failed.length, 'the wrong password was recorded').toBeGreaterThan(0)
  }
})

test('a request from another site cannot change the settings (X-06)', async ({ request }) => {
  const res = await request.put('/api/v1/settings', { data: { refreshSeconds: 5 }, headers: { Origin: 'https://evil.example', 'Sec-Fetch-Site': 'cross-site' } })
  expect(res.status()).toBe(403)
})

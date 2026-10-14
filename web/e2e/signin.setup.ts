// Signs in once before the suite and keeps the session for every test
// (P3-01): through the simulated OIDC provider (make e2e-sim), or the local
// accounts the chart in kind is installed with. A console with sign-in off
// needs nothing. The session goes to e2e/.auth/<project>.json, which the
// browser contexts and the request fixture both load.
import { expect, test as setup } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { E2E_PASSWORD, E2E_USER } from './accounts'

function sessionFile(project: string): string {
  return `e2e/.auth/${project}.json`
}

setup('sign in', async ({ page }, testInfo) => {
  await mkdir('e2e/.auth', { recursive: true })
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  const oidc = page.getByTestId('signin-oidc')
  const local = page.getByTestId('signin-form')
  await expect(oidc.or(local).or(page.getByTestId('cluster'))).toBeVisible({ timeout: 20_000 })
  if (await oidc.count()) {
    await oidc.click()
    await page.waitForURL(/\/oidc\/auth/)
    await page.fill('input[name="email"]', E2E_USER)
    await page.fill('input[name="password"]', E2E_PASSWORD)
    await page.click('button[type="submit"]')
  } else if (await local.count()) {
    await local.locator('input[name="email"]').fill(E2E_USER)
    await local.locator('input[name="password"]').fill(E2E_PASSWORD)
    await local.getByTestId('signin-submit').click()
  }
  await expect(page.getByTestId('cluster')).toBeVisible({ timeout: 20_000 })
  const me = await page.evaluate(() => fetch('/api/v1/me').then((r) => r.json() as Promise<{ mode: string; admin: boolean }>))
  expect(me.admin, 'the test account is an administrator').toBe(true)
  await page.context().storageState({ path: sessionFile(testInfo.project.name) })
})

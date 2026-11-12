// The accounts the end-to-end suite signs in with: the simulated provider's
// (make e2e-sim) or the local accounts the chart in kind is installed with.
import type { Browser, Page } from '@playwright/test'

export const E2E_USER = process.env.E2E_USER ?? 'admin@corp.example'
export const E2E_PASSWORD = process.env.E2E_PASSWORD ?? 'password'
export const E2E_VIEWER = process.env.E2E_VIEWER ?? 'viewer@corp.example'
/** The name Kubernetes knows the viewer by: a local user is prefixed in kind. */
export const E2E_VIEWER_KUBE = process.env.E2E_VIEWER_KUBE ?? E2E_VIEWER
/** A person Kubernetes lets list only some namespaces (P3-02). */
export const E2E_DEV = process.env.E2E_DEV ?? 'dev@corp.example'
/** The name Kubernetes knows the dev by: a local user is prefixed in kind. */
export const E2E_DEV_KUBE = process.env.E2E_DEV_KUBE ?? E2E_DEV
/** A second SuperAdmin, who approves what the first asks for (the simulated provider has one). */
export const E2E_ADMIN2 = process.env.E2E_ADMIN2 ?? 'admin2@corp.example'

/**
 * Approves, as the second SuperAdmin, the console changes that wait: a
 * cluster taken off production waits for a second person where one could
 * approve it. Where the first is the only SuperAdmin it applied at once.
 */
export async function approveConsoleChanges(browser: Browser): Promise<void> {
  const page = await signInAs(browser, E2E_ADMIN2)
  try {
    await page.getByTestId('cluster-state').waitFor({ timeout: 30_000 })
    const list = (await (await page.request.get('/api/v1/admin/approvals')).json()) as { items: { kind: string; state: string; id?: string; own?: boolean }[] }
    for (const r of list.items) {
      if (r.kind === 'admin' && r.state === 'pending' && !r.own && r.id) {
        await page.request.post(`/api/v1/admin/requests/${r.id}/approve`, { data: {}, headers: { 'Sec-Fetch-Site': 'same-origin' } })
      }
    }
  } finally {
    await page.context().close()
  }
}

/** A new browser context signed in as someone, through the simulated provider or the local form. */
export async function signInAs(browser: Browser, email: string, password: string = E2E_PASSWORD): Promise<Page> {
  const context = await browser.newContext({ storageState: undefined })
  const page = await context.newPage()
  await page.goto('/#/nodes', { waitUntil: 'domcontentloaded' })
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

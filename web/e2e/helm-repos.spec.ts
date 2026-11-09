// Helm repositories and OCI registries (Phase 12, wave 5, HR-02 to HR-06):
// Settings › Applications keeps them with Test, a password written and
// never read back; the Helm tab's new application takes a chart of one,
// or of an OCI registry, at a version. Nothing here reaches the internet:
// the repository is at an address that does not resolve, so Test says it
// cannot be reached, and the new application's request is looked at in
// the browser and answered there. The fetching itself is covered by the
// server's tests against fake repositories (internal/apps/repotest).
import { expect, test } from './test'

test.describe.configure({ mode: 'serial' })

const SAME = { 'Sec-Fetch-Site': 'same-origin' }
const SHERLOCK_ON = { clusters: { local: true }, model: 'claude-opus-5-5', dailyUSD: 20, monthlyUSD: 200 }
const NAME = 'e2e-charts'

test.beforeAll(async ({ browser }) => {
  const page = await browser.newPage()
  await page.goto('/#/helm', { waitUntil: 'domcontentloaded' })
  expect((await page.request.put('/api/v1/sherlock/config', { data: SHERLOCK_ON, headers: SAME })).status()).toBe(200)
  await page.close()
})

test.afterAll(async ({ browser }) => {
  const page = await browser.newPage()
  await page.goto('/#/helm', { waitUntil: 'domcontentloaded' })
  await page.request.delete(`/api/v1/helm-repositories/${NAME}`, { headers: SAME })
  await page.request.put('/api/v1/sherlock/config', { data: { ...SHERLOCK_ON, clusters: {} }, headers: SAME })
  await page.close()
})

test('Settings › Applications adds, tests, changes and removes a repository; its password is never read back (HR-02, HR-03)', async ({ page }) => {
  await page.goto('/#/admin/settings/apps')
  const section = page.getByTestId('helm-repos-settings')
  await expect(section).toBeVisible()
  await expect(page.locator('[data-testid=settings-section][data-section=apps]')).toHaveAttribute('aria-current', 'page')
  await section.getByTestId('helm-repo-add').click()
  const editor = section.getByTestId('helm-repo-editor')
  await editor.getByTestId('helm-repo-name').fill(NAME)
  await editor.getByTestId('helm-repo-url').fill('https://charts.e2e.invalid/stable/index.yaml')
  await editor.getByTestId('helm-repo-auth-basic').click()
  await expect(editor.getByTestId('helm-repo-save')).toBeDisabled()
  await editor.getByTestId('helm-repo-username').fill('reader')
  await editor.getByTestId('helm-repo-secret').fill('e2e-password-not-to-be-seen')
  // Test before saving: the address does not resolve.
  await editor.getByTestId('helm-repo-try').click()
  await expect(editor.getByTestId('helm-repo-test-result')).toHaveAttribute('data-tone', 'err')
  await editor.getByTestId('helm-repo-save').click()
  const row = section.locator(`[data-testid=helm-repo-row][data-repo="${NAME}"]`)
  await expect(row).toContainText('https://charts.e2e.invalid/stable')
  await expect(row).toContainText('User reader and a password')
  const list = await page.request.get('/api/v1/helm-repositories', { headers: SAME })
  const body = await list.text()
  expect(body).toContain(`"name":"${NAME}"`)
  expect(body).toContain('"hasSecret":true')
  expect(body).not.toContain('e2e-password-not-to-be-seen')
  // Tested from the list, the result is kept.
  await row.getByTestId('helm-repo-test').click()
  await expect(row.getByTestId('helm-repo-tested')).toContainText('Failed')
  // Changed without the password, it keeps it; to an OCI registry, it asks for it again.
  await row.getByTestId('helm-repo-edit').click()
  await expect(editor.getByTestId('helm-repo-secret')).toHaveAttribute('placeholder', /kept/)
  await editor.getByTestId('helm-repo-kind-oci').click()
  await editor.getByTestId('helm-repo-url').fill('oci://registry.e2e.invalid/charts')
  await expect(editor.getByTestId('helm-repo-save')).toBeDisabled()
  await editor.getByTestId('helm-repo-auth-none').click()
  await editor.getByTestId('helm-repo-save').click()
  await expect(row).toContainText('OCI registry')
  await expect(row).toContainText('None')
  // Back to a Helm repository without credentials, for the next test.
  await row.getByTestId('helm-repo-edit').click()
  await editor.getByTestId('helm-repo-kind-helm').click()
  await editor.getByTestId('helm-repo-url').fill('https://charts.e2e.invalid/stable')
  await editor.getByTestId('helm-repo-save').click()
  await expect(row).toContainText('Helm repository')
})

test('the new application takes a Helm repository’s chart or an OCI registry’s, at a version (HR-05, HR-06)', async ({ page }) => {
  await page.goto('/#/helm')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  const sent: unknown[] = []
  await page.route('**/api/v1/**apps', async (route) => {
    if (route.request().method() !== 'POST') return route.fallback()
    sent.push(route.request().postDataJSON())
    await route.fulfill({ status: 400, contentType: 'application/json', body: JSON.stringify({ error: 'stopped by the test' }) })
  })
  await page.getByTestId('apps-new').click()
  const dlg = page.getByTestId('app-dialog')
  await dlg.getByTestId('app-namespace').fill('web')
  await dlg.getByTestId('app-name').fill('storefront')
  await dlg.getByTestId('app-kind-helm').click()
  await expect(dlg.getByTestId('app-repo')).toHaveCount(0)
  await expect(dlg.getByTestId('app-token')).toHaveCount(0)
  await dlg.getByTestId('app-repository').selectOption(NAME)
  await dlg.getByTestId('app-chart').fill('shop')
  await dlg.getByTestId('app-version-text').fill('1.2.0')
  await dlg.getByTestId('app-submit').click()
  await expect(dlg.getByText('stopped by the test')).toBeVisible()
  expect(sent[0]).toMatchObject({ namespace: 'web', name: 'storefront', source: { kind: 'helm', repository: NAME, chart: 'shop', ref: '1.2.0', refKind: 'version' } })
  // An OCI registry's chart is its address.
  await dlg.getByTestId('app-kind-oci').click()
  await dlg.getByTestId('app-oci').fill('oci://registry.e2e.invalid/org/charts/app')
  await dlg.getByTestId('app-version-text').fill('0.3.0')
  await dlg.getByTestId('app-submit').click()
  await expect.poll(() => sent.length).toBe(2)
  expect(sent[1]).toMatchObject({ source: { kind: 'oci', repo: 'oci://registry.e2e.invalid/org/charts', chart: 'app', ref: '0.3.0' } })
})

// Helm repositories and OCI registries (Phase 12, wave 5, HR-02 to HR-06):
// Administration › Settings › Repositories (Applications before 0.12.1, RP-01) keeps them
// with Test, and the git repositories applications' Setup picks (RP-02,
// RP-03, RP-06), each naming a credential of
// Administration › Credentials (0.12.1, CR-11), whose password is written there
// and never read back; the Helm tab's new application takes a chart of one,
// or of an OCI registry, at a version. Nothing here reaches the internet:
// the repository is at an address that does not resolve, so Test says it
// cannot be reached, and the new application's request is looked at in
// the browser and answered there. The fetching itself is covered by the
// server's tests against fake repositories (internal/apps/repotest).
import { releaseRows, snapshot } from './cluster'
import { expect, test } from './test'

test.describe.configure({ mode: 'serial' })

const SAME = { 'Sec-Fetch-Site': 'same-origin' }
const SHERLOCK_ON = { clusters: { local: true }, model: 'claude-opus-5-5', dailyUSD: 20, monthlyUSD: 200 }
const NAME = 'e2e-charts'
const GIT = 'e2e-git'
const CRED = 'e2e charts reader'
let credId = ''

test.beforeAll(async ({ browser }) => {
  const page = await browser.newPage()
  await page.goto('/#/helm', { waitUntil: 'domcontentloaded' })
  expect((await page.request.put('/api/v1/sherlock/config', { data: SHERLOCK_ON, headers: SAME })).status()).toBe(200)
  const made = await page.request.post('/api/v1/credentials', { data: { name: CRED, kind: 'userpass', endpoint: 'https://charts.e2e.invalid', username: 'reader', secret: { password: 'e2e-password-not-to-be-seen' } }, headers: SAME })
  expect(made.status()).toBe(201)
  credId = ((await made.json()) as { id: string }).id
  await page.close()
})

test.afterAll(async ({ browser }) => {
  const page = await browser.newPage()
  await page.goto('/#/helm', { waitUntil: 'domcontentloaded' })
  await page.request.delete(`/api/v1/helm-repositories/${NAME}`, { headers: SAME })
  await page.request.delete(`/api/v1/helm-repositories/${GIT}`, { headers: SAME })
  await page.request.delete(`/api/v1/credentials/${credId}`, { headers: SAME })
  await page.request.put('/api/v1/sherlock/config', { data: { ...SHERLOCK_ON, clusters: {} }, headers: SAME })
  await page.close()
})

test('Administration › Settings › Repositories adds, tests, changes and removes a repository naming its credential, whose password is never read back (HR-02, HR-03, CR-11, RP-01)', async ({ page }) => {
  // The address of Settings › Applications leads here.
  await page.goto('/#/admin/settings/apps')
  await expect(page).toHaveURL(/#\/admin\/settings\/repositories$/)
  const section = page.getByTestId('helm-repos-settings')
  await expect(section).toBeVisible()
  await expect(page.locator('[data-testid=settings-section][data-section=repositories]')).toHaveAttribute('aria-current', 'page')
  await expect(page.getByRole('heading', { level: 2, name: 'Repositories', exact: true })).toBeVisible()
  await section.getByTestId('helm-repo-add').click()
  const editor = section.getByTestId('helm-repo-editor')
  // A git repository first; this one is a Helm repository.
  await expect(editor.getByTestId('helm-repo-kind-git')).toHaveAttribute('aria-pressed', 'true')
  await editor.getByTestId('helm-repo-kind-helm').click()
  await editor.getByTestId('helm-repo-name').fill(NAME)
  await editor.getByTestId('helm-repo-url').fill('https://charts.e2e.invalid/stable/index.yaml')
  // No password of its own: the one credential that covers it is preselected.
  await expect(editor.getByTestId('helm-repo-secret')).toHaveCount(0)
  await expect(editor.getByTestId('helm-repo-credential-picker-select')).toHaveValue(credId)
  // Test before saving: the address does not resolve.
  await editor.getByTestId('helm-repo-try').click()
  await expect(editor.getByTestId('helm-repo-test-result')).toHaveAttribute('data-tone', 'err')
  await editor.getByTestId('helm-repo-save').click()
  const row = section.locator(`[data-testid=helm-repo-row][data-repo="${NAME}"]`)
  await expect(row).toContainText('https://charts.e2e.invalid/stable')
  await expect(row.getByTestId('helm-repo-credential')).toHaveText(CRED)
  const list = await page.request.get('/api/v1/helm-repositories', { headers: SAME })
  const body = await list.text()
  expect(body).toContain(`"name":"${NAME}"`)
  expect(body).toContain(`"credential":"${credId}"`)
  expect(body).not.toContain('e2e-password-not-to-be-seen')
  // Tested from the list, the result is kept.
  await row.getByTestId('helm-repo-test').click()
  await expect(row.getByTestId('helm-repo-tested')).toContainText('Failed')
  // Moved to an address its credential does not cover: said so, and refused until none or another is chosen.
  // A repository keeps its kind: what names it expects one.
  await row.getByTestId('helm-repo-edit').click()
  await expect(editor.getByTestId('helm-repo-kind-oci')).toHaveCount(0)
  await editor.getByTestId('helm-repo-url').fill('https://elsewhere.e2e.invalid/stable')
  await expect(editor.getByTestId('helm-repo-credential-picker-hint')).toContainText('does not cover')
  await editor.getByTestId('helm-repo-credential-picker-select').selectOption('')
  await editor.getByTestId('helm-repo-save').click()
  await expect(row).toContainText('https://elsewhere.e2e.invalid/stable')
  await expect(row.getByTestId('helm-repo-credential')).toHaveText('None (public)')
  // Back to its address without credentials, for the next test.
  await row.getByTestId('helm-repo-edit').click()
  await editor.getByTestId('helm-repo-url').fill('https://charts.e2e.invalid/stable')
  await editor.getByTestId('helm-repo-credential-picker-select').selectOption('')
  await editor.getByTestId('helm-repo-save').click()
  await expect(row).toContainText('Helm repository')
  await expect(row.getByTestId('helm-repo-used-by')).toHaveText('Nothing')
})

test('a git repository is listed, picked by an application’s Setup, and not removed while it is used (RP-02, RP-03, RP-06)', async ({ page, request }) => {
  const r = releaseRows(await snapshot(request, ['helm']))[0]!
  await request.delete(`/api/v1/apps/${r.namespace}/${r.name}?record=1`, { headers: SAME })
  try {
    // Listed in Administration › Settings › Repositories: a public git repository with its default branch.
    await page.goto('/#/admin/settings/repositories')
    const section = page.getByTestId('helm-repos-settings')
    await section.getByTestId('helm-repo-add').click()
    const editor = section.getByTestId('helm-repo-editor')
    await editor.getByTestId('helm-repo-name').fill(GIT)
    await editor.getByTestId('helm-repo-url').fill('https://git.e2e.invalid/org/shop.git')
    await editor.getByTestId('helm-repo-branch').fill('main')
    await expect(editor.getByTestId('helm-repo-credential-picker-select')).toHaveValue('')
    await editor.getByTestId('helm-repo-save').click()
    const row = section.locator(`[data-testid=helm-repo-row][data-repo="${GIT}"]`)
    await expect(row).toContainText('Git repository')
    await expect(row).toContainText('https://git.e2e.invalid/org/shop')
    await expect(row.getByTestId('helm-repo-used-by')).toHaveText('Nothing')

    // Picked by a release's Setup, which keeps its own path and takes the default branch.
    await page.goto(`/#/helm/${r.namespace}/${r.name}`)
    await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
    const panel = page.getByTestId('app-panel')
    await panel.getByTestId('app-src-path').fill('charts/shop')
    await panel.getByTestId('app-src-repository-select').selectOption(GIT)
    await expect(panel.getByTestId('app-src-repository-hint')).toContainText('Public: no credential')
    await expect(panel.getByTestId('app-src-ref')).toHaveValue('main')
    await expect(panel.getByTestId('app-source-state')).toHaveText('Saved', { timeout: 30_000 })
    const app = (await (await request.get(`/api/v1/apps/${r.namespace}/${r.name}`, { headers: SAME })).json()) as { source: Record<string, string> }
    expect(app.source).toMatchObject({ repo: 'https://git.e2e.invalid/org/shop', repository: GIT, path: 'charts/shop', ref: 'main' })

    // Used by it, and not removed while it is, saying by whom.
    await page.goto('/#/admin/settings/repositories')
    await expect(row.getByTestId('helm-repo-used-by')).toContainText(`application ${r.namespace}/${r.name}`)
    await row.getByTestId('helm-repo-remove').click()
    const confirm = page.getByTestId('confirm')
    await expect(confirm).toContainText('It is used by')
    await confirm.getByTestId('confirm-ok').click()
    await expect(confirm).toContainText(`application ${r.namespace}/${r.name}`)
    await confirm.getByRole('button', { name: 'Cancel' }).click()
    await expect(row).toBeVisible()
  } finally {
    await request.delete(`/api/v1/apps/${r.namespace}/${r.name}?record=1`, { headers: SAME })
  }
  // Once nothing names it, it goes.
  expect((await request.delete(`/api/v1/helm-repositories/${GIT}`, { headers: SAME })).status()).toBe(204)
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
  await expect(dlg.getByTestId('app-git-repository')).toHaveCount(0)
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

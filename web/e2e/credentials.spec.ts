// Administration › Credentials (0.12.1, CR-01 to CR-14): a SuperAdmin adds a
// credential whose secret never comes back; a git repository of Administration › Settings ›
// Repositories, added from a knowledge source's drawer, picks it by name,
// the only one that covers its address preselected, and an application
// (its release's Setup card) picks that repository (RP-03); while the
// repository uses it, Administration › Credentials lists it under Used by and
// refuses to remove it, saying by whom. Nothing here reaches the internet:
// the addresses do not resolve.
import { cluster, releaseRows, snapshot } from './cluster'
import { expect, test } from './test'

const SAME = { 'Sec-Fetch-Site': 'same-origin' }
const NAME = 'E2E git reader'
const SECRET = 'e2e-git-token-never-shown-4b2f'
const SOURCE = 'e2e-runbooks-repo'
const REPO = 'e2e-runbooks'
const SHERLOCK_ON = { clusters: { local: true }, model: 'claude-opus-5-5', dailyUSD: 20, monthlyUSD: 200 }

// A release's Setup card is changed through Watson, which runs where Sherlock is on.
test.beforeAll(async ({ browser }) => {
  const page = await browser.newPage()
  await page.goto('/#/helm', { waitUntil: 'domcontentloaded' })
  const on = await page.request.put('/api/v1/sherlock/config', { data: SHERLOCK_ON, headers: SAME })
  expect(on.status(), await on.text()).toBe(200)
  await page.close()
})

test.afterAll(async ({ browser }) => {
  const page = await browser.newPage()
  await page.goto('/#/helm', { waitUntil: 'domcontentloaded' })
  await page.request.put('/api/v1/sherlock/config', { data: { ...SHERLOCK_ON, clusters: {} }, headers: SAME })
  await page.close()
})

test('a credential is picked by a knowledge source and an application, and cannot be removed while they use it', async ({ page, request }) => {
  const releases = releaseRows(await snapshot(request, ['helm']))
  const r = releases[0]!
  await request.delete(`/api/v1/apps/${r.namespace}/${r.name}?record=1`, { headers: SAME })
  let credId = ''
  try {
    // Added in Administration › Credentials: a token sent as Basic for x-access-token.
    await page.goto('/#/admin/credentials')
    const settings = page.getByTestId('credentials-settings')
    await expect(settings).toBeVisible()
    await expect(page.locator('[data-admin-page=credentials]')).toHaveAttribute('aria-current', 'page')
    await settings.getByTestId('credential-add').click()
    const drawer = page.getByTestId('credential-drawer')
    await drawer.getByTestId('credential-name').fill(NAME)
    await drawer.getByTestId('credential-endpoint').fill('https://git.e2e.invalid/org')
    await drawer.getByTestId('credential-send-basic').click()
    await drawer.getByTestId('credential-basic-user').fill('x-access-token')
    await drawer.getByTestId('credential-secret-token').fill(SECRET)
    await drawer.getByTestId('credential-save').click()
    const row = settings.locator(`[data-testid=credential-row][data-credential="${NAME}"]`)
    await expect(row).toContainText('https://git.e2e.invalid/org')
    await expect(row).toContainText('Basic as x-access-token')
    const list = await (await request.get('/api/v1/credentials', { headers: SAME })).text()
    expect(list).not.toContain(SECRET)
    credId = (JSON.parse(list) as { credentials: { id: string; name: string }[] }).credentials.find((c) => c.name === NAME)!.id

    // A knowledge source's git repository, added from its drawer: the credential under it preselected.
    await page.goto('/#/admin/ai/knowledge')
    await page.getByTestId('knowledge-add').click()
    const source = page.getByTestId('knowledge-source-drawer')
    await source.getByTestId('knowledge-source-kind').selectOption('git')
    await source.getByTestId('knowledge-source-name').fill('E2E runbooks repo')
    await source.getByTestId('knowledge-source-repository-select').selectOption('__add')
    const add = source.getByTestId('knowledge-source-repository-new')
    await add.getByTestId('knowledge-source-repository-new-url').fill('https://git.e2e.invalid/org/runbooks.git')
    await expect(add.getByTestId('knowledge-source-repository-new-name')).toHaveValue('runbooks')
    await add.getByTestId('knowledge-source-repository-new-name').fill(REPO)
    await expect(add.getByTestId('knowledge-source-repository-new-credential-select')).toHaveValue(credId)
    await add.getByTestId('knowledge-source-repository-new-save').click()
    await expect(source.getByTestId('knowledge-source-repository-select')).toHaveValue(REPO)
    await expect(source.getByTestId('knowledge-source-repository-hint')).toContainText(`Fetched with ${NAME}`)
    await source.getByTestId('knowledge-source-save').click()
    await expect(page.locator(`[data-testid=knowledge-source][data-source=${SOURCE}]`)).toContainText(`credential ${NAME}`)

    // An application's Setup card picks the same repository: fetched with its credential.
    await page.goto('/#/helm')
    await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
    await page.getByTestId('apps-table').locator(`[data-release="${r.key}"]`).click()
    const panel = page.getByTestId('app-panel')
    await expect(panel.getByTestId('app-src-repository-select')).toBeEnabled()
    await panel.getByTestId('app-src-ref').fill('main')
    await panel.getByTestId('app-src-ref').blur()
    await panel.getByTestId('app-src-repository-select').selectOption(REPO)
    await expect(panel.getByTestId('app-src-repository-hint')).toContainText(`Fetched with ${NAME}`)
    await expect(panel.getByTestId('app-source-state')).toHaveText('Saved', { timeout: 15_000 })
    const app = await (await request.get(`/api/v1/apps/${r.namespace}/${r.name}`, { headers: SAME })).text()
    expect(app).toContain(`"repository":"${REPO}"`)
    expect(app).toContain(`"credential":"${credId}"`)
    expect(app).not.toContain(SECRET)

    // Administration › Settings › Repositories says what uses the repository.
    await page.goto('/#/admin/settings/repositories')
    const repoRow = page.locator(`[data-testid=helm-repo-row][data-repo="${REPO}"]`)
    await expect(repoRow.getByTestId('helm-repo-used-by')).toContainText('knowledge source E2E runbooks repo')
    await expect(repoRow.getByTestId('helm-repo-used-by')).toContainText(`application ${r.namespace}/${r.name}`)

    // The credential is used by the repository; not removed while it is, saying by whom.
    await page.goto('/#/admin/credentials')
    await expect(row.getByTestId('credential-used-by')).toContainText(`git repository ${REPO}`)
    await row.getByTestId('credential-remove').click()
    const confirm = page.getByTestId('confirm')
    await expect(confirm).toContainText('It is used by')
    await confirm.getByTestId('confirm-ok').click()
    await expect(confirm).toContainText('choose another credential there first')
    await confirm.getByRole('button', { name: 'Cancel' }).click()
    await expect(row).toBeVisible()
    expect(JSON.stringify(await cluster.auditLog())).not.toContain(SECRET)
  } finally {
    await request.delete(`/api/v1/apps/${r.namespace}/${r.name}?record=1`, { headers: SAME })
    await request.delete(`/api/v1/knowledge/sources/${SOURCE}`, { headers: SAME })
    await request.delete(`/api/v1/helm-repositories/${REPO}`, { headers: SAME })
    if (credId) expect((await request.delete(`/api/v1/credentials/${credId}`, { headers: SAME })).status()).toBe(204)
  }
})

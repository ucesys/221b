// The Helm1 page (Phase 10, HA-01 to HA-19): every release in one table
// with its source and drift, a release's panel with its history, diffs and
// images, the tools an Admin gets and a Viewer does not, and on the kind
// cluster an install from a public git repository through Watson.
import { E2E_VIEWER, signInAs } from './accounts'
import { cluster, inKind, releaseRows, snapshot } from './cluster'
import { expect, test } from './test'

const SAME = { 'Sec-Fetch-Site': 'same-origin' }
const SHERLOCK_ON = { clusters: { local: true }, model: 'claude-opus-5-5', dailyUSD: 20, monthlyUSD: 200 }

// The tools need Watson, which runs where Sherlock is on.
test.beforeAll(async ({ browser }) => {
  const page = await browser.newPage()
  await page.goto('/#/helm1', { waitUntil: 'domcontentloaded' })
  const on = await page.request.put('/api/v1/sherlock/config', { data: SHERLOCK_ON, headers: SAME })
  expect(on.status(), await on.text()).toBe(200)
  await page.close()
})

test.afterAll(async ({ browser }) => {
  const page = await browser.newPage()
  await page.goto('/#/helm1', { waitUntil: 'domcontentloaded' })
  await page.request.put('/api/v1/sherlock/config', { data: { ...SHERLOCK_ON, clusters: {} }, headers: SAME })
  await page.close()
})

test('lists every release with its chart, revision, status, drift and source (HA-01)', async ({ page, request }) => {
  const releases = releaseRows(await snapshot(request, ['helm']))
  expect(releases.length).toBeGreaterThan(0)
  await page.goto('/#/helm1')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  const table = page.getByTestId('apps-table')
  await expect(table.locator('[data-release]')).toHaveCount(releases.length)
  const first = releases[0]!
  const row = table.locator(`[data-release="${first.key}"]`)
  await expect(row).toContainText(first.name)
  await expect(row).toContainText(first.namespace)
  await expect(row).toContainText(`${first.chart}-${first.chartVersion}`)
  await expect(row).toContainText(String(first.revision))
  // No source kept: no drift and no source.
  await expect(row).toContainText('—')
  const ok = await request.get('/api/v1/apps')
  expect(ok.status()).toBe(200)
  expect(await ok.json()).toEqual([])
})

test('a release opens beside the table: overview, history with a diff of two revisions, and images (HA-14 to HA-17)', async ({ page, request }) => {
  const releases = releaseRows(await snapshot(request, ['helm']))
  const multi = releases.find((r) => r.revisions > 1) ?? releases[0]!
  await page.goto('/#/helm1')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  await page.getByTestId('apps-table').locator(`[data-release="${multi.key}"]`).click()
  await expect(page).toHaveURL(new RegExp(`#/helm1/${multi.namespace}/${multi.name}`))
  const panel = page.getByTestId('app-panel')
  await expect(panel.getByTestId('app-title')).toContainText(multi.name)
  await expect(panel.getByTestId('app-source-line')).toContainText('keeps no source')
  // The table folds into the side list, with the Helm tab's details.
  await expect(page.getByTestId('apps-table').getByRole('columnheader', { name: /^Source/ })).toHaveCount(0)
  await expect(page.getByTestId('apps-table').locator('.rk-rrow', { hasText: multi.name })).toContainText(`${multi.chart}-${multi.chartVersion}`)
  await expect(page.getByTestId('apps-addons')).toBeVisible()
  // The panel shows what the Helm tab shows: the cards and the pods section.
  await expect(panel.getByTestId('release-cards')).toBeVisible()
  await panel.locator('[data-subtab="pods"]').click()
  await expect(panel.getByRole('heading', { name: 'Pods in this release' })).toBeVisible()

  await panel.locator('[data-subtab="history"]').click()
  await expect(panel.locator('[data-revision]')).toHaveCount(multi.revisions)
  if (multi.revisions > 1) {
    await panel.getByTestId('app-diff-from').selectOption('1')
    await panel.getByTestId('app-diff-to').selectOption(String(multi.revisions))
    await panel.getByTestId('app-compare').click()
    await expect(panel.getByTestId('app-diff').or(panel.getByTestId('app-diff-same'))).toBeVisible({ timeout: 15_000 })
  }
  const images = await request.get(`/api/v1/apps/${multi.namespace}/${multi.name}/images`)
  expect(images.status()).toBe(200)
  const list = (await images.json()) as { revision: number; images: { image: string }[] }
  await panel.locator('[data-subtab="images"]').click()
  if (list.images.length) await expect(panel.getByTestId('app-images').locator('[data-image]')).toHaveCount(list.images.length)
  else await expect(panel).toContainText('runs no container')

  // Close returns to the full table.
  await panel.getByTestId('app-close').click()
  await expect(page).toHaveURL(/#\/helm1$/)
  await expect(page.getByTestId('apps-table').getByRole('columnheader', { name: /^Source/ })).toBeVisible()
})

test('a source is set in place, refused when it is not https, and forgotten; a Viewer may do none of it (HA-02, HA-11)', async ({ page, request, browser }) => {
  const releases = releaseRows(await snapshot(request, ['helm']))
  const r = releases[0]!
  // A record a failed run left behind would get in the way.
  await request.delete(`/api/v1/apps/${r.namespace}/${r.name}?record=1`, { headers: SAME })
  await page.goto(`/#/helm1/${r.namespace}/${r.name}`)
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  const panel = page.getByTestId('app-panel')
  await expect(panel.getByTestId('app-source-state')).toHaveText('Not set')
  await panel.getByTestId('app-src-repo').fill('http://github.com/ucesys/221b')
  await panel.getByTestId('app-src-ref').fill('main')
  await panel.getByTestId('app-src-ref').blur()
  await expect(panel.getByTestId('app-source-state')).toContainText('https://')
  await panel.getByTestId('app-src-repo').fill('github.com/ucesys/221b')
  await panel.getByTestId('app-src-path').fill('deploy/helm/221b')
  await panel.getByTestId('app-src-path').blur()
  await expect(panel.getByTestId('app-source-state')).toHaveText('Saved', { timeout: 10_000 })
  await expect(panel.getByTestId('app-source-line')).toContainText('github.com/ucesys/221b/deploy/helm/221b @ main')
  try {
    const list = await request.get('/api/v1/apps')
    const apps = (await list.json()) as { namespace: string; name: string; source: { repo: string; ref: string; refKind: string } }[]
    expect(apps.find((a) => a.name === r.name)?.source).toEqual({ repo: 'https://github.com/ucesys/221b', path: 'deploy/helm/221b', ref: 'main', refKind: 'branch' })
    // The values save by themselves, and show at once.
    await panel.locator('[data-subtab="values"]').click()
    await panel.getByTestId('app-values').fill('replicaCount: 2\n')
    await expect(panel.getByTestId('app-values-state')).toHaveText('Saved', { timeout: 10_000 })
    await page.reload()
    await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
    await page.getByTestId('app-panel').locator('[data-subtab="values"]').click()
    await expect(page.getByTestId('app-panel').getByTestId('app-values')).toHaveValue('replicaCount: 2\n')

    const me = (await (await request.get('/api/v1/me')).json()) as { mode: string }
    if (me.mode !== 'none') {
      const viewer = await signInAs(browser, E2E_VIEWER)
      try {
        await viewer.goto('/#/helm1')
        await expect(viewer.getByTestId('cluster-state')).toHaveText('Connected')
        await expect(viewer.getByTestId('apps-new')).toHaveCount(0)
        await expect(viewer.locator('[data-testid^="app-tools-"]')).toHaveCount(0)
        const refused = await viewer.request.post('/api/v1/apps', { data: { namespace: 'web', name: 'nope', source: { repo: 'https://github.com/ucesys/221b', ref: 'main', refKind: 'branch' } }, headers: SAME })
        expect(refused.status()).toBe(403)
      } finally {
        await viewer.context().close()
      }
    }
  } finally {
    const forget = await request.delete(`/api/v1/apps/${r.namespace}/${r.name}?record=1`, { headers: SAME })
    expect([204, 404]).toContain(forget.status())
  }
})

test('installs a chart from a public git repository through Watson, checks its drift and images, and uninstalls it (HA-04 to HA-09, HA-15)', async ({ page, request }) => {
  test.skip(!inKind, 'needs a real cluster and the internet')
  test.setTimeout(240_000)
  await page.goto('/#/helm1')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  await page.request.put('/api/v1/watson/config', { data: { twoPerson: false }, headers: SAME })
  const ns = 'e2e-apps'
  const name = 'hello'
  await page.getByTestId('apps-new').click()
  const dlg = page.getByTestId('app-dialog')
  await dlg.getByTestId('app-namespace').fill(ns)
  await dlg.getByTestId('app-name').fill(name)
  await dlg.getByTestId('app-repo').fill('https://github.com/helm/examples')
  await dlg.getByTestId('app-path').fill('charts/hello-world')
  await dlg.getByTestId('app-load-refs').click()
  await expect(dlg.getByTestId('app-ref')).toBeVisible({ timeout: 60_000 })
  await dlg.getByTestId('app-ref').selectOption('branch:main')
  await dlg.getByTestId('app-create-ns').check()
  await dlg.getByTestId('app-params').fill('replicaCount=1')
  await dlg.getByTestId('app-submit').click()
  await expect(dlg).toHaveCount(0)
  // The install is a card: read, then approved.
  const card = page.getByTestId('watson').locator('[data-testid=watson-card]').filter({ hasText: `helm install ${name}` })
  await expect(card.getByTestId('watson-approve')).toBeEnabled({ timeout: 90_000 })
  await card.getByTestId('watson-approve').click()
  try {
    const row = page.getByTestId('apps-table').locator(`[data-release="${ns}/${name}"]`)
    await expect(row).toBeVisible({ timeout: 120_000 })
    await expect(row).toContainText('github.com/helm/examples/charts/hello-world @ main')
    await row.click()
    const panel = page.getByTestId('app-panel')
    await expect(panel.getByTestId('app-drift')).toContainText('in sync', { timeout: 60_000 })
    await panel.locator('[data-subtab="images"]').click()
    await expect(panel.getByTestId('app-images')).toContainText('nginx', { timeout: 30_000 })
    const log = JSON.stringify(await cluster.auditLog())
    expect(log).toContain('app.save')
    expect(log).toContain('helm install hello --git https://github.com/helm/examples --path charts/hello-world --ref main -n e2e-apps --app --create-namespace')
  } finally {
    const res = await request.delete(`/api/v1/apps/${ns}/${name}`, { headers: SAME })
    expect([202, 404]).toContain(res.status())
    const gone = page.getByTestId('watson').locator('[data-testid=watson-card]').filter({ hasText: `helm uninstall ${name}` })
    await expect(gone.getByTestId('watson-approve')).toBeEnabled({ timeout: 60_000 })
    await gone.getByTestId('watson-approve').click()
    await expect(page.getByTestId('apps-table').locator(`[data-release="${ns}/${name}"]`)).toHaveCount(0, { timeout: 120_000 })
  }
})

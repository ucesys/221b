// The Helm tab, as P2-06 and P2-07 describe it.
import { expect, test, type Page } from '@playwright/test'
import type { ReleaseDetails } from '../src/api/types'
import { cluster, inKind, PLANTED_HELM, releaseRows, snapshot } from './cluster'

async function open(page: Page, hash = '#/helm') {
  await page.goto('/' + hash)
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  await expect(page.getByTestId('release-name')).toBeVisible()
}

test('matches helm list -A: every release with its chart, app version, revision, status and update time', async ({ page, request }) => {
  await open(page)
  const rows = releaseRows(await snapshot(request, ['helm']))
  const want = await cluster.helmList()
  expect(rows.map((r) => `${r.namespace}/${r.name}`).sort()).toEqual(want.map((r) => `${r.namespace}/${r.name}`).sort())
  for (const w of want) {
    const r = rows.find((x) => x.name === w.name && x.namespace === w.namespace)!
    expect(`${r.chart}-${r.chartVersion}`, `chart of ${w.name}`).toBe(w.chart)
    expect(r.appVersion, `app version of ${w.name}`).toBe(w.app_version)
    expect(r.revision, `revision of ${w.name}`).toBe(w.revision)
    expect(r.status, `status of ${w.name}`).toBe(w.status)
    expect(r.updated, `update time of ${w.name}`).toBeTruthy()
  }
  await expect(page.getByRole('link', { name: /^Helm/ })).toContainText(String(rows.length))
  const list = page.getByTestId('release-list')
  for (const r of rows) {
    const item = list.getByRole('link', { name: new RegExp(`^${r.name}\\b`) })
    await expect(item).toBeVisible()
    await expect(item).toContainText(r.status)
    await expect(item).toContainText(`${r.chart}-${r.chartVersion}`)
    await expect(item).toContainText(`rev ${r.revision}`)
  }
})

test('details show the revision history as helm history lists it, the notes and the user-supplied values', async ({ page, request }) => {
  const rows = releaseRows(await snapshot(request, ['helm']))
  const target = rows.find((r) => r.name === 'shop') ?? rows.find((r) => r.revisions > 1) ?? rows[0]!
  await open(page, `#/helm/${target.namespace}/${target.name}`)
  await expect(page.getByTestId('release-name')).toHaveText(target.name)
  await expect(page.getByTestId('release-facts')).toContainText(`${target.chart}-${target.chartVersion}`)
  await expect(page.getByTestId('release-facts')).toContainText(target.appVersion || '—')

  const res = await request.get(`/api/v1/helm/${target.namespace}/${target.name}`)
  expect(res.ok()).toBe(true)
  const d = (await res.json()) as ReleaseDetails
  const want = await cluster.helmHistory(target.namespace, target.name)
  expect(d.history.map((h) => [h.revision, h.status, `${h.chart}-${h.chartVersion}`])).toEqual([...want].sort((a, b) => b.revision - a.revision).map((h) => [h.revision, h.status, h.chart]))

  await page.locator('[data-subtab="history"]').click()
  await expect(page.locator('[data-revision]')).toHaveCount(want.length)
  await expect(page.locator('[data-revision]').first()).toContainText(String(target.revision))
  if (want.some((h) => h.status === 'failed')) await expect(page.locator('[data-revision]', { hasText: 'failed' })).toBeVisible()

  await page.locator('[data-subtab="values"]').click()
  const values = page.getByTestId('release-values')
  if (inKind) {
    await expect(values).toContainText('clusterName: kind-e2e')
  } else {
    await expect(values).toContainText('replicas: 3')
    // A value that looks like a credential is masked (P2-07).
    await expect(values).toContainText('password: •••••• (hidden by 221B)')
    await expect(values).not.toContainText(PLANTED_HELM)
    expect(JSON.stringify(d)).not.toContain(PLANTED_HELM)
    expect(d.maskedValues).toContain('db.password')
  }

  await page.locator('[data-subtab="notes"]').click()
  if (inKind || d.notes) await expect(page.getByTestId('release-notes')).toContainText(d.notes.split('\n')[0]!)

  // The release's pods, and its resources with their state.
  await page.locator('[data-subtab="pods"]').click()
  await expect(page.locator('[data-pod]')).toHaveCount(d.podRows.length)
  if (d.resources.length) {
    await page.locator('[data-subtab="workloads"]').click()
    await expect(page.locator('[data-resource]').first()).toBeVisible()
    await page.locator('[data-resource]').first().click()
    await expect(page.getByRole('dialog', { name: 'Object YAML' }).getByTestId('yaml')).toContainText('kind:')
  }
})

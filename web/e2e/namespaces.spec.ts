// The Namespaces tab, as P2-02 and P2-08 describe it.
import { expect, test, type Page } from '@playwright/test'
import type { NamespaceRow, ObjectRow } from '../src/api/types'
import { cluster, countKind, inKind, PLANTED, snapshot } from './cluster'

async function open(page: Page, hash = '#/namespaces') {
  await page.goto('/' + hash)
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  await expect(page.getByTestId('namespace-name')).toBeVisible()
}

test('lists every namespace and what it holds, by kind, with counts that match the cluster', async ({ page, request }) => {
  await open(page)
  const namespaces = (await snapshot(request, ['namespaces'])).views.namespaces!.rows as NamespaceRow[]
  expect(namespaces.length).toBeGreaterThan(0)
  await expect(page.getByRole('link', { name: /^Namespaces/ })).toContainText(String(namespaces.length))
  const list = page.getByTestId('namespace-list')
  for (const ns of namespaces) await expect(list.getByRole('link', { name: new RegExp(`^${ns.name}\\b`) })).toBeVisible()

  // The namespace with the most pods, opened from the list.
  const ns = [...namespaces].sort((a, b) => b.pods - a.pods)[0]!
  await list.getByRole('link', { name: new RegExp(`^${ns.name}\\b`) }).click()
  await expect(page).toHaveURL(new RegExp(`#/namespaces/${ns.name}`))
  await expect(page.getByTestId('namespace-name')).toHaveText(ns.name)
  await expect(page.getByTestId('namespace-summary')).toContainText(`${ns.objects} objects of ${ns.kinds} kinds`)

  // Each kind's table says how many there are, and that matches the cluster.
  const objects = (await snapshot(request, [`ns:${ns.name}`])).views[`ns:${ns.name}`]!.rows as ObjectRow[]
  for (const kind of ['pods', 'services', 'configmaps', 'deployments']) {
    const want = await countKind(ns.name, kind)
    const rows = objects.filter((o) => o.kindName === kind)
    expect(rows.length, `${kind} in ${ns.name}`).toBe(want)
    expect(ns.counts[kind] ?? 0, `${kind} count of ${ns.name}`).toBe(want)
    if (want > 0) {
      const card = page.locator(`[data-kind="${rows[0]!.kind}"]`)
      await expect(card.locator('.rk-count')).toHaveText(String(want))
      await expect(card.locator('[data-object]')).toHaveCount(Math.min(want, 8))
      if (want > 8) {
        await card.getByRole('button', { name: `Show all ${want}` }).click()
        // The grid then holds them all; long tables render only the rows in view.
        await expect(card.locator('.rk-grid[data-total]')).toHaveAttribute('data-total', String(want))
        expect(await card.locator('[data-object]').count()).toBeGreaterThanOrEqual(Math.min(want, 40))
      }
    }
  }
})

test('the kinds funnel narrows what is shown, and the search filters by name or detail', async ({ page, request }) => {
  await open(page)
  const ns = await page.getByTestId('namespace-name').textContent()
  const objects = (await snapshot(request, [`ns:${ns}`])).views[`ns:${ns}`]!.rows as ObjectRow[]
  const kinds = [...new Set(objects.map((o) => o.kind))]
  expect(kinds.length).toBeGreaterThan(1)
  await expect(page.locator('section[data-kind]')).toHaveCount(kinds.length)

  const funnel = page.getByTestId('kinds-funnel')
  await expect(funnel).toContainText('All kinds')
  await funnel.click()
  const menu = page.getByRole('group', { name: 'Kinds to show' })
  for (const k of kinds.slice(1)) await menu.getByRole('checkbox', { name: new RegExp(`^${k}s`) }).uncheck()
  await page.keyboard.press('Escape')
  await expect(page.locator('section[data-kind]')).toHaveCount(1)
  await expect(page.locator('section[data-kind]').first()).toHaveAttribute('data-kind', kinds[0]!)
  await expect(funnel).toContainText(`1 of ${kinds.length} kinds`)
  await expect(page).toHaveURL(/kinds=/)
  await funnel.click()
  await menu.getByRole('button', { name: 'All' }).click()
  await page.keyboard.press('Escape')
  await expect(page.locator('section[data-kind]')).toHaveCount(kinds.length)

  const target = objects.find((o) => o.kind === 'Pod') ?? objects[0]!
  await page.getByTestId('object-search').fill(target.name)
  await expect(page.locator(`[data-object="${target.key}"]`)).toBeVisible()
  await expect(page.locator('[data-object]').first()).toBeVisible()
  const shown = await page.locator('[data-object]').evaluateAll((els) => els.map((e) => e.getAttribute('data-object')))
  for (const k of shown) expect(objects.find((o) => o.key === k)!.name.includes(target.name) || objects.find((o) => o.key === k)!.details.includes(target.name)).toBe(true)
})

test('an object opens as YAML, with Secret data replaced by key names and sizes (P2-08)', async ({ page }) => {
  const secret = await cluster.plantedSecret()
  try {
    await open(page, `#/namespaces/${secret.namespace}?kinds=Secret`)
    const row = page.locator(`[data-object="Secret/${secret.name}"]`)
    await expect(row).toBeVisible({ timeout: 15_000 })
    // The table shows keys and sizes, never values.
    await expect(row).toContainText(`token (${PLANTED.length} B)`)
    await row.click()
    const drawer = page.getByTestId('object-drawer')
    await expect(drawer.getByTestId('yaml')).toContainText('kind: Secret')
    await expect(drawer.getByTestId('yaml')).toContainText(`token: <${PLANTED.length} bytes, hidden by 221B>`)
    await expect(drawer.getByRole('region', { name: 'YAML' })).not.toContainText('managedFields')
    const text = await drawer.getByTestId('yaml').textContent()
    expect(text).not.toContain(PLANTED)
    expect(text).not.toContain(Buffer.from(PLANTED).toString('base64'))
    await page.keyboard.press('Escape')
    await expect(drawer).toHaveCount(0)
  } finally {
    await secret.cleanup()
  }
  // Secrets are read by a server flag; the UI says so when they are off.
  if (!inKind) await expect(page.getByRole('link', { name: /^Helm/ })).toBeVisible()
})

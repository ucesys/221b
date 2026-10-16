// The Fleets tab (P4-01): groups of nodes by labels and taints, kept with
// the console's settings.
import type { Page } from '@playwright/test'
import { expect, test } from './test'
import { cluster, consoleNode, healthyWorkers, nodeRows, snapshot } from './cluster'
import { E2E_USER, E2E_VIEWER, signInAs } from './accounts'
import type { Fleet } from '../src/api/types'

async function open(page: Page) {
  await page.goto('/#/fleets')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  await expect(page.getByTestId('fleet-list')).toBeVisible()
}

/** Replaces the fleets through the API, as the editor does. */
async function putFleets(page: Page, fleets: Fleet[]): Promise<number> {
  return page.evaluate(async (body) => {
    const res = await fetch('/api/v1/fleets', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ fleets: body }) })
    return res.status
  }, fleets)
}

const row = (page: Page, id: string) => page.locator(`[data-fleet="${id}"]`)

test.beforeEach(async ({ page }) => {
  await open(page)
  expect(await putFleets(page, [])).toBe(200)
})

test.afterAll(async ({ browser }) => {
  const page = await browser.newPage()
  await open(page)
  await putFleets(page, [])
  await page.close()
})

test('an administrator creates a fleet in the drawer, and its nodes are its members', async ({ page, request }) => {
  const total = nodeRows(await snapshot(request)).length
  const linux = await cluster.countNodes('kubernetes.io/os=linux')
  await page.getByTestId('fleet-new').click()
  const editor = page.getByTestId('fleet-editor')
  await expect(editor).toBeVisible()

  // Nothing to save yet: a name and a requirement are needed.
  await editor.getByTestId('fleet-save').click()
  await expect(editor.getByTestId('fleet-error')).toHaveText('Give the fleet a name.')
  await editor.getByTestId('fleet-name').fill('Linux nodes')
  await editor.getByTestId('fleet-save').click()
  await expect(editor.getByTestId('fleet-error')).toHaveText('Pick at least one label or taint.')

  // A requirement typed wrong says why.
  await editor.getByTestId('fleet-text').fill('=linux')
  await editor.getByTestId('fleet-text-add').click()
  await expect(editor.getByTestId('fleet-text-error')).toBeVisible()
  await editor.getByTestId('fleet-text').fill('kubernetes.io/os=linux')
  await editor.getByTestId('fleet-text-add').click()
  await expect(editor.getByTestId('fleet-reqs')).toContainText('kubernetes.io/os=linux')
  await expect(editor.getByTestId('fleet-preview')).toContainText(String(linux))
  await editor.getByTestId('fleet-save').click()
  await expect(editor).toHaveCount(0)

  const f = page.locator('[data-fleet]').filter({ hasText: 'Linux nodes' })
  await expect(f.getByTestId('fleet-count')).toHaveText(String(linux))
  await expect(page.locator('[data-testid="fleet-list"] h2')).toContainText('1')
  await expect(page.getByRole('link', { name: /^Fleets/ })).toContainText('1')

  // The new fleet is selected, with its members in the matrix.
  await expect(page).toHaveURL(/fleet=linux-nodes/)
  await expect(page.getByTestId('fleet-header')).toContainText('Linux nodes')
  await expect(page.getByTestId('fleet-matrix').locator('[data-node]')).toHaveCount(linux)
  expect(linux).toBeLessThanOrEqual(total)
  // Clicking it again shows every node.
  await f.getByTestId('fleet-pick').click()
  await expect(page).not.toHaveURL(/fleet=/)
  await expect(page.getByTestId('fleet-matrix').locator('[data-node]')).toHaveCount(total)

  // A second fleet may not take its name.
  await page.getByTestId('fleet-new').click()
  await editor.getByTestId('fleet-name').fill('Linux nodes')
  await editor.getByTestId('fleet-text').fill('kubernetes.io/arch=amd64')
  await editor.getByTestId('fleet-text-add').click()
  await editor.getByTestId('fleet-save').click()
  await expect(editor.getByTestId('fleet-error')).toHaveText('Another fleet is already called “Linux nodes”.')
  await page.keyboard.press('Escape')

  // The change is in the audit log.
  await expect
    .poll(async () => (await cluster.auditLog()).some((r) => r.action === 'fleets.update' && String(r.detail ?? '').includes('Linux nodes')), { timeout: 15_000 })
    .toBe(true)

  // Deleting takes a second click.
  await f.getByTestId('fleet-edit').click()
  await editor.getByTestId('fleet-delete').click()
  await expect(editor.getByTestId('fleet-delete')).toHaveText(/Click again/)
  await editor.getByTestId('fleet-delete').click()
  await expect(editor).toHaveCount(0)
  await expect(page.locator('[data-fleet]')).toHaveCount(0)
})

test('a node that gets the labels joins its fleet within 5 seconds', async ({ page, request }) => {
  expect(await putFleets(page, [{ id: 'e2e-fleet', name: 'E2E fleet', color: 2, labels: ['e2e.221b/fleet=on'], taints: [] }])).toBe(200)
  const f = row(page, 'e2e-fleet')
  await expect(f.getByTestId('fleet-count')).toHaveText('0')

  const skip = await consoleNode()
  const worker = healthyWorkers(await snapshot(request)).find((r) => r.name !== skip)!.name
  const n = await cluster.nodeWith(worker, { 'e2e.221b/fleet': 'on' })
  const start = Date.now()
  try {
    await expect(f.getByTestId('fleet-count')).toHaveText('1', { timeout: 5_000 })
    expect(Date.now() - start).toBeLessThan(5_000)
    await f.getByTestId('fleet-pick').click()
    await expect(page.getByTestId('fleet-matrix').locator(`[data-node="${n.node}"]`)).toBeVisible()
  } finally {
    await n.cleanup()
  }
  // And leaves when it loses them.
  await expect(f.getByTestId('fleet-count')).toHaveText('0', { timeout: 5_000 })
})

test('a fleet with a taint takes only the nodes with both, and says what the others lack', async ({ page, request }) => {
  const taint = { key: 'e2e.221b/dedicated', value: 'fleet', effect: 'NoSchedule' }
  const fleet: Fleet = { id: 'e2e-tainted', name: 'E2E tainted', color: 3, labels: ['e2e.221b/tainted=on'], taints: [`${taint.key}=${taint.value}:${taint.effect}`] }
  expect(await putFleets(page, [fleet])).toBe(200)
  const f = row(page, 'e2e-tainted')
  const skip = await consoleNode()
  const worker = healthyWorkers(await snapshot(request)).find((r) => r.name !== skip)!.name
  const n = await cluster.nodeWith(worker, { 'e2e.221b/tainted': 'on' })
  try {
    // The label alone is not enough.
    await page.waitForTimeout(2_000)
    await expect(f.getByTestId('fleet-count')).toHaveText('0')
    // The editor shows it as almost in, with the command that adds the taint.
    await f.getByTestId('fleet-edit').click()
    const near = page.getByTestId('fleet-editor').getByTestId('fleet-near')
    await expect(near).toContainText(n.node)
    await expect(near).toContainText(taint.key)
    await page.keyboard.press('Escape')

    await cluster.taint(n.node, taint, true)
    await expect(f.getByTestId('fleet-count')).toHaveText('1', { timeout: 5_000 })
  } finally {
    await cluster.taint(n.node, taint, false)
    await n.cleanup()
  }
})

test('the fleets cannot be changed by someone who is not an administrator', async ({ browser }) => {
  // A context with no session: the API refuses it before anything else.
  const ctx = await browser.newContext({ storageState: { cookies: [], origins: [] } })
  const page = await ctx.newPage()
  await page.goto('/')
  const status = await page.evaluate(async () => (await fetch('/api/v1/fleets', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: '{"fleets":[]}' })).status)
  expect([401, 403]).toContain(status)
  await ctx.close()
})

test('a saved fleet shows at once; a node opens its details here; the columns can all be picked or cleared', async ({ page, request }) => {
  const worker = healthyWorkers(await snapshot(request))[0]!
  const label = Object.entries(worker.labels).find(([k]) => k === 'kubernetes.io/hostname') ?? Object.entries(worker.labels)[0]!
  const saved = Date.now()
  expect(await putFleets(page, [{ id: 'e2e-now', name: 'E2E now', color: 1, labels: [`${label[0]}=${label[1]}`], taints: [] }])).toBe(200)
  // Not a refresh later (asked at the Phase 4 review).
  await expect(row(page, 'e2e-now')).toBeVisible({ timeout: 2_000 })
  expect(Date.now() - saved).toBeLessThan(2_000)
  await row(page, 'e2e-now').getByTestId('fleet-pick').click()
  await expect(page.getByTestId('fleet-header')).toContainText('E2E now')

  // A node's name opens its details over the Fleets tab.
  await page.getByTestId('fleet-matrix').locator(`[data-node="${worker.name}"] a`).click()
  await expect(page.getByTestId('node-panel')).toContainText(worker.name)
  await expect(page).toHaveURL(/#\/fleets\?.*peek=node/)
  await page.keyboard.press('Escape')
  await expect(page.getByTestId('node-panel')).toHaveCount(0)

  // Columns: clear all, then select all.
  await page.getByRole('button', { name: /^Columns/ }).click()
  await page.getByTestId('fleet-cols-none').click()
  await expect(page.getByRole('button', { name: /^Columns/ })).toContainText(/(^|\D)0\/\d+/)
  await page.getByTestId('fleet-cols-all').click()
  const text = (await page.getByRole('button', { name: /^Columns/ }).textContent()) ?? ''
  const [shown, all] = text.match(/(\d+)\/(\d+)/)!.slice(1)
  expect(shown).toBe(all)
})

test('fleets are the cluster’s, the same for everyone who signs in', async ({ page, browser }) => {
  expect(await putFleets(page, [{ id: 'e2e-shared', name: 'E2E shared', color: 4, labels: ['kubernetes.io/os=linux'], taints: [] }])).toBe(200)
  await expect(row(page, 'e2e-shared')).toBeVisible()
  // Someone else, in another browser, sees the same fleet: it is kept on the server, not in a browser.
  const other = await signInAs(browser, E2E_VIEWER)
  try {
    await other.goto('/#/fleets')
    await expect(other.getByTestId('fleet-list')).toBeVisible()
    // The definition reaches them with the settings, whatever nodes they may list; the tab counts it.
    await expect(other.getByRole('link', { name: /^Fleets/ })).toContainText('1')
    const shared = await other.evaluate(async () => {
      const snap = (await (await fetch('/api/v1/snapshot?views=nodes')).json()) as { settings?: { fleets?: Record<string, { id: string }[]> } }
      return (snap.settings?.fleets?.local ?? []).map((f) => f.id)
    })
    expect(shared).toEqual(['e2e-shared'])
    expect(await other.evaluate(() => Object.keys(localStorage).filter((k) => localStorage.getItem(k)?.includes('e2e-shared')))).toEqual([])
  } finally {
    await other.context().close()
  }
})

test('column choices stay in this browser, signed in or not (Phase 6 review)', async ({ page, browser }) => {
  await page.goto('/#/fleets')
  await expect(page.getByTestId('fleet-list')).toBeVisible()
  await page.getByRole('button', { name: /^Columns/ }).click()
  await page.getByTestId('fleet-cols-none').click()
  await expect(page.getByRole('button', { name: /^Columns/ })).toContainText(/(^|\D)0\/\d+/)
  // Kept here, across a reload.
  await page.reload()
  await expect(page.getByTestId('fleet-list')).toBeVisible()
  await expect(page.getByRole('button', { name: /^Columns/ })).toContainText(/(^|\D)0\/\d+/)
  // Another browser, even signed in as the same person, starts from the defaults.
  const same = await signInAs(browser, E2E_USER)
  try {
    await same.goto('/#/fleets', { waitUntil: 'domcontentloaded' })
    await expect(same.getByTestId('fleet-list')).toBeVisible()
    await expect(same.getByRole('button', { name: /^Columns/ })).not.toContainText(/(^|\D)0\/\d+/)
    // And the server keeps no preferences.
    expect((await same.request.get('/api/v1/prefs')).status()).toBe(404)
  } finally {
    await same.context().close()
  }
})

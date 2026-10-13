// The Nodes tab, as P1-13 to P1-17 describe it.
import { readFileSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'
import { compareNatural, fCores, fMem, pct } from '../src/lib/format'
import { DEFAULT_HIDDEN, exportJSON, exportSheet, filterRows, free, visibleColumns, type ColumnId } from '../src/lib/nodes'
import { inKind, kubectl, snapshot } from './cluster'
import { readSheet } from './xlsx'

const shownNames = (page: Page) => page.locator('[data-node]').evaluateAll((els) => els.map((e) => e.getAttribute('data-node') ?? ''))

async function open(page: Page) {
  await page.goto('/')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
}

test.beforeEach(async ({ page }) => {
  await open(page)
})

test('shows the cluster and every node', async ({ page, request }) => {
  const snap = await snapshot(request)
  await expect(page.getByTestId('cluster')).toContainText(snap.cluster!.name)
  await expect(page.getByTestId('cluster-version')).toHaveText(snap.cluster!.version)
  if (inKind) {
    const v = JSON.parse(await kubectl('version', '-o', 'json')) as { serverVersion: { gitVersion: string } }
    expect(snap.cluster!.version).toBe(v.serverVersion.gitVersion)
  }
  await expect(page.locator('[data-node]')).toHaveCount(snap.rows.length)
  const cp = snap.rows.filter((r) => r.role === 'control-plane').length
  await expect(page.getByRole('button', { name: /^Nodes/ })).toContainText(String(snap.rows.length))
  const roles = page.getByRole('group', { name: 'Show nodes by role' })
  await expect(roles.getByRole('button', { name: /^All/ })).toContainText(String(snap.rows.length))
  await expect(roles.getByRole('button', { name: /Control plane/ })).toContainText(String(cp))
  await expect(roles.getByRole('button', { name: /Workers/ })).toContainText(String(snap.rows.length - cp))
  // No summary line and no capacity cards: the home page will have them.
  await expect(page.getByRole('heading', { level: 1 })).toHaveCount(0)
  await expect(page.getByTestId('shown')).toHaveText('')
})

test('shows the role, allocatable and free resources of every node', async ({ page, request }) => {
  const snap = await snapshot(request)
  for (const r of snap.rows.slice(0, 3)) {
    const cells = page.locator(`[data-node="${r.name}"] > span`)
    await expect(cells.nth(1)).toHaveText(r.role === 'control-plane' ? 'cp' : 'worker')
    await expect(cells.nth(4)).toContainText(fCores(r.cpu.requested))
    await expect(cells.nth(5)).toHaveText(fCores(r.cpu.allocatable))
    await expect(cells.nth(6)).toHaveText(fCores(free(r.cpu)))
    await expect(cells.nth(7)).toContainText(fMem(r.memory.requested))
    await expect(cells.nth(8)).toHaveText(fMem(r.memory.allocatable))
    await expect(cells.nth(9)).toHaveText(fMem(free(r.memory)))
    await expect(cells.nth(10)).toHaveText(`${r.pods.running} / ${r.pods.capacity}`)
  }
  // The role is not repeated in the node column.
  await expect(page.locator('[data-node] > span').first()).not.toContainText('worker')
})

test('sorts by any column', async ({ page, request }) => {
  const snap = await snapshot(request)
  const byName = new Map(snap.rows.map((r) => [r.name, r]))

  const name = page.locator('.rk-gh [data-col="name"]')
  await expect(name).toHaveAttribute('aria-label', /sorted ascending/)
  expect(await shownNames(page)).toEqual(snap.rows.map((r) => r.name).sort(compareNatural))
  await name.click()
  await expect(name).toHaveAttribute('aria-label', /sorted descending/)
  expect(await shownNames(page)).toEqual(snap.rows.map((r) => r.name).sort(compareNatural).reverse())

  await page.locator('.rk-gh [data-col="cpu"]').click()
  await expect(page.locator('.rk-gh [data-col="cpu"]')).toHaveAttribute('aria-label', /sorted descending/)
  const shares = (await shownNames(page)).map((n) => pct(byName.get(n)!.cpu.requested, byName.get(n)!.cpu.allocatable))
  expect(shares).toEqual([...shares].sort((a, b) => b - a))

  await page.locator('.rk-gh [data-col="cpuFree"]').click()
  const frees = (await shownNames(page)).map((n) => free(byName.get(n)!.cpu))
  expect(frees).toEqual([...frees].sort((a, b) => b - a))

  await page.locator('.rk-gh [data-col="role"]').click()
  expect(byName.get((await shownNames(page))[0]!)!.role).toBe('control-plane')

  for (const col of ['status', 'problems', 'cpuAlloc', 'memory', 'memAlloc', 'memFree', 'pods', 'age']) {
    const header = page.locator(`.rk-gh [data-col="${col}"]`)
    await header.click()
    await expect(header).toHaveAttribute('aria-label', /sorted/)
    await expect(page.locator('[data-node]')).toHaveCount(snap.rows.length)
  }
})

test('filters by role and by search', async ({ page, request }) => {
  const snap = await snapshot(request)
  const cp = snap.rows.filter((r) => r.role === 'control-plane')
  await page.getByRole('group', { name: 'Show nodes by role' }).getByRole('button', { name: /Control plane/ }).click()
  expect(await shownNames(page)).toEqual(cp.map((r) => r.name).sort(compareNatural))
  await expect(page.getByTestId('shown')).toHaveText(`Showing ${cp.length} of ${snap.rows.length} nodes`)
  await page.getByRole('group', { name: 'Show nodes by role' }).getByRole('button', { name: /Workers/ }).click()
  await expect(page.locator('[data-node]')).toHaveCount(snap.rows.length - cp.length)
  await page.getByRole('group', { name: 'Show nodes by role' }).getByRole('button', { name: /^All/ }).click()

  const target = snap.rows.find((r) => r.role === 'worker')!.name
  await page.getByPlaceholder('Filter by name, label or taint').fill(target)
  const expected = filterRows(snap.rows, 'all', target).map((r) => r.name).sort(compareNatural)
  expect(await shownNames(page)).toEqual(expected)
  await expect(page.getByTestId('shown')).toHaveText(`Showing ${expected.length} of ${snap.rows.length} nodes`)

  await page.getByPlaceholder('Filter by name, label or taint').fill('no-node-has-this-name')
  await expect(page.getByText('No nodes match this filter.')).toBeVisible()
})

test('hides taints, zone, labels and kubelet until picked, and remembers the choice', async ({ page }) => {
  for (const col of DEFAULT_HIDDEN) await expect(page.locator(`.rk-gh [data-col="${col}"]`)).toHaveCount(0)
  const menuButton = page.getByRole('button', { name: /^Columns/ })
  await expect(menuButton).not.toContainText('/')
  await menuButton.click()
  await page.getByRole('checkbox', { name: 'Zone' }).check()
  await page.getByRole('checkbox', { name: 'Labels' }).check()
  await page.getByRole('checkbox', { name: 'Pods' }).uncheck()
  await expect(page.locator('.rk-gh [data-col="zone"]')).toBeVisible()
  await expect(page.locator('.rk-gh [data-col="labels"]')).toBeVisible()
  await expect(page.locator('.rk-gh [data-col="pods"]')).toHaveCount(0)
  await page.keyboard.press('Escape')
  await expect(page.getByRole('group', { name: 'Columns to show' })).toHaveCount(0)
  await expect(menuButton).toBeFocused()
  await expect(menuButton).toContainText('13/16')

  await page.reload()
  await expect(page.locator('.rk-gh [data-col="name"]')).toBeVisible()
  await expect(page.locator('.rk-gh [data-col="zone"]')).toBeVisible()
  await expect(page.locator('.rk-gh [data-col="pods"]')).toHaveCount(0)
  await expect(page.locator('.rk-gh [data-col="kubelet"]')).toHaveCount(0)

  await page.getByRole('button', { name: /^Columns/ }).click()
  await page.getByRole('button', { name: 'Reset to default' }).click()
  await page.getByRole('button', { name: 'Done', exact: true }).click()
  await expect(page.locator('.rk-gh [data-col="zone"]')).toHaveCount(0)
  await expect(page.locator('.rk-gh [data-col="pods"]')).toBeVisible()
  await expect(menuButton).not.toContainText('/')
})

test('exports what is on screen to Excel', async ({ page, request }) => {
  const snap = await snapshot(request)
  // A filter, a sort and a hidden column, so the sheet has to follow all three.
  await page.getByRole('group', { name: 'Show nodes by role' }).getByRole('button', { name: /Workers/ }).click()
  await page.locator('.rk-gh [data-col="memory"]').click()
  await page.getByRole('button', { name: /^Columns/ }).click()
  await page.getByRole('checkbox', { name: 'Pods' }).uncheck()
  await page.keyboard.press('Escape')

  const names = await shownNames(page)
  await page.getByRole('button', { name: /^Export/ }).click()
  const [download] = await Promise.all([page.waitForEvent('download'), page.getByRole('button', { name: 'Excel' }).click()])
  expect(download.suggestedFilename()).toMatch(new RegExp(`^${snap.cluster!.name.toLowerCase().replace(/[^a-z0-9]+/g, '-')}-nodes-\\d{8}-\\d{4}\\.xlsx$`))
  const sheet = readSheet(readFileSync(await download.path()))

  const byName = new Map(snap.rows.map((r) => [r.name, r]))
  const expected = exportSheet(
    names.map((n) => byName.get(n)!),
    visibleColumns(new Set<ColumnId>([...DEFAULT_HIDDEN, 'pods']), false),
  )
  expect(sheet[0]).toEqual(expected.head)
  expect(sheet.slice(1).map((r) => r[0])).toEqual(names)
  expect(sheet.length - 1).toBe(names.length)
  // Same values, apart from usage, which moves between reads.
  const usage = new Set(expected.head.flatMap((h, i) => (/in use/.test(h) ? [i] : [])))
  const strip = (rows: unknown[][]) => rows.map((r) => r.map((v, i) => (usage.has(i) ? 0 : v ?? null)))
  expect(strip(sheet.slice(1))).toEqual(strip(expected.rows.map((r) => r.map((v) => (v === '' ? null : v)))))
})

test('exports what is on screen as JSON', async ({ page, request }) => {
  const snap = await snapshot(request)
  await page.locator('.rk-gh [data-col="name"]').click() // descending
  await page.getByRole('button', { name: /^Columns/ }).click()
  await page.getByRole('checkbox', { name: 'Taints' }).check()
  await page.keyboard.press('Escape')
  const names = await shownNames(page)

  await page.getByRole('button', { name: /^Export/ }).click()
  const [download] = await Promise.all([page.waitForEvent('download'), page.getByRole('button', { name: 'JSON' }).click()])
  expect(download.suggestedFilename()).toMatch(/-nodes-\d{8}-\d{4}\.json$/)
  const got = JSON.parse(readFileSync(await download.path(), 'utf8')) as Record<string, unknown>[]

  const byName = new Map(snap.rows.map((r) => [r.name, r]))
  const hidden = new Set<ColumnId>(DEFAULT_HIDDEN)
  hidden.delete('taints')
  const expected = exportJSON(
    names.map((n) => byName.get(n)!),
    visibleColumns(hidden, false),
  )
  expect(got.map((r) => r['Node'])).toEqual(names)
  expect(Object.keys(got[0]!)).toEqual(Object.keys(expected[0]!))
  expect(got[0]!['Taints']).toEqual(expected[0]!['Taints'])
  const strip = (rows: Record<string, unknown>[]) => rows.map((r) => Object.fromEntries(Object.entries(r).filter(([k]) => !/in use/.test(k))))
  expect(strip(got)).toEqual(strip(expected))
})

test('opens node details with Enter and closes them with Escape', async ({ page, request }) => {
  const snap = await snapshot(request)
  const target = snap.rows.find((r) => r.role === 'worker')!
  const row = page.locator(`[data-node="${target.name}"]`)
  await row.focus()
  await page.keyboard.press('Enter')

  const panel = page.getByTestId('node-panel')
  await expect(panel).toBeVisible()
  await expect(panel.getByRole('heading', { level: 2 })).toHaveText(target.name)
  await expect(page.getByRole('button', { name: 'Close node details' })).toBeFocused()
  await expect(row).toHaveAttribute('aria-pressed', 'true')

  // Status first, then taints and labels, then the resources, then the system.
  const headings = await panel.locator('h3').allTextContents()
  expect(headings.filter((h) => h !== 'Problems')).toEqual(['Status', 'Taints', 'Labels', 'CPU', 'Memory', 'Pods', 'System'])
  await expect(panel.getByTestId('node-status')).toContainText(target.status)
  await expect(panel.getByTestId('node-status')).toContainText('worker')
  await expect(panel.getByRole('region', { name: 'Taints' })).toBeVisible()
  for (const k of ['Uptime', 'Internal IP', 'Zone', 'OS', 'Kernel', 'Kubelet', 'Runtime', 'Architecture', 'Joined']) {
    await expect(panel.locator('dt', { hasText: new RegExp(`^${k}$`) })).toBeVisible()
  }
  await expect(panel.locator('dt', { hasText: /^Internal IP$/ }).locator('+ dd')).toHaveText(target.internalIP)
  await expect(panel).toContainText(target.kubelet)
  // Resources as plain sections, not tiles.
  const cpu = panel.getByTestId('resource-cpu')
  await expect(cpu.locator('dt')).toHaveText(['Allocatable', 'Requested', 'Free to schedule', 'In use', 'Limits'])
  await expect(cpu.locator('dt', { hasText: 'Free to schedule' }).locator('+ dd')).toContainText(fCores(free(target.cpu)))
  await expect(panel.getByTestId('resource-memory').locator('dt', { hasText: 'Allocatable' }).locator('+ dd')).toContainText(fMem(target.memory.allocatable))
  await expect(panel.getByTestId('resource-pods').locator('dt', { hasText: 'Capacity' }).locator('+ dd')).toContainText(String(target.pods.capacity))
  await expect(panel.locator('.rk-card')).toHaveCount(0)
  // Uptime from the kubelet, when the server may ask it.
  if (snap.cluster!.uptime === 'available') {
    await expect(panel.locator('dt', { hasText: /^Uptime$/ }).locator('+ dd')).toContainText(/booted \d{4}-\d{2}-\d{2}/, { timeout: 40_000 })
  }
  // kubernetes.io labels are hidden until asked for.
  const system = Object.keys(target.labels).filter((k) => /^(beta\.kubernetes\.io|kubernetes\.io|node\.kubernetes\.io)\//.test(k))
  if (system.length) {
    await expect(panel.getByText(system[0]!, { exact: true })).toHaveCount(0)
    await panel.getByRole('button', { name: `Show ${system.length} kubernetes.io labels` }).click()
    await expect(panel.locator('.rk-chip', { hasText: system[0]! }).first()).toBeVisible()
  }
  // Wide-only columns step aside while the panel is open.
  await expect(page.locator('.rk-gh [data-col="cpuAlloc"]')).toHaveCount(0)

  await page.keyboard.press('Escape')
  await expect(panel).toHaveCount(0)
  await expect(row).toBeFocused()
  await expect(page.locator('.rk-gh [data-col="cpuAlloc"]')).toBeVisible()
})

test('sends security headers', async ({ request }) => {
  const res = await request.get('/')
  const h = res.headers()
  expect(h['content-security-policy']).toContain("default-src 'self'")
  expect(h['content-security-policy']).toContain("frame-ancestors 'none'")
  expect(h['x-content-type-options']).toBe('nosniff')
  expect(h['x-frame-options']).toBe('DENY')
  expect(h['referrer-policy']).toBe('no-referrer')
})

test.describe('on a phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true })

  test('fits 390 px without scrolling sideways', async ({ page }) => {
    const overflow = () => page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
    await expect(page.locator('[data-node]').first()).toBeVisible()
    expect(await overflow()).toBeLessThanOrEqual(0)
    await page.locator('[data-node]').first().click()
    await expect(page.getByTestId('node-panel')).toBeVisible()
    expect(await overflow()).toBeLessThanOrEqual(0)
    const box = await page.getByTestId('node-panel').boundingBox()
    expect(box!.x).toBeGreaterThanOrEqual(0)
    expect(box!.x + box!.width).toBeLessThanOrEqual(390.5)
    await page.keyboard.press('Escape')
    await page.getByRole('button', { name: 'Settings' }).click()
    await expect(page.getByRole('dialog')).toBeVisible()
    expect(await overflow()).toBeLessThanOrEqual(0)
  })
})

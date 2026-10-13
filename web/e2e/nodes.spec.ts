// The Nodes tab, as P1-13 to P1-17 describe it.
import { readFileSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'
import { compareNatural, pct } from '../src/lib/format'
import { exportSheet, filterRows, visibleColumns, type ColumnId } from '../src/lib/nodes'
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
  const pods = snap.rows.reduce((s, r) => s + r.pods.running, 0)
  await expect(page.getByTestId('summary')).toContainText(`${cp} control-plane node${cp === 1 ? '' : 's'} · ${snap.rows.length - cp} worker`)
  await expect(page.getByTestId('summary')).toContainText(`${pods} pods`)
  await expect(page.getByRole('button', { name: /^Nodes/ })).toContainText(String(snap.rows.length))
})

test('totals equal the sums over the table', async ({ page, request }) => {
  const snap = await snapshot(request)
  const cpu = snap.rows.reduce((s, r) => s + r.cpu.allocatable, 0) / 1000
  const pods = snap.rows.reduce((s, r) => s + r.pods.capacity, 0)
  await expect(page.getByTestId('card-cpu')).toContainText(`${Number(cpu.toFixed(cpu < 10 ? 2 : 1))} cores allocatable`)
  await expect(page.getByTestId('card-pods')).toContainText(`${pods} max`)
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

  for (const col of ['status', 'problems', 'memory', 'pods', 'taints', 'zone', 'kubelet', 'age']) {
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
  await page.getByRole('group', { name: 'Show nodes by role' }).getByRole('button', { name: /Workers/ }).click()
  await expect(page.locator('[data-node]')).toHaveCount(snap.rows.length - cp.length)
  await page.getByRole('group', { name: 'Show nodes by role' }).getByRole('button', { name: /^All/ }).click()

  const target = snap.rows.find((r) => r.role === 'worker')!.name
  await page.getByPlaceholder('Filter by name, label or taint').fill(target)
  const expected = filterRows(snap.rows, 'all', target).map((r) => r.name).sort(compareNatural)
  expect(await shownNames(page)).toEqual(expected)
  await expect(page.getByTestId('summary')).toContainText(`Showing ${expected.length} of ${snap.rows.length} nodes`)

  await page.getByPlaceholder('Filter by name, label or taint').fill('no-node-has-this-name')
  await expect(page.getByText('No nodes match this filter.')).toBeVisible()
})

test('chooses columns and remembers the choice', async ({ page }) => {
  const menuButton = page.getByRole('button', { name: /^Columns/ })
  await menuButton.click()
  await page.getByRole('checkbox', { name: 'Zone' }).uncheck()
  await page.getByRole('checkbox', { name: 'Kubelet' }).uncheck()
  await expect(page.locator('.rk-gh [data-col="zone"]')).toHaveCount(0)
  await page.keyboard.press('Escape')
  await expect(page.getByRole('group', { name: 'Columns to show' })).toHaveCount(0)
  await expect(menuButton).toBeFocused()
  await expect(menuButton).toContainText('8/10')

  await page.reload()
  await expect(page.locator('.rk-gh [data-col="name"]')).toBeVisible()
  await expect(page.locator('.rk-gh [data-col="zone"]')).toHaveCount(0)
  await expect(page.locator('.rk-gh [data-col="kubelet"]')).toHaveCount(0)

  await page.getByRole('button', { name: /^Columns/ }).click()
  await page.getByRole('button', { name: 'Reset to default' }).click()
  await page.getByRole('button', { name: 'Done', exact: true }).click()
  await expect(page.locator('.rk-gh [data-col="zone"]')).toBeVisible()
})

test('exports what is on screen to Excel', async ({ page, request }) => {
  const snap = await snapshot(request)
  // A filter, a sort and a hidden column, so the sheet has to follow all three.
  await page.getByRole('group', { name: 'Show nodes by role' }).getByRole('button', { name: /Workers/ }).click()
  await page.locator('.rk-gh [data-col="memory"]').click()
  await page.getByRole('button', { name: /^Columns/ }).click()
  await page.getByRole('checkbox', { name: 'Taints' }).uncheck()
  await page.keyboard.press('Escape')

  const names = await shownNames(page)
  const [download] = await Promise.all([page.waitForEvent('download'), page.getByRole('button', { name: 'Excel' }).click()])
  expect(download.suggestedFilename()).toMatch(new RegExp(`^${snap.cluster!.name.toLowerCase().replace(/[^a-z0-9]+/g, '-')}-nodes-\\d{8}-\\d{4}\\.xlsx$`))
  const sheet = readSheet(readFileSync(await download.path()))

  const byName = new Map(snap.rows.map((r) => [r.name, r]))
  const expected = exportSheet(
    names.map((n) => byName.get(n)!),
    visibleColumns(new Set<ColumnId>(['taints']), false),
  )
  expect(sheet[0]).toEqual(expected.head)
  expect(sheet.slice(1).map((r) => r[0])).toEqual(names)
  expect(sheet.length - 1).toBe(names.length)
  // Same values, apart from usage, which moves between reads.
  const usage = new Set(expected.head.flatMap((h, i) => (/in use/.test(h) ? [i] : [])))
  const strip = (rows: unknown[][]) => rows.map((r) => r.map((v, i) => (usage.has(i) ? 0 : v ?? null)))
  expect(strip(sheet.slice(1))).toEqual(strip(expected.rows.map((r) => r.map((v) => (v === '' ? null : v)))))
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
  for (const k of ['Internal IP', 'Zone', 'OS', 'Kernel', 'Kubelet', 'Runtime', 'Architecture', 'Joined']) {
    await expect(panel.locator('dt', { hasText: new RegExp(`^${k}$`) })).toBeVisible()
  }
  await expect(panel.locator('dd').first()).toHaveText(target.internalIP)
  await expect(panel).toContainText(target.kubelet)
  await expect(panel.getByRole('heading', { name: 'Taints' })).toBeVisible()
  await expect(panel.getByTestId('card-cpu')).toBeVisible()
  await expect(panel.getByTestId('card-memory')).toBeVisible()
  await expect(panel.getByTestId('card-pods')).toBeVisible()
  // kubernetes.io labels are hidden until asked for.
  const system = Object.keys(target.labels).filter((k) => /^(beta\.kubernetes\.io|kubernetes\.io|node\.kubernetes\.io)\//.test(k))
  if (system.length) {
    await expect(panel.getByText(system[0]!, { exact: true })).toHaveCount(0)
    await panel.getByRole('button', { name: `Show ${system.length} kubernetes.io labels` }).click()
    await expect(panel.locator('.rk-chip', { hasText: system[0]! }).first()).toBeVisible()
  }
  // Wide-only columns step aside while the panel is open.
  await expect(page.locator('.rk-gh [data-col="zone"]')).toHaveCount(0)

  await page.keyboard.press('Escape')
  await expect(panel).toHaveCount(0)
  await expect(row).toBeFocused()
  await expect(page.locator('.rk-gh [data-col="zone"]')).toBeVisible()
})

test('switches theme and remembers it', async ({ page }) => {
  const root = page.locator('.rk')
  const before = (await root.getAttribute('data-theme')) ?? 'light'
  const after = before === 'dark' ? 'light' : 'dark'
  await page.getByRole('button', { name: `Switch to the ${after} theme` }).click()
  await expect(root).toHaveAttribute('data-theme', after)
  await page.reload()
  await expect(page.locator('.rk')).toHaveAttribute('data-theme', after)
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
  })
})

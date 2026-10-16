// The Nodes tab, as P1-13 to P1-17 describe it.
import { readFileSync } from 'node:fs'
import type { Page } from '@playwright/test'
import { expect, test } from './test'
import { NODE_COLUMNS, nodeTerms } from '../src/columns/nodes'
import { compareNatural, fCores, fMem, pct } from '../src/lib/format'
import { free } from '../src/lib/nodes'
import { defaultHidden, exportTable, matchesQuery, queryWords, visibleColumns } from '../src/lib/table'
import { inKind, kubectl, kubectlYAML, nodeRows, snapshot, stableYAML } from './cluster'
import { readSheet } from './xlsx'

const DEFAULT_HIDDEN = defaultHidden(NODE_COLUMNS)

const shownNames = (page: Page) => page.locator('[data-node]').evaluateAll((els) => els.map((e) => e.getAttribute('data-node') ?? ''))

async function open(page: Page) {
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
}

test.beforeEach(async ({ page }) => {
  await open(page)
})

test('shows the cluster and every node', async ({ page, request }) => {
  const snap = await snapshot(request)
  const rows = nodeRows(snap)
  await expect(page.getByTestId('cluster')).toContainText(snap.cluster!.name)
  await expect(page.getByTestId('cluster-version')).toHaveText(snap.cluster!.version)
  if (inKind) {
    const v = JSON.parse(await kubectl('version', '-o', 'json')) as { serverVersion: { gitVersion: string } }
    expect(snap.cluster!.version).toBe(v.serverVersion.gitVersion)
  }
  await expect(page.locator('[data-node]')).toHaveCount(rows.length)
  const cp = rows.filter((r) => r.role === 'control-plane').length
  await expect(page.getByRole('link', { name: /^Nodes/ })).toContainText(String(rows.length))
  const roles = page.getByRole('group', { name: 'Show nodes by role' })
  await expect(roles.getByRole('button', { name: /^All/ })).toContainText(String(rows.length))
  await expect(roles.getByRole('button', { name: /Control plane/ })).toContainText(String(cp))
  await expect(roles.getByRole('button', { name: /Workers/ })).toContainText(String(rows.length - cp))
  // No summary line and no capacity cards: the home page will have them.
  await expect(page.getByRole('heading', { level: 1 })).toHaveCount(0)
  await expect(page.getByTestId('shown')).toHaveText('')
})

test('shows the role, allocatable and free resources of every node', async ({ page, request }) => {
  const snap = await snapshot(request)
  for (const r of nodeRows(snap).slice(0, 3)) {
    const cells = page.locator(`[data-node="${r.name}"] > span`)
    await expect(cells.nth(1)).toHaveText(r.role === 'control-plane' ? 'cp' : 'worker')
    await expect(cells.nth(4)).toContainText(fCores(r.cpu.requested))
    await expect(cells.nth(5)).toHaveText(fCores(r.cpu.allocatable))
    await expect(cells.nth(6)).toHaveText(fCores(free(r.cpu)))
    await expect(cells.nth(7)).toContainText(fMem(r.memory.requested))
    await expect(cells.nth(8)).toHaveText(fMem(r.memory.allocatable))
    await expect(cells.nth(9)).toHaveText(fMem(free(r.memory)))
    // Pods come and go on a real cluster: the figure is the one the server has now.
    await expect
      .poll(async () => {
        const now = nodeRows(await snapshot(request)).find((n) => n.name === r.name)!
        return (await cells.nth(10).textContent()) === `${now.pods.running} / ${now.pods.capacity}`
      })
      .toBe(true)
  }
  // The role is not repeated in the node column: it holds the name alone.
  const first = (await shownNames(page))[0]!
  await expect(page.locator(`[data-node="${first}"] > span`).first()).toHaveText(first)
})

test('sorts by any column', async ({ page, request }) => {
  const rows = nodeRows(await snapshot(request))
  const byName = new Map(rows.map((r) => [r.name, r]))

  // Nodes with problems come first until someone sorts: errors, then
  // warnings, then by name (asked at the Phase 6 review).
  const problems = page.locator('.rk-gh [data-col="problems"]')
  await expect(problems).toHaveAttribute('aria-label', /sorted descending/)
  await expect
    .poll(async () => {
      const latest = new Map(nodeRows(await snapshot(request)).map((r) => [r.name, r]))
      const shown = await shownNames(page)
      const want = [...shown].sort((a, b) => latest.get(b)!.errors * 10 + latest.get(b)!.warnings - (latest.get(a)!.errors * 10 + latest.get(a)!.warnings) || compareNatural(a, b))
      return shown.join(' ') === want.join(' ')
    })
    .toBe(true)
  const name = page.locator('.rk-gh [data-col="name"]')
  await name.click()
  await expect(name).toHaveAttribute('aria-label', /sorted ascending/)
  expect(await shownNames(page)).toEqual(rows.map((r) => r.name).sort(compareNatural))
  await name.click()
  await expect(name).toHaveAttribute('aria-label', /sorted descending/)
  expect(await shownNames(page)).toEqual(rows.map((r) => r.name).sort(compareNatural).reverse())

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
    await expect(page.locator('[data-node]')).toHaveCount(rows.length)
  }
})

test('filters by role and by search, which suggests as you type', async ({ page, request }) => {
  const rows = nodeRows(await snapshot(request))
  const cp = rows.filter((r) => r.role === 'control-plane')
  await page.getByRole('group', { name: 'Show nodes by role' }).getByRole('button', { name: /Control plane/ }).click()
  // Problems first, then by name: compare the sets.
  expect((await shownNames(page)).sort(compareNatural)).toEqual(cp.map((r) => r.name).sort(compareNatural))
  await expect(page.getByTestId('shown')).toHaveText(`Showing ${cp.length} of ${rows.length} nodes`)
  await expect(page).toHaveURL(/role=control-plane/)
  await page.getByRole('group', { name: 'Show nodes by role' }).getByRole('button', { name: /Workers/ }).click()
  await expect(page.locator('[data-node]')).toHaveCount(rows.length - cp.length)
  await page.getByRole('group', { name: 'Show nodes by role' }).getByRole('button', { name: /^All/ }).click()

  const target = rows.find((r) => r.role === 'worker')!.name
  const search = page.getByTestId('node-search')
  await search.fill(target)
  const expected = rows.filter((r) => matchesQuery(nodeTerms(r), queryWords(target))).map((r) => r.name).sort(compareNatural)
  expect((await shownNames(page)).sort(compareNatural)).toEqual(expected)
  await expect(page.getByTestId('shown')).toHaveText(`Showing ${expected.length} of ${rows.length} nodes`)

  // Suggestions complete what the nodes carry: a taint, picked by keyboard.
  const taint = rows.flatMap((r) => r.taints)[0]
  if (taint) {
    await search.fill(taint.key.slice(0, 6))
    const list = page.getByRole('listbox', { name: 'Suggestions' })
    await expect(list).toBeVisible()
    await expect(list.getByRole('option').first()).toContainText(taint.key.slice(0, 6))
    await search.press('ArrowDown')
    await search.press('Enter')
    const picked = (await search.inputValue()).trim()
    expect(picked.length).toBeGreaterThan(6)
    await expect(list).toHaveCount(0)
    const expectedPicked = rows.filter((r) => matchesQuery(nodeTerms(r), queryWords(picked))).map((r) => r.name).sort(compareNatural)
    expect((await shownNames(page)).sort(compareNatural)).toEqual(expectedPicked)
  }

  await search.fill('no-node-has-this-name')
  await expect(page.getByText('No nodes match this filter.')).toBeVisible()
})

test('narrows the nodes by label and taint, and keeps that in the link (P2-09)', async ({ page, request }) => {
  const rows = nodeRows(await snapshot(request))
  const taint = rows.flatMap((r) => r.taints.map((t) => ({ node: r.name, id: `${t.key}${t.value ? '=' + t.value : ''}:${t.effect}` })))[0]
  test.skip(!taint, 'no tainted node to pick')
  await page.getByRole('button', { name: /Select nodes by label and taint/ }).click()
  const picker = page.getByRole('group', { name: 'Select nodes by label and taint' })
  await expect(picker).toBeVisible()
  await picker.locator(`[data-taint="${taint!.id}"]`).click()
  await expect(picker.locator(`[data-taint="${taint!.id}"]`)).toHaveAttribute('aria-pressed', 'true')
  const expected = rows.filter((r) => r.taints.some((t) => `${t.key}${t.value ? '=' + t.value : ''}:${t.effect}` === taint!.id)).map((r) => r.name).sort(compareNatural)
  await expect(picker.getByTestId('facet-match')).toHaveText(`${expected.length} of ${rows.length} nodes match`)
  await page.getByRole('button', { name: 'Done', exact: true }).click()
  expect((await shownNames(page)).sort(compareNatural)).toEqual(expected)
  await expect(page).toHaveURL(new RegExp(`taint=${encodeURIComponent(taint!.id).replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}`))
  // The chip removes the taint again.
  await page.getByTestId('facet-chips').getByRole('button', { name: `Remove ${taint!.id}` }).click()
  await expect(page.locator('[data-node]')).toHaveCount(rows.length)
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
  const rows = nodeRows(snap)
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

  const byName = new Map(rows.map((r) => [r.name, r]))
  const expected = exportTable(
    names.map((n) => byName.get(n)!),
    visibleColumns(NODE_COLUMNS, new Set([...DEFAULT_HIDDEN, 'pods']), false),
    Date.now(),
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
  const rows = nodeRows(await snapshot(request))
  await page.locator('.rk-gh [data-col="name"]').click() // descending
  await page.getByRole('button', { name: /^Columns/ }).click()
  await page.getByRole('checkbox', { name: 'Taints' }).check()
  await page.keyboard.press('Escape')
  const names = await shownNames(page)

  await page.getByRole('button', { name: /^Export/ }).click()
  const [download] = await Promise.all([page.waitForEvent('download'), page.getByRole('button', { name: 'JSON' }).click()])
  expect(download.suggestedFilename()).toMatch(/-nodes-\d{8}-\d{4}\.json$/)
  const got = JSON.parse(readFileSync(await download.path(), 'utf8')) as Record<string, unknown>[]

  const byName = new Map(rows.map((r) => [r.name, r]))
  const hidden = new Set(DEFAULT_HIDDEN)
  hidden.delete('taints')
  const expected = exportTable(
    names.map((n) => byName.get(n)!),
    visibleColumns(NODE_COLUMNS, hidden, false),
    Date.now(),
  ).json
  expect(got.map((r) => r['Node'])).toEqual(names)
  expect(Object.keys(got[0]!)).toEqual(Object.keys(expected[0]!))
  expect(got[0]!['Taints']).toEqual(expected[0]!['Taints'])
  const strip = (rows: Record<string, unknown>[]) => rows.map((r) => Object.fromEntries(Object.entries(r).filter(([k]) => !/in use/.test(k))))
  expect(strip(got)).toEqual(strip(expected))
})

test('exports what is on screen as CSV, with the columns shown', async ({ page }) => {
  await page.getByRole('button', { name: /^Columns/ }).click()
  await page.getByRole('checkbox', { name: 'Zone' }).check()
  await page.keyboard.press('Escape')
  const names = await shownNames(page)
  await page.getByRole('button', { name: /^Export/ }).click()
  const [download] = await Promise.all([page.waitForEvent('download'), page.getByRole('button', { name: 'CSV' }).click()])
  expect(download.suggestedFilename()).toMatch(/-nodes-\d{8}-\d{4}\.csv$/)
  const text = readFileSync(await download.path(), 'utf8')
  expect(text.startsWith('\uFEFF')).toBe(true)
  const lines = text.slice(1).split('\r\n').filter(Boolean)
  expect(lines[0]).toBe('Node,Role,Status,Problems,CPU requested (cores),CPU requested %,CPU in use (cores),CPU allocatable (cores),CPU free (cores),Memory requested (GiB),Memory requested %,Memory in use (GiB),Memory allocatable (GiB),Memory free (GiB),Pods,Pod capacity,Zone,Joined')
  expect(lines.length - 1).toBe(names.length)
  expect(lines.slice(1).map((l) => l.split(',')[0])).toEqual(names)
})

test('opens node details with Enter and closes them with Escape', async ({ page, request }) => {
  const snap = await snapshot(request)
  const target = nodeRows(snap).find((r) => r.role === 'worker')!
  const row = page.locator(`[data-node="${target.name}"]`)
  await row.focus()
  await page.keyboard.press('Enter')

  const panel = page.getByTestId('node-panel')
  await expect(panel).toBeVisible()
  await expect(panel.getByRole('heading', { level: 2 })).toHaveText(target.name)
  await expect(page.getByRole('button', { name: 'Close node details' })).toBeFocused()
  await expect(row).toHaveAttribute('aria-selected', 'true')

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
  // The panel lies over the table, which keeps every column (asked at the Phase 4 review).
  await expect(page.locator('.rk-gh [data-col="cpuAlloc"]')).toHaveCount(1)
  // The node links to its pods (P2-09), and shows its YAML (P2-08).
  await expect(panel.getByTestId('node-pods-link')).toHaveAttribute('href', `#/pods?node=${target.name}`)
  await panel.locator('[data-dtab="yaml"]').click()
  await expect(panel.getByTestId('yaml')).toContainText('kind: Node')
  await expect(panel.getByTestId('yaml')).toContainText(`name: ${target.name}`)
  await expect(panel.getByRole('region', { name: 'YAML' })).not.toContainText('managedFields')
  // Apart from managed fields (and what changes between two reads), the
  // YAML is what kubectl shows (P2-08).
  const yaml = await (await request.get(`/api/v1/yaml/nodes/${target.name}`)).text()
  expect(yaml).not.toContain('managedFields')
  if (inKind) expect(stableYAML(yaml)).toBe(stableYAML(await kubectlYAML('node', target.name)))
  await panel.locator('[data-dtab="details"]').click()
  await expect(panel.getByTestId('node-status')).toBeVisible()

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
    await page.locator('[data-node] .rk-pname').first().click()
    await expect(page.getByTestId('node-panel')).toBeVisible()
    expect(await overflow()).toBeLessThanOrEqual(0)
    await page.getByTestId('node-panel').evaluate((el) => Promise.all(el.getAnimations({ subtree: true }).map((a) => a.finished)))
    const box = await page.getByTestId('node-panel').boundingBox()
    expect(box!.x).toBeGreaterThanOrEqual(0)
    expect(box!.x + box!.width).toBeLessThanOrEqual(390.5)
    await page.keyboard.press('Escape')
    await page.getByRole('button', { name: 'Settings' }).click()
    await expect(page.getByTestId('settings-page')).toBeVisible()
    expect(await overflow()).toBeLessThanOrEqual(0)
  })
})

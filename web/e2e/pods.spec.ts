// The Pods tab and the pod details, as P2-03, P2-04 and P2-09 describe them.
import { readFileSync } from 'node:fs'
import type { Page } from '@playwright/test'
import { expect, test } from './test'
import type { PodRow } from '../src/api/types'
import { allNodesReady, cluster, consoleNode, healthyWorkers, inKind, kubectlYAML, podCrashing, podRows, snapshot, stableYAML } from './cluster'

async function open(page: Page, hash = '#/pods') {
  await page.goto('/' + hash)
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  await expect(page.locator('[data-pod]').first()).toBeVisible()
}

const shownKeys = (page: Page) => page.locator('[data-pod]').evaluateAll((els) => els.map((e) => e.getAttribute('data-pod') ?? ''))

/** The pods count the heading says: "202 pods". */
async function countShown(page: Page): Promise<number> {
  const text = await page.getByTestId('pods-count').textContent()
  return Number(/(\d+) pods?/.exec(text ?? '')?.[1] ?? NaN)
}

test('lists every pod with its status, node and namespace, and filters by each', async ({ page, request }) => {
  await open(page)
  const pods = podRows(await snapshot(request, ['pods']))
  expect(pods.length).toBeGreaterThan(0)
  await expect.poll(() => countShown(page)).toBe(pods.length)

  // A namespace alone: clear the namespaces, then tick one.
  const ns = pods[0]!.namespace
  const inNs = pods.filter((p) => p.namespace === ns)
  const nsRow = page.getByTestId('namespace-list').locator('.rk-frow', { hasText: ns }).first()
  await page.getByRole('checkbox', { name: /Select all/ }).last().uncheck()
  await nsRow.getByRole('checkbox').check()
  await expect(page).toHaveURL(new RegExp(`ns=${ns}`))
  await expect.poll(() => countShown(page)).toBe(inNs.length)
  for (const k of await shownKeys(page)) expect(k.startsWith(ns + '/')).toBe(true)

  // Back to every namespace, then one node alone.
  await page.getByTestId('namespace-list').locator('.rk-frow', { hasText: ns }).first().getByRole('checkbox').check()
  await page.getByRole('checkbox', { name: /Select all \(\d+\)/ }).last().check()
  const node = pods.find((p) => p.node)!.node
  const onNode = pods.filter((p) => p.node === node)
  const nodeRow = page.getByTestId('node-list').locator('.rk-frow', { hasText: node }).first()
  await page.getByRole('checkbox', { name: /Select all/ }).first().uncheck()
  await nodeRow.getByRole('checkbox').check()
  await expect(page).toHaveURL(new RegExp(`node=${node}`))
  await expect.poll(() => countShown(page)).toBe(onNode.length)
  await expect(page.getByText(`1 of ${(await page.getByTestId('node-list').locator('.rk-frow').count()).toString()} selected`)).toBeVisible()

  // Statuses: only those with pods show, and their counts add up to the pods in scope.
  const segs = page.getByRole('group', { name: 'Pods by status' })
  const shown = segs.locator('[data-status]:not([data-status="all"])')
  const counts = (await shown.allTextContents()).map((t) => Number(t.replace(/\D/g, '')))
  expect(counts.every((n) => n > 0)).toBe(true)
  expect(counts.reduce((a, b) => a + b, 0)).toBe(onNode.length)
  await segs.locator('[data-status="running"]').click()
  await expect(page).toHaveURL(/status=running/)
  await expect.poll(async () => (await shownKeys(page)).length).toBe(Number((await segs.locator('[data-status="running"]').textContent())?.replace(/\D/g, '')))
})

test('pods on a node that stopped reporting are Unknown, in a segment of their own that stays while chosen', async ({ page, request }) => {
  test.skip(inKind, 'kind takes minutes to mark a node Unknown; the simulator shows it at once')
  await allNodesReady()
  const skip = await consoleNode()
  const node = healthyWorkers(await snapshot(request)).find((n) => n.name !== skip)!.name
  const onNode = podRows(await snapshot(request, ['pods'])).filter((p) => p.node === node && !p.done)
  expect(onNode.length).toBeGreaterThan(0)
  await open(page, `#/pods?node=${node}`)
  const segs = page.getByRole('group', { name: 'Pods by status' })
  await expect(segs.locator('[data-status="unknown"]')).toHaveCount(0)
  await cluster.stopKubelet(node)
  try {
    await expect(segs.locator('[data-status="unknown"]')).toHaveText(`Unknown ${onNode.length}`, { timeout: 10_000 })
    await expect(segs.locator('[data-status="failing"]')).toHaveCount(0)
    await segs.locator('[data-status="unknown"]').click()
    await expect.poll(() => countShown(page)).toBe(onNode.length)
  } finally {
    await cluster.startKubelet(node)
  }
  // Its pods are known again; the chosen segment stays, empty, until another is chosen.
  await expect(segs.locator('[data-status="unknown"]')).toHaveText('Unknown 0', { timeout: 10_000 })
  await segs.locator('[data-status="all"]').click()
  await expect(segs.locator('[data-status="unknown"]')).toHaveCount(0)
})

test('a node links to its pods, and a pod links to its node, namespace, Service and Helm release (P2-09)', async ({ page, request }) => {
  const pods = podRows(await snapshot(request, ['pods']))
  // From the Nodes tab: the pod count of a node opens its pods.
  await page.goto('/#/nodes')
  await expect(page.locator('[data-node]').first()).toBeVisible()
  const node = healthyWorkers(await snapshot(request))[0]!
  await page.locator(`[data-node="${node.name}"] a`).first().click()
  await expect(page).toHaveURL(new RegExp(`#/pods\\?node=${node.name}`))
  await expect(page.locator('[data-pod]').first()).toBeVisible()
  const onNode = pods.filter((p) => p.node === node.name)
  await expect.poll(() => countShown(page)).toBe(onNode.length)

  // From a pod: its Service and its release, where it has them.
  const withLinks = pods.find((p) => p.release && p.node) ?? pods.find((p) => p.node)!
  await page.goto(`/#/pods?open=${encodeURIComponent(withLinks.key)}`)
  const panel = page.getByTestId('pod-panel')
  await expect(panel.getByRole('heading', { level: 2 })).toHaveText(withLinks.name)
  // One node: its details open over this tab (asked at the Phase 4 review).
  const nodeLink = panel.getByTestId('pod-node-link')
  await expect(nodeLink).toHaveAttribute('data-peek', `node:${withLinks.node}`)
  await nodeLink.click()
  await expect(page.getByTestId('node-panel')).toContainText(withLinks.node!)
  await expect(page).toHaveURL(/#\/pods\?/)
  await page.keyboard.press('Escape')
  await expect(page.getByTestId('node-panel')).toHaveCount(0)
  await expect(panel).toBeVisible()
  await expect(panel.getByRole('link', { name: withLinks.namespace, exact: true }).first()).toHaveAttribute('href', `#/namespaces/${withLinks.namespace}`)
  if (withLinks.release) {
    await expect(panel.getByTestId('pod-release-link')).toHaveAttribute('href', `#/helm/${withLinks.namespace}/${withLinks.release}`)
    await panel.getByTestId('pod-release-link').click()
    await expect(page.getByTestId('release-name')).toHaveText(withLinks.release)
  }
})

test('a crash-looping pod shows its last termination reason, and its previous container’s logs (P2-03, P2-04)', async ({ page, request }) => {
  test.setTimeout(inKind ? 300_000 : 60_000)
  let key: string
  let cleanup = async () => {}
  if (inKind) {
    const crash = await cluster.crashLoop(await consoleNode())
    key = crash.pod
    cleanup = crash.cleanup
    await expect.poll(() => podCrashing(key), { timeout: 120_000, intervals: [1000] }).toBe(true)
  } else {
    key = podRows(await snapshot(request, ['pods'])).find((p) => p.status === 'CrashLoopBackOff')!.key
  }
  try {
    await open(page, `#/pods?q=${encodeURIComponent(key.split('/')[1]!)}`)
    const row = page.locator(`[data-pod="${key}"]`)
    // Between two back-offs a real kubelet reports the container as
    // terminated with an Error, as kubectl does; the back-off itself shows
    // as CrashLoopBackOff.
    await expect(row).toContainText(/CrashLoopBackOff|Error/, { timeout: 60_000 })
    const restarts = row.locator('.rk-cell').nth(3)
    await expect(restarts).not.toHaveText(/^0/)
    await expect(restarts.locator('[title]').first()).toHaveAttribute('title', /Last restart .*: (Error|OOMKilled|Completed)/)

    await row.click()
    const panel = page.getByTestId('pod-panel')
    await expect(panel).toContainText(/Last restart: (Error|Completed|OOMKilled)/)
    await panel.locator('[data-dtab="logs"]').click()
    // The container picker looks like a drop-down (asked at the Phase 4 review).
    const picker = panel.getByTestId('log-container')
    await expect(picker).toHaveJSProperty('tagName', 'SELECT')
    await expect(picker.locator('xpath=..').locator('.rk-selchev')).toBeVisible()
    const previous = panel.getByTestId('log-previous')
    await expect(previous).toBeEnabled()
    await previous.check()
    await expect(panel.getByTestId('log-state')).toHaveText(/\d+ lines/, { timeout: 15_000 })
    const text = await panel.getByTestId('log-lines').textContent()
    expect(text?.length).toBeGreaterThan(0)
  } finally {
    await cleanup()
  }
})

test('pod details: containers, environment, volumes, events and logs that start within 2 seconds', async ({ page, request }) => {
  const pods = podRows(await snapshot(request, ['pods']))
  // A running pod with an environment: the console's own in kind, any with a Secret in the simulator.
  const target = inKind ? pods.find((p) => p.name.startsWith('k8s-console-221b') && p.status === 'Running')! : pods.find((p) => p.status === 'Running' && p.name.startsWith('api-'))!
  await open(page, `#/pods?open=${encodeURIComponent(target.key)}`)
  const panel = page.getByTestId('pod-panel')
  await expect(panel.getByRole('heading', { level: 2 })).toHaveText(target.name)
  await expect(panel.locator('dt', { hasText: /^Pod IP$/ }).locator('+ dd')).not.toHaveText('—')
  await expect(panel.locator('dt', { hasText: /^Controlled by$/ }).locator('+ dd')).toContainText(/ReplicaSet\/.* → Deployment\//)

  await panel.locator('[data-dtab="containers"]').click()
  await expect(panel.getByRole('region', { name: /^Container / }).first()).toBeVisible()
  await expect(panel.locator('.rk-pill', { hasText: 'Running' }).first()).toBeVisible()
  await expect(panel.locator('dt', { hasText: /^Image$/ }).first().locator('+ dd')).not.toHaveText('')

  await panel.locator('[data-dtab="environment"]').click()
  const env = panel.getByRole('table', { name: /^Environment of / }).first()
  await expect(env).toBeVisible()
  if (!inKind) {
    // A variable taken from a Secret shows the reference, never the value (X-01).
    const secretVar = env.locator('.rk-gr', { hasText: 'DB_PASSWORD' })
    await expect(secretVar).toContainText('from Secret')
    await expect(secretVar).toContainText('key password')
  }

  await panel.locator('[data-dtab="volumes"]').click()
  await expect(panel.getByRole('region', { name: /^Volume / }).first()).toBeVisible()

  await panel.locator('[data-dtab="events"]').click()
  await expect(panel.getByRole('table', { name: 'Events' }).or(panel.getByText(/No events in the last hour/))).toBeVisible()

  const started = Date.now()
  await panel.locator('[data-dtab="logs"]').click()
  await expect(panel.getByTestId('log-lines').locator('div').first()).toBeVisible({ timeout: 10_000 })
  const took = Date.now() - started
  test.info().annotations.push({ type: 'logs started after', description: `${took} ms` })
  expect(took).toBeLessThan(2_000)
  await expect(panel.getByTestId('log-state')).toContainText(/Live|lines/)
  if (!inKind) {
    // The simulator keeps writing: following shows new lines without a reload.
    const before = await panel.getByTestId('log-lines').locator('div').count()
    await expect.poll(() => panel.getByTestId('log-lines').locator('div').count(), { timeout: 10_000 }).toBeGreaterThan(before)
  }
  // The container picker names every container, and the log downloads as a file.
  const picker = panel.getByTestId('log-container')
  const containers = Number(await panel.locator('[data-dtab="containers"] .rk-count').textContent())
  await expect(picker.locator('option')).toHaveCount(containers)
  const [logFile] = await Promise.all([page.waitForEvent('download'), panel.getByRole('button', { name: 'Download' }).click()])
  expect(logFile.suggestedFilename()).toMatch(new RegExp(`^${target.name}.*\\.log$`))
  expect(readFileSync(await logFile.path(), 'utf8').split('\n').filter(Boolean).length).toBeGreaterThan(0)

  await panel.locator('[data-dtab="yaml"]').click()
  await expect(panel.getByTestId('yaml')).toContainText('kind: Pod')
  await expect(panel.getByTestId('yaml')).toContainText(`name: ${target.name}`)
  await expect(panel.getByRole('region', { name: 'YAML' })).not.toContainText('managedFields')
  const yaml = await (await request.get(`/api/v1/yaml/pods/${target.namespace}/${target.name}`)).text()
  expect(yaml).not.toContain('managedFields')
  if (inKind) expect(stableYAML(yaml)).toBe(stableYAML(await kubectlYAML('pod', target.name, target.namespace)))
})

test('a pod deleted while its details are open says so', async ({ page, request }) => {
  test.setTimeout(inKind ? 300_000 : 60_000)
  let key: string
  if (inKind) {
    const crash = await cluster.crashLoop(await consoleNode())
    key = crash.pod
  } else {
    // A pod of its own in the simulator: deleting it does not come back.
    key = podRows(await snapshot(request, ['pods'])).find((p) => p.status === 'Running' && p.namespace === 'batch')!.key
  }
  await open(page, `#/pods?open=${encodeURIComponent(key)}`)
  const panel = page.getByTestId('pod-panel')
  await expect(panel.getByRole('heading', { level: 2 })).toHaveText(key.split('/')[1]!)
  await expect(panel.getByTestId('pod-deleted')).toHaveCount(0)
  await cluster.deletePod(key)
  await expect(panel.getByTestId('pod-deleted')).toBeVisible({ timeout: 30_000 })
  await expect(panel.getByTestId('pod-deleted')).toContainText('This pod was deleted')
  // The last state stays readable.
  await expect(panel.getByRole('heading', { level: 2 })).toHaveText(key.split('/')[1]!)
  await expect(page.locator(`[data-pod="${key}"]`)).toHaveCount(0)
})

test('searches with suggestions, and exports the pods on screen as CSV', async ({ page, request }) => {
  await open(page)
  const pods = podRows(await snapshot(request, ['pods']))
  const search = page.getByTestId('pod-search')
  await search.fill('Runn')
  const list = page.getByRole('listbox', { name: 'Suggestions' })
  await expect(list.getByRole('option', { name: /Running/ })).toBeVisible()
  await list.getByRole('option', { name: /Running/ }).click()
  await expect(search).toHaveValue('Running ')
  const running = pods.filter((p) => p.status === 'Running')
  // The table holds every running pod; the screen shows the first of them.
  await expect(page.locator('.rk-grid[data-total]')).toHaveAttribute('data-total', String(running.length))
  for (const k of await shownKeys(page)) expect(running.some((p) => p.key === k), k).toBe(true)

  await page.getByRole('button', { name: /^Export/ }).click()
  const [download] = await Promise.all([page.waitForEvent('download'), page.getByRole('button', { name: 'CSV' }).click()])
  expect(download.suggestedFilename()).toMatch(/-pods-\d{8}-\d{4}\.csv$/)
  const text = readFileSync(await download.path(), 'utf8')
  const lines = text.slice(1).split('\r\n').filter(Boolean)
  expect(lines[0]!.split(',').slice(0, 5)).toEqual(['Pod', 'Namespace', 'Status', 'Ready', 'Restarts'])
  expect(lines.length - 1).toBe(running.length)
  // The pod names, in the table's order: the screen shows the first of them.
  const shown = (await shownKeys(page)).map((k) => k.split('/')[1])
  expect(lines.slice(1, 1 + shown.length).map((l) => l.split(',')[0])).toEqual(shown)

  // JSON carries the same rows.
  await page.getByRole('button', { name: /^Export/ }).click()
  const [json] = await Promise.all([page.waitForEvent('download'), page.getByRole('button', { name: 'JSON' }).click()])
  const got = JSON.parse(readFileSync(await json.path(), 'utf8')) as Record<string, unknown>[]
  expect(got.length).toBe(running.length)
  expect(got[0]).toHaveProperty('Pod')
  expect(got[0]).toHaveProperty('CPU requested (cores)')
})

test('the Columns menu and the Export menu are the same on every table', async ({ page }) => {
  for (const hash of ['#/nodes', '#/pods', '#/services']) {
    await page.goto('/' + hash)
    await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
    await expect(page.locator('.rk-gr').first()).toBeVisible()
    await page.getByRole('button', { name: /^Columns/ }).first().click()
    const menu = page.getByRole('group', { name: 'Columns to show' })
    await expect(menu).toBeVisible()
    await expect(menu.getByText('The first column always shows')).toBeVisible()
    await expect(menu.getByRole('button', { name: 'Reset to default' })).toBeVisible()
    await page.keyboard.press('Escape')
    await page.getByRole('button', { name: /^Export/ }).first().click()
    const exp = page.getByRole('group', { name: 'Export format' })
    await expect(exp.getByRole('button', { name: 'Excel' })).toBeVisible()
    await expect(exp.getByRole('button', { name: 'CSV' })).toBeVisible()
    await expect(exp.getByRole('button', { name: 'JSON' })).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(page.getByRole('combobox').first()).toBeVisible()
  }
})

test.describe('on a phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true })

  test('the Pods tab and a pod’s details fit 390 px', async ({ page }) => {
    await open(page)
    const overflow = () => page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
    expect(await overflow()).toBeLessThanOrEqual(0)
    await page.locator('[data-pod] .rk-pname').first().click()
    await expect(page.getByTestId('pod-panel')).toBeVisible()
    expect(await overflow()).toBeLessThanOrEqual(0)
    // Once the drawer has slid in.
    await page.getByTestId('pod-panel').evaluate((el) => Promise.all(el.getAnimations().map((a) => a.finished)))
    const box = await page.getByTestId('pod-panel').boundingBox()
    expect(box!.x + box!.width).toBeLessThanOrEqual(390.5)
  })
})

export type { PodRow }

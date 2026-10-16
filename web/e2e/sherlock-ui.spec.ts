// Sherlock reads and changes the asking person's own view (P6U-01 to
// P6U-20): the view state goes with each question, and the UI actions the
// model calls run in the console window that asked. The fake Claude API
// runs the tool calls a question scripts as [[tool {input}]], one per
// round, so each action is exercised exactly; [[sleep ms]] waits before
// the next call, as a slow model would.
import AxeBuilder from '@axe-core/playwright'
import type { Page } from '@playwright/test'
import { expect, test } from './test'
import { cluster, healthyWorkers, nodeRows, podRows, snapshot } from './cluster'
import { E2E_DEV, signInAs } from './accounts'

test.describe.configure({ mode: 'serial' })

const KEY = 'sk-ant-e2e-fake-key-0123456789'
const ON = { clusters: { local: true }, model: 'claude-opus-5-5', dailyUSD: 20, monthlyUSD: 200 }

async function put(page: Page, path: string, body: unknown, method = 'PUT'): Promise<number> {
  return page.evaluate(
    async ([p, b, m]) => (await fetch(p as string, { method: m as string, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(b) })).status,
    [path, body, method] as const,
  )
}

async function open(page: Page, hash = '#/pods') {
  await page.goto('/' + hash)
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  await expect(page.getByTestId('sherlock-input')).toBeVisible({ timeout: 15_000 })
}

const lastAnswer = (page: Page) => page.getByTestId('sherlock-answer').last()

async function ask(page: Page, q: string, fresh = true) {
  if (fresh && (await page.getByTestId('sherlock-new').count())) await page.getByTestId('sherlock-new').click()
  await page.getByTestId('sherlock-input').fill(q)
  await page.getByTestId('sherlock-input').press('Enter')
  await expect(page.getByTestId('sherlock-question').last()).toHaveText(q)
}

async function done(page: Page) {
  await expect(lastAnswer(page)).toHaveAttribute('data-status', /done|stopped|failed/, { timeout: 30_000 })
}

const params = (page: Page) => new URL(page.url().replace('#/', 'x/'), 'http://h/').searchParams

/** The tool results the fake Claude API was last sent. */
async function lastResults(): Promise<string> {
  const reqs = await cluster.claudeRequests()
  return reqs[reqs.length - 1] ?? ''
}

test.beforeAll(async ({ browser }) => {
  const page = await browser.newPage()
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  expect(await put(page, '/api/v1/sherlock/key', { key: KEY })).toBe(200)
  expect(await put(page, '/api/v1/sherlock/config', ON)).toBe(200)
  await page.evaluate(() => localStorage.removeItem('221b.dock'))
  await page.close()
})

test.afterAll(async ({ browser }) => {
  const page = await browser.newPage()
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  await put(page, '/api/v1/sherlock/config', { ...ON, clusters: {} })
  await put(page, '/api/v1/sherlock/key', null, 'DELETE')
  await page.close()
})

test('the view goes with the question: "this node" is the one open, and secrets typed in a search are scrubbed (P6U-02, P6U-03)', async ({ page }) => {
  const node = healthyWorkers(await snapshot(page.context().request))[0]!.name
  await open(page, `#/pods?peek=node:${node}`)
  await cluster.resetClaude()
  // The details pane covers the dock; the question box is still typed in.
  await ask(page, 'Why is this node not ready?', false)
  await done(page)
  await expect(lastAnswer(page)).toContainText(`${node}, the node you have open`)
  const sent = (await cluster.claudeRequests())[0]!
  expect(sent).toContain('console-view')
  expect(sent).toContain(`node:${node}`)

  const token = 'ghp_' + 'Zq7'.repeat(12)
  await open(page, `#/pods?q=${token}`)
  await cluster.resetClaude()
  await ask(page, 'What needs attention right now?')
  await done(page)
  const all = (await cluster.claudeRequests()).join('\n')
  expect(all).not.toContain(token)
  expect(all).toContain('[REDACTED]')
})

test('Sherlock opens a tab, filters, searches, sorts and changes columns, and focus stays put (P6U-05, P6U-09)', async ({ page }) => {
  const pods = podRows(await snapshot(page.context().request, ['pods']))
  const ns = pods[0]!.namespace
  await open(page, '#/pods')
  await ask(
    page,
    `Show me [[view_open {"tab":"pods","filters":{"status":"all"}}]] [[view_filter {"add":{"ns":["${ns}"]}}]] [[view_search {"table":"pods","text":"a"}]] [[view_sort {"table":"pods","column":"restarts","direction":"desc"}]] [[view_columns {"table":"pods","show":["owner"],"hide":["node"]}]]`,
  )
  const focused = await page.evaluate(() => document.activeElement?.getAttribute('data-testid') ?? document.activeElement?.tagName)
  await done(page)
  expect(params(page).get('ns')).toBe(ns)
  expect(params(page).get('q')).toBe('a')
  await expect(page.locator('button[data-col=restarts]').first()).toHaveAttribute('data-active', 'true')
  await expect(page.locator('button[data-col=owner]')).toHaveCount(1)
  await expect(page.locator('button[data-col=node]')).toHaveCount(0)
  const notice = lastAnswer(page).getByTestId('sherlock-view-changes').locator('li')
  await expect(notice).toHaveCount(5)
  await expect(notice.nth(3)).toHaveText('Sorted pods by Restarts, descending')
  expect(await page.evaluate(() => document.activeElement?.getAttribute('data-testid') ?? document.activeElement?.tagName)).toBe(focused)

  // Not saved: after a reload the columns are the person's own again (P6U-11).
  await page.reload()
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  await expect(page.locator('button[data-col=node]')).toHaveCount(1)
  await expect(page.locator('button[data-col=owner]')).toHaveCount(0)
})

test('Sherlock highlights a row and opens and closes details without taking focus (P6U-05, P6U-09)', async ({ page }) => {
  await open(page, '#/pods')
  const first = page.locator('[data-testid=rows] [role=row]').first()
  const name = (await first.getAttribute('data-key'))!
  const [ns, pod] = name.split('/')
  await ask(page, `[[view_highlight {"table":"pods","row":"${name}"}]] [[view_details {"kind":"Pod","namespace":"${ns}","name":"${pod}"}]]`)
  await expect(page.locator(`[data-testid=rows] [data-key="${name}"]`)).toHaveAttribute('data-highlight', 'true')
  await done(page)
  expect(params(page).get('peek')).toBe(`pod:${ns}/${pod}`)
  await expect(page.getByRole('dialog')).toBeVisible()
  expect(await page.evaluate(() => !!document.activeElement?.closest('[role=dialog]'))).toBe(false)
  // The drawer covers the page; focus is still in Sherlock's box, so the person can ask on.
  await ask(page, '[[view_details {"close":true}]]', false)
  await done(page)
  expect(params(page).get('peek')).toBeNull()
  await expect(page.getByRole('dialog')).toHaveCount(0)
})

test('Undo puts the view back as it was before the answer’s first change (P6U-10)', async ({ page }) => {
  const pod = podRows(await snapshot(page.context().request, ['pods']))[0]!
  await open(page, '#/pods')
  const before = new URL(page.url()).hash
  await ask(
    page,
    `[[view_filter {"set":{"status":"running"}}]] [[view_search {"table":"pods","text":"e"}]] [[view_sort {"table":"pods","column":"restarts","direction":"desc"}]] [[view_columns {"table":"pods","show":["owner"]}]] [[view_details {"kind":"Pod","namespace":"${pod.namespace}","name":"${pod.name}"}]]`,
  )
  await done(page)
  await expect(page.locator('button[data-col=owner]')).toHaveCount(1)
  const live = lastAnswer(page).getByTestId('sherlock-view-changed').locator('[role=status]')
  await expect(live).toHaveAttribute('aria-live', 'polite')
  // A drawer Sherlock opened leaves the dock in reach.
  await lastAnswer(page).getByTestId('sherlock-view-undo').click()
  await expect(lastAnswer(page).getByTestId('sherlock-view-changed')).toContainText('were undone')
  expect(new URL(page.url()).hash).toBe(before)
  await expect(page.locator('button[data-col=owner]')).toHaveCount(0)
  await expect(page.locator('button[data-col=name]').first()).toHaveAttribute('data-active', 'true')
  await expect(page.getByRole('dialog')).toHaveCount(0)
})

test('what Sherlock may not do is refused, changes nothing, and another cluster is a link (P6U-04, P6U-06, P6U-12)', async ({ page }) => {
  await open(page, '#/pods')
  const before = new URL(page.url()).hash
  await cluster.resetClaude()
  await ask(
    page,
    '[[view_filter {"set":{"open":"x"}}]] [[view_sort {"table":"pods","column":"secret","direction":"asc"}]] [[view_open {"tab":"https://evil.example"}]] [[view_search {"table":"pods","text":"a","script":"alert(1)"}]] [[view_open {"tab":"nodes","cluster":"staging"}]]',
  )
  await done(page)
  expect(new URL(page.url()).hash).toBe(before)
  await expect(lastAnswer(page).getByTestId('sherlock-view-changed')).toHaveCount(0)
  await expect(lastAnswer(page).getByTestId('sherlock-steps').locator('[data-status=failed]')).toHaveCount(5)
  const sent = (await cluster.claudeRequests()).join('\n')
  for (const why of ['has no filter', 'has no column', 'must be one of', 'view_search takes no', 'does not switch to another cluster']) expect(sent).toContain(why)
  const link = lastAnswer(page).getByTestId('sherlock-console-link')
  await expect(link).toHaveAttribute('href', '#/c/staging/nodes')
})

test('values Sherlock sets are data: markup in a search stays text (P6U-13)', async ({ page }) => {
  await open(page, '#/pods')
  const evil = '<img src=x onerror="window.__pwned=1">'
  await ask(page, `[[view_search {"table":"pods","text":${JSON.stringify(evil)}}]]`)
  await done(page)
  await expect.poll(() => page.evaluate(() => [...document.querySelectorAll<HTMLInputElement>('input[type=search]')].map((i) => i.value))).toContain(evil)
  await expect(lastAnswer(page).getByTestId('sherlock-view-changes')).toContainText(evil)
  expect(await page.evaluate(() => (window as unknown as { __pwned?: number }).__pwned)).toBeUndefined()
})

test('Sherlock yields when the person changes the view during the answer (P6U-09)', async ({ page }) => {
  await open(page, '#/pods')
  await ask(page, '[[view_state]] [[sleep 2500]] [[view_filter {"set":{"status":"failing"}}]]')
  await expect(lastAnswer(page).getByTestId('sherlock-steps').locator('[data-status=done]')).toHaveCount(1, { timeout: 15_000 })
  await page.getByRole('link', { name: /^Nodes/ }).first().click()
  await expect(page).toHaveURL(/#\/nodes/)
  await done(page)
  expect(page.url()).not.toContain('status=failing')
  await expect(lastAnswer(page)).toContainText('You changed the view yourself')
  await expect(lastAnswer(page).getByTestId('sherlock-view-changed')).toHaveCount(0)
})

test('only the window that asked changes; in Sherlock’s own window, the main window it follows (P6U-08)', async ({ page, context }) => {
  await open(page, '#/pods')
  const other = await context.newPage()
  await open(other, '#/pods')
  await ask(page, '[[view_filter {"set":{"status":"running"}}]]')
  await done(page)
  expect(params(page).get('status')).toBe('running')
  expect(params(other).get('status')).toBeNull()
  await other.close()

  // From Sherlock's own window, the main window that opened it changes.
  await open(page, '#/pods')
  const [popup] = await Promise.all([context.waitForEvent('page'), page.locator('[data-testid=dock-popout]:visible').click()])
  await popup.waitForLoadState('domcontentloaded')
  await expect(popup.getByTestId('sherlock-input')).toBeVisible({ timeout: 15_000 })
  await ask(popup, '[[view_filter {"set":{"status":"pending"}}]]')
  await done(popup)
  await expect.poll(() => params(page).get('status')).toBe('pending')
  // Undo there puts the main window back.
  await popup.getByTestId('sherlock-answer').last().getByTestId('sherlock-view-undo').click()
  await expect.poll(() => params(page).get('status')).toBeNull()

  // With the main window gone, Sherlock says so instead of guessing.
  await page.close()
  await ask(popup, '[[view_filter {"set":{"status":"failing"}}]]')
  await done(popup)
  await expect(popup.getByTestId('sherlock-answer').last()).toContainText('No console window is open')
  await popup.close()
})

test('an action on an object the person may not see says only that it is not available (P6U-07)', async ({ page, browser }) => {
  const me = (await (await page.context().request.get('/api/v1/me')).json()) as { mode: string }
  test.skip(me.mode === 'none', 'sign-in is off on this console')
  const allowed = await cluster.limitDev()
  const all = podRows(await snapshot(page.context().request, ['pods']))
  const target = all.find((p) => !allowed.includes(p.namespace))!
  const dev = await signInAs(browser, E2E_DEV)
  try {
    await dev.goto('/#/pods')
    await expect(dev.getByTestId('cluster-state')).toHaveText('Connected', { timeout: 20_000 })
    await expect(dev.getByTestId('sherlock-input')).toBeVisible({ timeout: 15_000 })
    await cluster.resetClaude()
    await ask(dev, `[[view_details {"kind":"Pod","namespace":"${target.namespace}","name":"${target.name}"}]]`)
    await done(dev)
    await expect(dev.getByTestId('sherlock-answer').last()).toContainText('not available')
    const sent = (await cluster.claudeRequests()).join('\n')
    expect(sent).not.toContain(target.uid)
    if (target.node) expect(sent).not.toContain(`"node":"${target.node}"`)
  } finally {
    await dev.context().close()
  }
})

test('with view changes off, Sherlock only reads the view (P6U-14)', async ({ page }) => {
  await open(page, '#/pods')
  expect(await put(page, '/api/v1/sherlock/config', { ...ON, viewControl: false })).toBe(200)
  try {
    await cluster.resetClaude()
    await ask(page, 'What needs attention right now?')
    await done(page)
    const sent = await lastResults()
    expect(sent).toContain('"name":"view_state"')
    expect(sent).not.toContain('"name":"view_filter"')
  } finally {
    expect(await put(page, '/api/v1/sherlock/config', ON)).toBe(200)
  }
})

test('every table on the tabs is registered, with its sort and search (P6U-01)', async ({ page }) => {
  const node = nodeRows(await snapshot(page.context().request))[0]!.name
  const tabs: [string, string[]][] = [
    ['#/nodes', ['nodes']],
    ['#/pods', ['pods']],
    ['#/services', ['services']],
    ['#/networks', ['net-routes', 'net-nodes']],
    ['#/access', ['access-who', 'access-bindings']],
    ['#/policies', ['policy-coverage']],
    ['#/policies/pdb', ['policy-drains', 'policy-pdbs']],
    ['#/namespaces', ['namespace-objects']],
    [`#/nodes?peek=node:${node}`, ['nodes']],
  ]
  for (const [hash, ids] of tabs) {
    await open(page, hash)
    await cluster.resetClaude()
    // A details pane the person opened covers the dock; the box keeps focus there.
    await ask(page, '[[view_state]]', !hash.includes('peek'))
    await done(page)
    const sent = await lastResults()
    for (const id of ids) expect(sent, `${id} on ${hash}`).toContain(`\\"id\\":\\"${id}\\"`)
  }
})

test('the change notice and the highlight meet WCAG 2.2 AA in both themes and at 390 px (P6U-18)', async ({ page }) => {
  const TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa']
  for (const theme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    await open(page, '#/pods')
    const name = (await page.locator('[data-testid=rows] [role=row]').nth(3).getAttribute('data-key'))!
    await ask(page, `[[view_sort {"table":"pods","column":"name","direction":"asc"}]] [[view_highlight {"table":"pods","row":"${name}"}]]`)
    await done(page)
    await expect(page.locator('[data-highlight=true]')).toHaveCount(1)
    await expect(lastAnswer(page).getByTestId('sherlock-view-changed')).toBeVisible()
    const results = await new AxeBuilder({ page }).withTags(TAGS).analyze()
    expect(results.violations, `${theme}: ${results.violations.map((v) => v.id).join(', ')}`).toEqual([])
  }
  // At 390 px the pods table is long; the notice is what is new there.
  await page.setViewportSize({ width: 390, height: 844 })
  await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' })
  await open(page, '#/alerts')
  await ask(page, '[[view_filter {"set":{"kind":"pods"}}]]')
  await done(page)
  await expect(lastAnswer(page).getByTestId('sherlock-view-changed')).toBeVisible()
  const narrow = await new AxeBuilder({ page }).withTags(TAGS).include('[data-testid=sherlock-view-changed]').analyze()
  expect(narrow.violations.map((v) => v.id)).toEqual([])
  const box = await lastAnswer(page).getByTestId('sherlock-view-changed').boundingBox()
  expect(box!.width).toBeLessThanOrEqual(390)
})

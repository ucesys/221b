// The README's screenshots (docs/screenshots), taken from the simulated
// cluster so they show sample data only: `make screenshots`, or the
// Screenshots workflow, which commits them. Each shot is its own test, so
// one that fails leaves the others to be taken.
import { expect, test, type Browser, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { E2E_PASSWORD, E2E_USER } from '../e2e/accounts'
import { healthyWorkers, podRows, snapshot } from '../e2e/cluster'

const OUT = process.env.SHOTS_DIR ?? '../docs/screenshots'
const SAME = { 'Sec-Fetch-Site': 'same-origin' }
const SHERLOCK_ON = { clusters: { local: true }, model: 'claude-opus-5-5', dailyUSD: 20, monthlyUSD: 200 }
// Any key of the right shape: the simulator's fake model API answers.
const FAKE_KEY = 'sk-ant-screenshots-fake-key-0123456789'

let page: Page

async function signIn(browser: Browser): Promise<Page> {
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: 'light' })
  const p = await context.newPage()
  await p.goto('/', { waitUntil: 'domcontentloaded' })
  const oidc = p.getByTestId('signin-oidc')
  const local = p.getByTestId('signin-form')
  await expect(oidc.or(local).or(p.getByTestId('cluster'))).toBeVisible({ timeout: 20_000 })
  if (await oidc.count()) {
    await oidc.click()
    await p.waitForURL(/\/oidc\/auth/)
    await p.fill('input[name="email"]', E2E_USER)
    await p.fill('input[name="password"]', E2E_PASSWORD)
    await p.click('button[type="submit"]')
  } else if (await local.count()) {
    await local.locator('input[name="email"]').fill(E2E_USER)
    await local.locator('input[name="password"]').fill(E2E_PASSWORD)
    await local.getByTestId('signin-submit').click()
  }
  await expect(p.getByTestId('cluster')).toBeVisible({ timeout: 20_000 })
  return p
}

test.beforeAll(async ({ browser }) => {
  await mkdir(OUT, { recursive: true })
  page = await signIn(browser)
})

test.afterAll(async () => {
  await page?.context().close()
})

/** Opens a page, waits until it is ready and has settled, and saves it. */
async function shot(name: string, hash: string, ready?: (p: Page) => Promise<void>) {
  await page.goto('/' + hash)
  await expect(page.getByTestId('cluster')).toBeVisible({ timeout: 20_000 })
  if (ready) await ready(page)
  await save(name)
}

async function save(name: string) {
  // Live numbers, charts and fades settle; no tooltip under the pointer.
  await page.mouse.move(1, 1)
  await page.waitForTimeout(2_000)
  await page.screenshot({ path: `${OUT}/${name}.png` })
}

const connected = async (p: Page) => {
  await expect(p.getByTestId('cluster-state')).toHaveText('Connected', { timeout: 20_000 })
}

test('home', async () => {
  await shot('home', '#/', async (p) => {
    await expect(p.getByTestId('home-add')).toBeVisible({ timeout: 20_000 })
  })
})

for (const [name, hash] of [
  ['nodes', '#/nodes'],
  ['fleets', '#/fleets'],
  ['alerts', '#/alerts'],
  ['networks', '#/networks'],
  ['access', '#/access'],
  ['policies', '#/policies'],
  ['helm', '#/helm'],
] as const) {
  test(name, async () => {
    await shot(name, hash, connected)
  })
}

test('pod details', async () => {
  const rows = podRows(await snapshot(page.request, ['pods']))
  const pod = rows.find((r) => r.status === 'Pending') ?? rows.find((r) => r.status !== 'Running') ?? rows[0]!
  await shot('pod-details', `#/pods?peek=pod:${pod.namespace}/${pod.name}`, connected)
})

for (const [name, hash] of [
  ['admin-roles', '#/admin/roles'],
  ['admin-permissions', '#/admin/permissions'],
  ['admin-users', '#/admin/users'],
] as const) {
  test(name, async () => {
    await shot(name, hash)
  })
}

test('add a cluster', async () => {
  await page.goto('/#/')
  await page.getByTestId('home-add').click()
  await page.getByTestId('rbac-toggle').click()
  await expect(page.getByTestId('rbac-yaml')).toBeVisible()
  await save('add-cluster')
  await page.keyboard.press('Escape')
})

test('sherlock', async () => {
  await page.goto('/#/pods')
  await connected(page)
  expect((await page.request.put('/api/v1/sherlock/key', { data: { key: FAKE_KEY }, headers: SAME })).status()).toBe(200)
  expect((await page.request.put('/api/v1/sherlock/config', { data: SHERLOCK_ON, headers: SAME })).status()).toBe(200)
  await page.evaluate(() => localStorage.removeItem('221b.dock'))
  const rows = podRows(await snapshot(page.request, ['pods']))
  const pod = rows.find((r) => r.status === 'Pending') ?? rows.find((r) => r.status !== 'Running') ?? rows[0]!
  await page.goto('/#/pods')
  await connected(page)
  const input = page.getByTestId('sherlock-input')
  await expect(input).toBeVisible({ timeout: 15_000 })
  await input.fill(`Why is ${pod.namespace}/${pod.name} ${pod.status === 'Pending' ? 'pending' : 'not running'}?`)
  await input.press('Enter')
  await expect(page.getByTestId('sherlock-answer').last()).toHaveAttribute('data-status', 'done', { timeout: 30_000 })
  await save('sherlock')
})

test('watson approval card', async () => {
  const node = healthyWorkers(await snapshot(page.request))[0]!.name
  await page.request.put('/api/v1/watson/config', { data: { twoPerson: false }, headers: SAME })
  const res = await page.request.post('/api/v1/watson/commands', {
    data: { command: `kubectl drain ${node} --ignore-daemonsets`, from: 'Typed', run: true },
    headers: SAME,
  })
  expect(res.status(), await res.text()).toBe(202)
  await page.goto('/#/nodes')
  await connected(page)
  if (await page.getByTestId('dock-show-watson').count()) await page.getByTestId('dock-show-watson').click()
  const card = page.getByTestId('watson-card').last()
  await expect(card).toBeVisible({ timeout: 15_000 })
  await expect(card.getByTestId('watson-approve')).toBeEnabled({ timeout: 60_000 })
  await card.scrollIntoViewIfNeeded()
  await save('watson-approval')
})

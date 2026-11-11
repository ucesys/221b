// The README's screenshots (docs/screenshots), taken from the simulated
// cluster so they show sample data only: `make screenshots`, or the
// Screenshots workflow, which commits them. Each shot is its own test, so
// one that fails leaves the others to be taken.
import { expect, test, type Browser, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { E2E_DEV, E2E_DEV_KUBE, E2E_PASSWORD, E2E_USER } from '../e2e/accounts'
import { healthyWorkers, podRows, snapshot } from '../e2e/cluster'

const OUT = process.env.SHOTS_DIR ?? '../docs/screenshots'
const SAME = { 'Sec-Fetch-Site': 'same-origin' }
const SHERLOCK_ON = { clusters: { local: true }, model: 'claude-opus-5-5', dailyUSD: 20, monthlyUSD: 200 }
// Any key of the right shape: the simulator's fake model API answers.
const FAKE_KEY = 'sk-ant-screenshots-fake-key-0123456789'

let page: Page

async function signIn(browser: Browser, email = E2E_USER): Promise<Page> {
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: 'light' })
  const p = await context.newPage()
  await p.goto('/', { waitUntil: 'domcontentloaded' })
  const oidc = p.getByTestId('signin-oidc')
  const local = p.getByTestId('signin-form')
  await expect(oidc.or(local).or(p.getByTestId('cluster'))).toBeVisible({ timeout: 20_000 })
  if (await oidc.count()) {
    await oidc.click()
    await p.waitForURL(/\/oidc\/auth/)
    await p.fill('input[name="email"]', email)
    await p.fill('input[name="password"]', E2E_PASSWORD)
    await p.click('button[type="submit"]')
  } else if (await local.count()) {
    await local.locator('input[name="email"]').fill(email)
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

for (const [name, hash] of [['admin-roles', '#/admin/roles']] as const) {
  test(name, async () => {
    await shot(name, hash)
  })
}

// AIOps (Phase 12). The simulator starts with sample cases, three runs of
// the nightly audit and a known issue (casework's seed), so these pages
// have something to show. They are taken before Sherlock is turned on
// below, so the dock is not over them.
async function aiops(name: string, hash: string, ready: (p: Page) => Promise<void>) {
  await page.goto('/' + hash)
  await ready(page)
  await save(name)
}

test('aiops inbox', async () => {
  await aiops('aiops-inbox', '#/aiops/inbox', async (p) => {
    await expect(p.getByRole('grid', { name: 'Inbox' })).toHaveCount(1, { timeout: 20_000 })
    await expect(p.locator('[data-inbox-row]').first()).toBeVisible({ timeout: 20_000 })
  })
})

test('aiops case', async () => {
  // An agent-found case: its finding, how often it was seen and its analysis.
  await page.goto('/#/aiops/inbox?source=agents')
  const row = page.locator('[data-inbox-row^="case:"]').first()
  await expect(row).toBeVisible({ timeout: 20_000 })
  const n = (await row.getAttribute('data-inbox-row'))!.split(':')[1]
  await aiops('aiops-case', `#/aiops/inbox/case/${n}`, async (p) => {
    await expect(p.getByTestId('case-drawer')).toBeVisible({ timeout: 20_000 })
  })
  await page.keyboard.press('Escape')
})

for (const [name, hash, testid] of [
  ['aiops-jobs', '#/aiops/jobs', 'jobs-tab'],
  ['aiops-rules', '#/aiops/jobs/rules', 'rules-table'],
  ['aiops-known-issues', '#/aiops/mycroft/known', 'mycroft-tab'],
] as const) {
  test(name, async () => {
    await aiops(name, hash, async (p) => {
      await expect(p.getByTestId(testid)).toBeVisible({ timeout: 20_000 })
    })
  })
}

test('aiops runs', async () => {
  // A run carries its agent's and clusters' ids; the page shows their names
  // once the jobs' list has come (U-11, U-12), so wait for them.
  await aiops('aiops-runs', '#/aiops/jobs/runs', async (p) => {
    await expect(p.getByTestId('runs-table')).toBeVisible({ timeout: 20_000 })
    await expect(p.getByTestId('run-row').first()).toContainText('Auditor · simulated', { timeout: 20_000 })
  })
})

// Automation. The simulator has no scripts of its own, so the shot makes
// three, written with the header the console teaches (# 221b:describe).
const SCRIPTS = [
  {
    name: 'disk-usage',
    group: 'Checks',
    text: `#!/bin/sh
# 221b:describe Reports the file systems above a threshold
# 221b:param THRESHOLD optional percent used that counts as full (85)
# 221b:privilege nobody
# 221b:facts os.family
set -eu
df -P -x tmpfs -x devtmpfs | awk -v t="\${THRESHOLD:-85}" 'NR > 1 && $5 + 0 >= t { print $6, $5 }'
`,
  },
  {
    name: 'rotate-journal',
    group: 'Maintenance',
    text: `#!/bin/sh
# 221b:describe Trims the systemd journal to a size
# 221b:param KEEP optional how much to keep (500M)
# 221b:privilege elevated
# 221b:dry-run yes
set -eu
if [ "\${DRY_RUN:-0}" = 1 ]; then journalctl --disk-usage; exit 0; fi
journalctl --vacuum-size="\${KEEP:-500M}"
`,
  },
  {
    name: 'restart-checkout',
    group: 'Maintenance',
    text: `#!/bin/sh
# 221b:describe Restarts the shop's checkout and waits for it
# 221b:target kubernetes
# 221b:privilege none
# 221b:dry-run yes
# 221b:k8s rollout restart deployments
# 221b:k8s rollout status deployments
kubectl -n shop rollout restart deployment/checkout
kubectl -n shop rollout status deployment/checkout --timeout=120s
`,
  },
]

test('automation', async () => {
  for (const script of SCRIPTS) {
    // 409 when a run before this one made it already.
    const res = await page.request.post('/api/v1/automation/scripts', { data: script, headers: SAME })
    expect([200, 201, 409], await res.text()).toContain(res.status())
  }
  await shot('automation', '#/scripts', async (p) => {
    await expect(p.getByTestId('scripts-table')).toBeVisible({ timeout: 20_000 })
    await expect(p.getByTestId('script-row')).toHaveCount(SCRIPTS.length, { timeout: 20_000 })
  })
  // One of them open: its text, its header as the console read it, its versions.
  await page.locator('[data-testid=script-row][data-name=rotate-journal]').getByTestId('script-open').click()
  await expect(page.getByTestId('script-drawer')).toBeVisible({ timeout: 20_000 })
  await save('automation-script')
  await page.keyboard.press('Escape')
})

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
  const answer = page.getByTestId('sherlock-answer').last()
  await expect(answer).toHaveAttribute('data-status', 'done', { timeout: 30_000 })
  // The dock's controls sit in the right-hand pane's header (Watson's).
  await page.getByTestId('watson').getByTestId('sherlock-maximize').first().click()
  await answer.evaluate((el) => el.scrollIntoView({ block: 'start' }))
  await save('sherlock')
  await page.keyboard.press('Escape')
})

test('admin settings', async () => {
  // Settings › Agents, with Sherlock on and its key saved by the test above.
  await page.goto('/#/admin/settings/agents')
  await expect(page.getByTestId('cluster')).toBeVisible({ timeout: 20_000 })
  await expect(page.locator('[data-testid=settings-section][data-section=agents]')).toBeVisible({ timeout: 20_000 })
  // The dock the test above opened would cover half the page. It comes a
  // moment after the page, so wait for it (or for its icon) before looking.
  const dock = page.getByTestId('sherlock-dock')
  await expect(dock.or(page.getByTestId('sherlock-fab')).first()).toBeVisible({ timeout: 15_000 })
  if (await dock.isVisible()) {
    // Two panes, two headers: the button that shows is the right-hand pane's.
    await page.locator('[data-testid=sherlock-minimize]:visible').first().click()
    await expect(dock).toHaveCount(0)
  }
  await save('admin-settings')
  // Open again: the next shot looks for Watson's card in the dock.
  const fab = page.getByTestId('sherlock-fab')
  if (await fab.isVisible()) {
    await fab.click()
    await expect(dock).toBeVisible()
  }
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
  const card = page.getByTestId('watson-card').last()
  await expect(card).toBeVisible({ timeout: 15_000 })
  await expect(card.getByTestId('watson-approve')).toBeEnabled({ timeout: 60_000 })
  await page.getByTestId('watson').getByTestId('sherlock-maximize').first().click()
  await card.evaluate((el) => el.scrollIntoView({ block: 'start' }))
  await save('watson-approval')
})

test('aiops troubleshoot', async () => {
  // A case's drawer with Sherlock on (the test above): the case on the
  // left, Sherlock and Watson on the right, the conversation the case's.
  // The case is one made here, about a pod that does not run, so Sherlock
  // has something to read.
  const rows = podRows(await snapshot(page.request, ['pods']))
  const pod = rows.find((r) => r.status === 'CrashLoopBackOff') ?? rows.find((r) => r.status !== 'Running') ?? rows[0]!
  const res = await page.request.post('/api/v1/aiops/cases', {
    data: { title: `${pod.name} keeps restarting`, cluster: 'local', resource: `${pod.namespace}/Pod/${pod.name}`, priority: 2, type: 'availability' },
    headers: SAME,
  })
  expect(res.status(), await res.text()).toBe(201)
  const { number } = (await res.json()) as { number: number }
  // The dock of the shots before would sit under the drawer: put it away.
  await page.goto('/#/aiops/inbox')
  await page.reload()
  const dock = page.getByTestId('sherlock-dock')
  await expect(dock.or(page.getByTestId('sherlock-fab')).first()).toBeVisible({ timeout: 15_000 })
  if (await dock.isVisible()) {
    await page.locator('[data-testid=sherlock-minimize]:visible').first().click()
    await expect(dock).toHaveCount(0)
  }
  await page.goto(`/#/aiops/inbox/case/${number}`)
  const drawer = page.getByTestId('case-drawer')
  await expect(drawer.getByTestId('drawer-split')).toHaveAttribute('data-layout', 'split', { timeout: 20_000 })
  const sherlock = drawer.getByTestId('case-trouble').getByTestId('sherlock')
  await sherlock.getByTestId('sherlock-input').fill(`Why is ${pod.namespace}/${pod.name} failing?`)
  // Asking waits for the case's stream to say Sherlock is available.
  await expect(sherlock.getByTestId('sherlock-send')).toBeEnabled({ timeout: 20_000 })
  await sherlock.getByTestId('sherlock-send').click()
  await expect(sherlock.getByTestId('sherlock-answer').last()).toHaveAttribute('data-status', 'done', { timeout: 30_000 })
  await save('aiops-troubleshoot')
  await page.keyboard.press('Escape')
})

// The Storage tab (docs/storage.png), and the other four pages of
// docs/storage.md's design: a person bound to analytics and batch, the
// drawers of a bound claim and of one that cannot bind, and a class's
// drawer, with Sherlock on for its questions and Watson's actions.
test('storage', async ({ browser }) => {
  const ready = async (p: Page) => {
    await connected(p)
    // Used arrives with the first read of the kubelets.
    await expect(p.locator('.rk-st-used').first()).toBeVisible({ timeout: 60_000 })
  }
  // Like the other tabs' shots, the pages themselves without the dock: a
  // hash change does not reload the app, so it reloads to drop the dock,
  // and minimizes it should it still show.
  const noDock = async (p: Page) => {
    await p.reload()
    await ready(p)
    const min = p.getByTestId('sherlock-minimize').first()
    if (await min.isVisible().catch(() => false)) await min.click()
    await expect(p.getByTestId('sherlock-dock')).toHaveCount(0)
  }
  expect((await page.request.put('/api/v1/sherlock/config', { data: { ...SHERLOCK_ON, clusters: {} }, headers: SAME })).status()).toBe(200)
  await page.goto('/#/storage')
  await noDock(page)
  await save('storage')
  const sim = process.env.E2E_SIM_URL ?? 'http://127.0.0.1:7443'
  expect((await fetch(`${sim}/sim/deny`, { method: 'POST', body: JSON.stringify({ only: { [E2E_DEV_KUBE]: ['analytics', 'batch'] } }) })).ok).toBe(true)
  const viewer = await signIn(browser, E2E_DEV)
  try {
    await viewer.goto('/#/storage')
    await noDock(viewer)
    await viewer.mouse.move(1, 1)
    await viewer.waitForTimeout(2_000)
    await viewer.screenshot({ path: `${OUT}/storage-namespace.png` })
  } finally {
    await viewer.context().close()
  }
  expect((await page.request.put('/api/v1/sherlock/key', { data: { key: FAKE_KEY }, headers: SAME })).status()).toBe(200)
  expect((await page.request.put('/api/v1/sherlock/config', { data: SHERLOCK_ON, headers: SAME })).status()).toBe(200)
  await page.reload()
  for (const [name, peek, testid] of [
    ['storage-claim', 'claim:storage/data-postgres-0', 'storage-chain'],
    ['storage-pending', 'claim:analytics/data-cache-2', 'storage-chain'],
    ['storage-class', 'class:longhorn', 'storage-class-panel'],
  ] as const) {
    await shot(name, `#/storage?peek=${encodeURIComponent(peek)}`, async (p) => {
      await ready(p)
      await expect(p.getByTestId(testid)).toBeVisible()
    })
  }
})

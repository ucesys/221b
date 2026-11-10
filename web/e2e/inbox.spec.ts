// The Inbox as one table and Sherlock in Cases (Phase 12, IB-08 to IB-13,
// IN-01 to IN-10; wave 6): every kind in one table (findings are cases
// now), pending first, its filters in the address (Source: People |
// Agents too) and each row opening its drawer; a proposed change that
// someone else approved first refused with who and when, objects linked
// with their age; Sherlock and Watson in the dock over Cases, told which
// page and which item are open, the conversation scrolling in its pane;
// and the top bar's bell, which is the Inbox's (CF-80).
import type { Page, Route } from '@playwright/test'
import { expect, test } from './test'
import { cluster, podRows, snapshot } from './cluster'
import type { Proposal } from '../src/api/cards'

test.describe.configure({ mode: 'serial' })

const SAME = { 'Sec-Fetch-Site': 'same-origin' }
const KEY = 'sk-ant-e2e-fake-key-0123456789'
const ON = { clusters: { local: true }, model: 'claude-opus-5-5', dailyUSD: 20, monthlyUSD: 200 }

async function put(page: Page, path: string, body: unknown, method = 'PUT'): Promise<number> {
  return page.evaluate(
    async ([p, b, m]) => (await fetch(p as string, { method: m as string, headers: { 'Content-Type': 'application/json' }, body: b === null ? undefined : JSON.stringify(b) })).status,
    [path, body, method] as const,
  )
}

/**
 * A proposed change on the local cluster, as the agents' cards come: the
 * simulator runs no agent, so the page is given one, for a pod that runs.
 */
function card(pod: string, over: Partial<Proposal> = {}): Proposal {
  const now = Date.now()
  return {
    id: 'PC-900',
    agent: 'troubleshooter',
    agentName: 'Troubleshooter',
    source: { kind: 'case', id: 'C-41', label: 'Case C-41: web crash-loops' },
    cluster: 'local',
    clusterName: 'simulated',
    title: 'Restart web',
    reasoning: 'It cannot reach its database since the Secret changed.',
    evidence: [],
    actions: [{ kind: 'kubectl', args: { command: `kubectl delete pod ${pod.split('/')[1]} -n ${pod.split('/')[0]}` }, risk: 'low' }],
    condition: { kind: 'none' },
    expires: new Date(now + 5 * 3600_000).toISOString(),
    state: 'queued',
    created: new Date(now - 2 * 3600_000).toISOString(),
    updated: new Date(now - 2 * 3600_000).toISOString(),
    needed: 1,
    plans: [{ summary: `kubectl delete pod`, targets: [`pod ${pod}`] }],
    canApprove: true,
    ...over,
  }
}

/** Serves the card to the page; Approve answers as the server does when Sam approved it first. */
async function serveCard(page: Page, c: Proposal) {
  const approved: Proposal = { ...c, state: 'approved', approvals: [{ by: { key: 'sam', name: 'Sam' }, at: new Date(Date.now() - 60_000).toISOString() }] }
  await page.route('**/api/v1/cases/proposals**', async (route: Route) => {
    const url = new URL(route.request().url())
    // The drawer's Sherlock on this made-up card: there is none (CF-61).
    if (url.pathname.includes('/sherlock/')) {
      await route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ error: 'no such proposal' }) })
      return
    }
    if (url.pathname.endsWith('/approve')) {
      await route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: 'Already approved by Sam at 2026-11-06 14:03:07 UTC; it is approved', proposal: approved }) })
      return
    }
    if (url.pathname.endsWith(`/proposals/${c.id}`)) {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(c) })
      return
    }
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ proposals: [c], waiting: 1, mayApprove: true }) })
  })
}

async function runningPod(page: Page): Promise<string> {
  const pods = podRows(await snapshot(page.context().request, ['pods'])).filter((p) => p.status === 'Running')
  expect(pods.length).toBeGreaterThan(0)
  return `${pods[0]!.namespace}/${pods[0]!.name}`
}

test.afterAll(async ({ browser }) => {
  const page = await browser.newPage()
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  await put(page, '/api/v1/sherlock/config', { ...ON, clusters: {} })
  await put(page, '/api/v1/sherlock/key', null, 'DELETE')
  await page.close()
})

test('the Inbox is one table of every kind, pending first, with its filters in the address and a drawer for each row (IN-01 to IN-06)', async ({ page }) => {
  const res = await page.request.post('/api/issues/v1/reports', { headers: SAME, data: { text: `The VPN drops every hour, inbox ${Date.now()}`, for: { id: 'dev', name: 'Dev Person' } } })
  expect(res.status(), await res.text()).toBe(201)
  const n = ((await res.json()) as { report: { number: number } }).report.number
  // A case being analysed is listed as well (the wave-4 Inbox left it out).
  const made = await page.request.post('/api/v1/cases/cases', { headers: SAME, data: { title: `Inbox case ${Date.now()}`, cluster: 'local', priority: 1 } })
  expect(made.status(), await made.text()).toBe(201)
  const c = (await made.json()) as { number: number }
  await page.goto('/#/cases/inbox')
  await expect(page.getByTestId('inbox-groups')).toHaveCount(0)
  await expect(page.getByRole('grid', { name: 'Inbox' })).toHaveCount(1)
  // No Kind filter or column since wave 6c (CF-38): a row's id and status say what it is.
  await expect(page.getByTestId('inbox-kinds')).toHaveCount(0)
  await expect(page.getByRole('columnheader')).toHaveText([/^Priority/, /^Title/, /^Type/, /^Incident/, /^Cluster/, /^Object/, /^Status/, /^Waiting/, /^Assignee/, /^Last change/])
  await expect(page.getByTestId('inbox-show-pending')).toHaveAttribute('aria-pressed', 'true')
  const kase = page.locator(`[data-inbox-row="case:${c.number}"]`)
  await expect(kase).toHaveAttribute('aria-label', new RegExp(`^Case C-${c.number}: `))
  await expect(kase.getByTestId('inbox-row-priority')).toHaveText('P1')
  await expect(kase.getByTestId('inbox-row-status')).toHaveText('New')
  // A report is a case of its own, C-n (CU-01).
  const row = page.locator(`[data-inbox-row="case:${n}"]`)
  await expect(row).toHaveAttribute('aria-label', new RegExp(`^Case C-${n}: `))
  await expect(row.getByTestId('inbox-row-waiting')).toHaveText(/^\d+(s|m)$/)
  // P1 before the report's priority: the case above the report.
  const order = await page.locator('[data-inbox-row]').evaluateAll((rs) => rs.map((r) => r.getAttribute('data-inbox-row')))
  expect(order.indexOf(`case:${c.number}`)).toBeLessThan(order.indexOf(`case:${n}`))
  // Source: the agents' rows alone leave people's cases out; People keeps them.
  await page.getByTestId('inbox-source-agents').click()
  await expect(page).toHaveURL(/#\/cases\/inbox\?source=agents$/)
  await expect(row).toHaveCount(0)
  await page.getByTestId('inbox-source-people').click()
  await expect(page).toHaveURL(/#\/cases\/inbox\?source=people$/)
  await expect(row).toBeVisible()
  await page.getByTestId('inbox-source-all').click()
  await expect(page).toHaveURL(/#\/cases\/inbox$/)
  await page.getByTestId('inbox-show-all').click()
  await expect(page).toHaveURL(/#\/cases\/inbox\?show=all$/)
  await expect(row).toBeVisible()
  // A row opens its drawer at its own address, and Escape closes it.
  await row.locator('[role=rowheader]').click()
  await expect(page).toHaveURL(new RegExp(`#/cases/inbox/case/${n}\\?show=all$`))
  await expect(page.getByTestId('case-drawer')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByTestId('case-drawer')).toHaveCount(0)
  await expect(page).toHaveURL(/#\/cases\/inbox\?show=all$/)
  // The old addresses land on the same drawers.
  await page.goto(`/#/cases/inbox?report=${n}`)
  await expect(page).toHaveURL(new RegExp(`#/cases/inbox/report/${n}$`))
  await expect(page.getByTestId('case-drawer')).toContainText(`C-${n}`)
  await page.goto(`/#/cases/cases/${c.number}`)
  await expect(page.getByTestId('case-drawer')).toBeVisible()
  await expect(page.locator(`[data-inbox-row="case:${c.number}"]`)).toHaveAttribute('aria-selected', 'true')
})

test('a proposed change someone else approved first is refused with who and when, and links its object with its age (IB-06, IB-08)', async ({ page }) => {
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  const pod = await runningPod(page)
  await serveCard(page, card(pod))
  // An address of before, with a kind: the kind is let go (CF-38), the row is there.
  await page.goto('/#/cases/inbox?kind=proposals')
  const row = page.locator('[data-inbox-row="proposal:PC-900"]')
  await expect(row).toHaveAttribute('aria-label', /^Proposed change PC-900: /)
  const obj = row.getByTestId('object-link')
  await expect(obj.locator('a')).toHaveAttribute('href', `#/pods?open=${encodeURIComponent(pod)}`)
  await expect(obj.getByTestId('object-age')).toHaveText(/^running \d+/)
  await row.locator('[role=rowheader]').click()
  await expect(page).toHaveURL(/#\/cases\/inbox\/proposal\/PC-900$/)
  const drawer = page.getByTestId('proposal-drawer')
  await expect(drawer.getByTestId('proposal-started-by')).toHaveText('Case analysis of C-41: web crash-loops')
  // Run in Watson brings its card to the drawer's Watson, which asks for the approval (CF-62).
  await drawer.getByTestId('proposal-run-watson').click()
  await drawer.getByTestId('watson-proposal-card').getByTestId('proposal-approve').click()
  await expect(drawer.getByTestId('proposal-error')).toHaveText(/^Already approved by Sam at .+\.$/)
  await expect(drawer.getByTestId('proposal-state')).toHaveText('Approved, running')
  await expect(drawer.getByTestId('proposal-approve')).toHaveCount(0)
  await expect(drawer.getByTestId('proposal-run-watson')).toHaveCount(0)
  // The object opens on its page in the Kubernetes area.
  await page.keyboard.press('Escape')
  await obj.locator('a').click()
  await expect(page).toHaveURL(new RegExp(`#/pods\\?open=${encodeURIComponent(pod).replace(/[.]/g, '\\.')}`))
})

test('the Inbox takes most of a wide screen, with Columns and Export just above its table (CF-66)', async ({ page }) => {
  await page.setViewportSize({ width: 1920, height: 1000 })
  await page.goto('/#/cases/inbox')
  const grid = page.getByRole('grid', { name: 'Inbox' })
  await expect(grid).toHaveCount(1)
  const area = (await page.getByTestId('cases-page').boundingBox())!
  const col = (await page.getByTestId('cases-inbox').boundingBox())!
  // Its side margins are about a quarter of the 1280px column's.
  const before = (area.width - 40 - 1280) / 2 + 20
  expect(col.width).toBeGreaterThan(1280)
  expect(col.x - area.x).toBeLessThan(before / 4 + 10)
  // Columns and Export sit on their own row right above the table, at its right; New case stays on the first row.
  const tools = page.getByTestId('inbox-table-tools')
  const t = (await tools.boundingBox())!
  const g = (await grid.boundingBox())!
  expect(t.y + t.height).toBeLessThanOrEqual(g.y + 1)
  expect(t.x + t.width).toBeGreaterThan(g.x + g.width - 4)
  await expect(tools.getByRole('button', { name: 'Export' })).toBeVisible()
  await expect(tools.getByTestId('cases-new-case')).toHaveCount(0)
  expect((await page.getByTestId('cases-new-case').boundingBox())!.y).toBeLessThan((await page.getByTestId('inbox-source').boundingBox())!.y)
  // Jobs, Schedule and Runs alike, is as wide as the Inbox.
  for (const hash of ['#/cases/jobs', '#/cases/jobs/runs']) {
    await page.goto('/' + hash)
    const jobs = (await page.getByTestId('jobs-area').boundingBox())!
    expect(Math.abs(jobs.width - col.width), `${hash} is as wide as the Inbox`).toBeLessThanOrEqual(2)
  }
  // On a phone the toolbar wraps without a sideways scroll.
  await page.setViewportSize({ width: 390, height: 800 })
  await page.goto('/#/cases/inbox')
  await expect(grid).toHaveCount(1)
  for (const row of [page.getByTestId('inbox-show').locator('..'), page.getByTestId('inbox-table-tools')]) {
    expect(await row.evaluate((el) => el.scrollWidth <= el.clientWidth + 1 && el.getBoundingClientRect().right <= window.innerWidth + 1)).toBe(true)
  }
})

test('the bell stands for what waits in the Inbox, in every area, and opens it; the Inbox links to the alerts (CF-80, CF-81)', async ({ page }) => {
  for (const hash of ['#/nodes', '#/scripts', '#/cases/inbox', '#/admin/users']) {
    await page.goto('/' + hash)
    const bell = page.getByTestId('inbox-bell')
    await expect(bell).toBeVisible()
    // Right of Live, the first of the icons (CF-41); no yellow i beside it.
    const [b, l] = [await bell.boundingBox(), await page.getByTestId('live').boundingBox()]
    expect(l!.x).toBeLessThan(b!.x)
    expect(await bell.evaluate((el) => el.previousElementSibling?.getAttribute('data-testid'))).toBe('live')
    await expect(page.getByTestId('admin-flag')).toHaveCount(0)
    // Its colour says what waits; it carries no number.
    await expect(bell).toHaveAttribute('data-state', /^(p01|waiting|none)$/)
    await expect(bell).toHaveAttribute('title', /^(\d+ waiting for you(, .+)?: open AIOps › Inbox|Nothing waiting for you in AIOps › Inbox)$/)
    await expect(bell).toHaveText('')
  }
  await page.getByTestId('inbox-bell').click()
  await expect(page).toHaveURL(/#\/cases\/inbox$/)
  // The simulator has alerts going on: the Inbox says how many, and opens the Alerts tab.
  const link = page.getByTestId('inbox-alerts')
  await expect(link).toHaveText(/^\d+ alerts? now$/)
  await link.click()
  await expect(page).toHaveURL(/#\/alerts/)
})

test('Sherlock is in the dock over Cases and is told the page and the item open; Open in Watson fills Watson’s box (IB-10 to IB-13)', async ({ page }) => {
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  expect(await put(page, '/api/v1/sherlock/key', { key: KEY })).toBe(200)
  expect(await put(page, '/api/v1/sherlock/config', ON)).toBe(200)
  await page.evaluate(() => localStorage.removeItem('221b.dock'))
  const pod = await runningPod(page)
  await serveCard(page, card(pod))
  // A link of before: the query, and the card's c<n> (CF-56).
  await page.goto('/#/cases/inbox?proposal=c900')
  await expect(page).toHaveURL(/#\/cases\/inbox\/proposal\/PC-900$/)
  await expect(page.getByTestId('proposal-drawer')).toContainText('Restart web')
  const input = page.getByTestId('sherlock-input')
  await expect(input).toBeVisible({ timeout: 15_000 })
  await cluster.resetClaude()
  await input.fill('What does this change do?')
  await input.press('Enter')
  await expect(page.getByTestId('sherlock-answer').last()).toHaveAttribute('data-status', /done|stopped|failed/, { timeout: 30_000 })
  const sent = (await cluster.claudeRequests()).join('\n')
  // The Cases skill (AIOps), and the view: the Inbox, with the change open.
  expect(sent).toContain('AIOps. The person is asking from AIOps')
  expect(sent).toMatch(/\\"view\\":\\"cases\\"/)
  expect(sent).toMatch(/\\"kind\\":\\"proposal\\",\\"id\\":\\"PC-900\\"/)
  expect(sent).toContain('Restart web')
  // Open in Watson: the command waits in Watson's box; nothing runs.
  const command = `kubectl get pod ${pod.split('/')[1]} -n ${pod.split('/')[0]}`
  await page.evaluate((c) => window.dispatchEvent(new CustomEvent('221b-watson-fill', { detail: { cluster: 'local', command: c } })), command)
  await expect(page.getByTestId('watson-input')).toHaveValue(command)
  await expect(page.getByTestId('watson-input')).toBeFocused()
  // The change waits on this cluster, but Watson's panel keeps its room for
  // commands: no notice of the Inbox there (IN-08).
  await expect(page.getByTestId('watson-log')).toBeVisible()
  await expect(page.getByTestId('watson-proposals')).toHaveCount(0)
})

for (const width of [1440, 800]) {
  test(`the conversation scrolls in Sherlock’s pane and the page never grows with it, ${width} wide (IN-09)`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    await page.goto('/#/nodes')
    await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
    expect(await put(page, '/api/v1/sherlock/key', { key: KEY })).toBe(200)
    expect(await put(page, '/api/v1/sherlock/config', ON)).toBe(200)
    await page.evaluate(() => localStorage.removeItem('221b.dock'))
    for (const hash of ['#/cases/inbox', '#/nodes']) {
      await page.goto('/' + hash)
      const input = page.getByTestId('sherlock-input')
      await expect(input).toBeVisible({ timeout: 15_000 })
      if (await page.getByTestId('sherlock-new').count()) await page.getByTestId('sherlock-new').click()
      // The page as it is once its list is read: narrower than 900 px the
      // page scrolls as a whole, so the list's rows lengthen it on their own.
      if (hash === '#/nodes') await expect(page.getByRole('grid').first().getByRole('row').nth(1)).toBeVisible()
      else await expect(page.getByTestId('inbox-shown')).not.toHaveText('Loading…')
      const height = () => page.evaluate(() => document.scrollingElement!.scrollHeight)
      const before = await height()
      for (let i = 0; i < 4; i++) {
        await input.fill(`Which nodes are not ready? ${i}`)
        await input.press('Enter')
        await expect(page.getByTestId('sherlock-answer').last()).toHaveAttribute('data-status', /done|stopped|failed/, { timeout: 30_000 })
      }
      expect(await height()).toBe(before)
      const chat = await page.locator('.rk-chat').evaluate((el) => ({ h: el.clientHeight, s: el.scrollHeight, top: el.scrollTop }))
      expect(chat.s).toBeGreaterThan(chat.h)
      // Kept at its end, the newest answer in view.
      expect(chat.top + chat.h).toBeGreaterThanOrEqual(chat.s - 60)
    }
  })
}

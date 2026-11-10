// The Cases area (Phase 12, CS-01 to CS-20, IN-01): the third area of the
// top bar with its tabs Inbox, Jobs and Mycroft, the Inbox, a report triaged end to end against the issue service,
// and the ways into the report page, which open it in a new tab with the
// text handed over, never sent by itself.
import type { Page } from '@playwright/test'
import { expect, test } from './test'

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

/** Files a report through the API, for someone else, as support does. */
async function file(page: Page, text: string, forUser = 'dev'): Promise<number> {
  const res = await page.request.post('/api/issues/v1/reports', { headers: SAME, data: { text, for: { id: forUser, name: 'Dev Person' } } })
  expect(res.status(), await res.text()).toBe(201)
  return ((await res.json()) as { report: { number: number } }).report.number
}

test.afterAll(async ({ browser }) => {
  // Sherlock off again, so the other specs see the console without the panel.
  const page = await browser.newPage()
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  await put(page, '/api/v1/sherlock/config', { ...ON, clusters: {} })
  await page.close()
})

test('Cases is the third area, with its tabs, no count on the switch, and kept in Administration (CS-01, CS-02, CS-04, CF-88)', async ({ page }) => {
  const n = await file(page, `The intranet is slow today, case ${Date.now()}`)
  await page.goto('/#/alerts')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  const cases = page.getByTestId('area-cases')
  await expect(cases).toBeVisible()
  // No count on the switch: the bell says what waits (CF-88).
  await expect(cases.getByTestId('cases-count')).toHaveCount(0)
  await expect(cases).toHaveText('AIOps')
  await cases.click()
  await expect(page).toHaveURL(/#\/cases\/inbox$/)
  await expect(cases).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByTestId('cases-nav').locator('[data-cases-tab]')).toHaveText(['Inbox', 'Jobs', 'Mycroft'].map((t) => new RegExp(`^${t}`)))
  // The report, a case of its own (CU-01).
  const row = page.locator(`[data-inbox-row="case:${n}"]`)
  await expect(row).toBeVisible()
  // A filter narrows the one table, and says so in the address (IN-04); no Kind filter since wave 6c (CF-38).
  await expect(page.getByTestId('inbox-kinds')).toHaveCount(0)
  await page.getByTestId('inbox-source-agents').click()
  await expect(page).toHaveURL(/source=agents/)
  await expect(row).toHaveCount(0)
  await page.getByTestId('inbox-source-all').click()
  await page.getByTestId('inbox-mine').click()
  await expect(page).toHaveURL(/who=mine/)
  await expect(row).toHaveCount(0)
  await page.getByTestId('inbox-unassigned').click()
  await expect(row).toBeVisible()
  // Administration keeps Cases selected, the area it was opened from (CF-88).
  await page.getByTestId('admin-link').click()
  await expect(page.getByTestId('admin-page')).toBeVisible()
  await expect(cases).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByTestId('area-kubernetes')).toHaveAttribute('aria-pressed', 'false')
  await expect(page.getByTestId('cases-nav')).toBeVisible()
  // Back to Kubernetes, where the cluster's tabs are.
  await page.getByTestId('area-kubernetes').click()
  await expect(page.locator('.rk-tab[data-tab=alerts]')).toBeVisible()
  // And Administration keeps Kubernetes now.
  await page.getByTestId('admin-link').click()
  await expect(page.getByTestId('admin-page')).toBeVisible()
  await expect(page.getByTestId('area-kubernetes')).toHaveAttribute('aria-pressed', 'true')
})

// A report triaged end to end, New Case and On behalf of are Cases now:
// casework.spec.ts (Phase 12, wave 5, CU-09 to CU-11).

test('the account menu has Report a problem and My cases (CS-16)', async ({ page }) => {
  await page.goto('/#/nodes')
  await page.getByTestId('account').click()
  await expect(page.getByTestId('account-report')).toHaveAttribute('href', '/report')
  await expect(page.getByTestId('account-my-reports')).toHaveAttribute('href', '/report/mine')
  const [tab] = await Promise.all([page.context().waitForEvent('page'), page.getByTestId('account-my-reports').click()])
  await tab.waitForLoadState('domcontentloaded')
  await expect(tab.getByTestId('my-reports').or(tab.getByText('Nothing yet.'))).toBeVisible()
  await expect(tab.getByTestId('report-text')).toHaveCount(0)
  await tab.close()
})

test('the report page shows known problems like what is typed, with their workarounds, and sends nothing by itself (wave 6)', async ({ page }) => {
  const stamp = Date.now()
  const made = await page.request.post('/api/issues/v1/known', { headers: SAME, data: { title: `Printing queue stalls zebra${stamp}`, symptoms: ['printing stalls'], workaround: 'Print from the web app meanwhile.', status: 'identified' } })
  expect(made.status(), await made.text()).toBeLessThan(300)
  await page.goto('/report')
  await page.getByTestId('report-text').fill(`My printing queue stalls since this morning zebra${stamp}`)
  const maybe = page.getByTestId('report-maybe-known')
  await expect(maybe).toContainText(`Printing queue stalls zebra${stamp}`)
  await expect(maybe).toContainText('Print from the web app meanwhile.')
  // Sending stays the person's choice; nothing was sent.
  await expect(page.getByRole('button', { name: 'Send' })).toBeEnabled()
  await expect(page.getByTestId('reply-card')).toHaveCount(0)
})

test('the report page sends the priority and cluster the reporter chose (CF-64)', async ({ page }) => {
  const stamp = Date.now()
  await page.goto('/report')
  const prio = page.getByTestId('report-priority')
  await expect(prio).toHaveValue('')
  await expect(prio.locator('option')).toHaveText(['Not sure', /^P1: Blocking \(/, /^P2: Service degradation \(/, /^P3: Preemptive \(/, /^P4: Question \(/])
  const cluster = page.getByTestId('report-cluster')
  await expect(cluster.locator('option').first()).toHaveText('Not sure')
  await expect(cluster.locator('option[value="local"]')).toHaveCount(1)
  await page.getByTestId('report-text').fill(`The build page shows an old version quail${stamp}`)
  await prio.selectOption('3')
  await cluster.selectOption('local')
  await page.getByRole('button', { name: 'Send' }).click()
  const card = page.getByTestId('reply-card')
  await expect(card).toBeVisible()
  const n = Number((await card.textContent())?.match(/C-(\d+)/)?.[1])
  expect(n).toBeGreaterThan(0)
  const got = (await (await page.request.get(`/api/issues/v1/reports/${n}`)).json()) as { priority: number; priorityBy?: string; cluster?: string }
  expect(got.priority).toBe(3)
  expect(got.priorityBy).toBe('reporter')
  expect(got.cluster).toBe('local')
  await expect(prio).toHaveValue('')
  // An unknown cluster is refused.
  const bad = await page.request.post('/api/issues/v1/reports', { headers: SAME, data: { text: `x quail${stamp}`, cluster: 'no-such-cluster' } })
  expect(bad.status()).toBe(400)
})

test('the report page leaves out known issues made inactive as duplicates (CF-63)', async ({ page }) => {
  const stamp = Date.now()
  const make = async (title: string) => {
    const res = await page.request.post('/api/issues/v1/known', { headers: SAME, data: { title, symptoms: ['scanner stalls'], workaround: 'Scan from the second floor.', status: 'identified' } })
    expect(res.status(), await res.text()).toBeLessThan(300)
    return ((await res.json()) as { id: string }).id
  }
  const kept = await make(`Scanner queue stalls heron${stamp}`)
  const dup = await make(`Scanner queue stalls again heron${stamp}`)
  const off = await page.request.patch(`/api/issues/v1/known/${dup}`, { headers: SAME, data: { inactive: true } })
  expect(off.status(), await off.text()).toBe(200)
  await page.goto('/report')
  const list = page.getByTestId('known-list')
  await expect(list).toContainText(`Scanner queue stalls heron${stamp}`)
  await expect(list).not.toContainText(`stalls again heron${stamp}`)
  await page.getByTestId('report-text').fill(`My scanner queue stalls since this morning heron${stamp}`)
  const maybe = page.getByTestId('report-maybe-known')
  await expect(maybe).toContainText(`Scanner queue stalls heron${stamp}`)
  await expect(maybe).not.toContainText(`stalls again heron${stamp}`)
  // The console's Inactive filter still has it: an operator asking for all.
  const all = (await (await page.request.get('/api/issues/v1/known?all=true')).json()) as { known: { id: string; inactive?: boolean }[] }
  expect(all.known.find((k) => k.id === dup)?.inactive).toBe(true)
  expect(all.known.some((k) => k.id === kept)).toBe(true)
})

test('Sherlock’s Report this opens the report page with the conversation filled in, and sends nothing (CS-17, CS-18)', async ({ page }) => {
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  expect(await put(page, '/api/v1/sherlock/key', { key: KEY })).toBe(200)
  expect(await put(page, '/api/v1/sherlock/config', ON)).toBe(200)
  await page.reload()
  await expect(page.getByTestId('sherlock-input')).toBeVisible({ timeout: 15_000 })
  if (await page.getByTestId('sherlock-new').count()) await page.getByTestId('sherlock-new').click()
  const before = ((await (await page.request.get('/api/issues/v1/reports')).json()) as { reports: unknown[] }).reports.length
  await page.getByTestId('sherlock-input').fill('Which nodes are not ready?')
  await page.getByTestId('sherlock-input').press('Enter')
  await expect(page.getByTestId('sherlock-answer').last()).toHaveAttribute('data-status', /done|stopped|failed/, { timeout: 30_000 })
  const link = page.getByTestId('sherlock-report')
  await expect(link).toBeVisible()
  const [tab] = await Promise.all([page.context().waitForEvent('page'), link.click()])
  await tab.waitForLoadState('domcontentloaded')
  // Handed over in this browser: neither the query nor, once read, the fragment carries it.
  await expect(tab.getByTestId('report-text')).toHaveValue(/^I asked Sherlock about .+: Which nodes are not ready\?/)
  await expect(tab.getByTestId('handoff-note')).toBeVisible()
  expect(new URL(tab.url()).search).toBe('')
  expect(new URL(tab.url()).hash).toBe('')
  // Nothing was sent.
  const after = ((await (await page.request.get('/api/issues/v1/reports')).json()) as { reports: unknown[] }).reports.length
  expect(after).toBe(before)
  await tab.close()
})

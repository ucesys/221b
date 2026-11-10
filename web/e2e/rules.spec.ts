// Validation rules (Phase 12, ADR-138): AIOps › Jobs › Validation rules
// (in Mycroft until CF-89, whose old addresses lead there) lists the check
// ids the agents name their findings by, built-in and added, as a table
// as wide as the Mycroft tables; a SuperAdmin
// adds a rule, edits its meaning, retires it and makes it active again,
// replaces one by another (the old id an alias of the new), and deletes a
// rule added that nothing holds. Since CF-85 it is one table: the ids
// the agents made up (the fake nightly audit names two alike, each with
// its description, CF-86) are rows under Unlisted, each with a
// drawer listing its findings by run; the housekeeping suggests them as
// one rule, a row under Suggested that says why, and a row of the
// SuperAdmin's Inbox (CF-87). The spec leaves the suggestion undecided:
// a promote or a dismiss would stay for every run after.
import type { Page } from '@playwright/test'
import { expect, test } from './test'

test.describe.configure({ mode: 'serial' })

const SAME = { 'Sec-Fetch-Site': 'same-origin' }
const KEY = 'sk-ant-e2e-fake-key-0123456789'
const ON = { clusters: { local: true }, model: 'claude-opus-5-5', dailyUSD: 20, monthlyUSD: 200 }
/** The ids the fake nightly audit makes up, alike in words. */
const SKEW = 'other.node-clock-skew'
const DRIFT = 'other.node-clock-drift'

interface Found {
  id: string
  runs: number
  suggestion?: string
}
interface Suggestion {
  id: string
  from: string[]
  why?: string
}

async function api(page: Page, method: string, path: string, body?: unknown): Promise<{ status: number; json: unknown }> {
  const res = await page.request.fetch(path, { method, headers: SAME, data: body === undefined ? undefined : body })
  let json: unknown = null
  try {
    json = await res.json()
  } catch {
    // No body.
  }
  return { status: res.status(), json }
}

/** Runs a job now and waits for its run to end. */
async function runJob(page: Page, job: string): Promise<string> {
  // The last run's turn is let go a moment after it is kept as ended.
  let started = await api(page, 'POST', `/api/v1/jobs/${job}/run`, {})
  for (let i = 0; i < 20 && started.status === 409; i++) {
    await page.waitForTimeout(250)
    started = await api(page, 'POST', `/api/v1/jobs/${job}/run`, {})
  }
  expect(started.status, JSON.stringify(started.json)).toBe(202)
  const id = (started.json as { id: string }).id
  await expect.poll(async () => ((await api(page, 'GET', `/api/v1/runs/${id}`)).json as { status: string }).status, { timeout: 30_000 }).not.toBe('running')
  return id
}

const rulesPage = async (page: Page) => (await api(page, 'GET', '/api/v1/cases/rules')).json as { found: Found[]; suggestions: Suggestion[] }

interface Rule {
  id: string
  state: string
  says: string
  replacedBy?: string
  aliases?: string[]
  builtIn: boolean
}

async function send(page: Page, path: string, method: string, body?: unknown): Promise<number> {
  return page.evaluate(
    async ([p, m, b]) => (await fetch(p as string, { method: m as string, headers: { 'Content-Type': 'application/json' }, body: b === undefined ? undefined : JSON.stringify(b) })).status,
    [path, method, body] as const,
  )
}

async function rules(page: Page): Promise<Rule[]> {
  return ((await (await page.request.get('/api/v1/cases/rules')).json()) as { rules: Rule[] }).rules
}

const stamp = Date.now()
const added = `e2e.host-path-${stamp}`
const oldId = `e2e.old-${stamp}`
const newId = `e2e.new-${stamp}`

test.beforeAll(async ({ browser }) => {
  // The nightly audit runs on the simulated cluster with the fake Claude API.
  const page = await browser.newPage()
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  expect((await api(page, 'PUT', '/api/v1/sherlock/key', { key: KEY })).status).toBe(200)
  expect((await api(page, 'PUT', '/api/v1/sherlock/config', ON)).status).toBe(200)
  expect((await api(page, 'PUT', '/api/v1/jobs/nightly-audit', { clusters: ['local'] })).status).toBe(200)
  await page.close()
})

test.afterAll(async ({ browser }) => {
  // The rules this spec added go, so the other specs see the list as before.
  const page = await browser.newPage()
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  // The fake audit proposes a change each run (CF-26): left waiting, it would wait in the Inbox of the specs after.
  const queued = ((await api(page, 'GET', '/api/v1/cases/proposals?state=queued')).json as { proposals?: { id: string; source: { kind: string } }[] } | null)?.proposals ?? []
  for (const c of queued.filter((p) => p.source.kind === 'job')) await api(page, 'POST', `/api/v1/cases/proposals/${c.id}/reject`, { reason: 'e2e: done' })
  await api(page, 'PUT', '/api/v1/jobs/nightly-audit', { clusters: [] })
  await api(page, 'PUT', '/api/v1/sherlock/config', { ...ON, clusters: {} })
  for (const r of (await rules(page)).filter((x) => x.id.startsWith('e2e.'))) {
    if (r.state !== 'active') await send(page, `/api/v1/cases/rules/${r.id}/activate`, 'POST')
  }
  for (const r of (await rules(page)).filter((x) => x.id.startsWith('e2e.'))) await send(page, `/api/v1/cases/rules/${r.id}`, 'DELETE')
  await page.close()
})

test('the validation rules are a table as wide as the Mycroft tables in Jobs, filtered by state and job, each opening in a drawer (ADR-138, CF-89)', async ({ page }) => {
  // The old Mycroft address leads to Jobs › Validation rules, and says so in the address.
  await page.goto('/#/cases/jobs/rules')
  await expect(page).toHaveURL(/#\/cases\/jobs\/rules$/)
  await expect(page.getByTestId('jobs-sub-rules')).toHaveAttribute('aria-pressed', 'true')
  await expect(page.locator('[data-cases-tab=jobs]')).toHaveAttribute('aria-current', 'page')
  await expect(page.getByTestId('mycroft-sub-rules')).toHaveCount(0)
  const table = page.getByTestId('rules-table')
  await expect(table).toBeVisible({ timeout: 20_000 })
  await expect(table).toHaveAttribute('style', /min-width: 980px/)
  await expect(page.getByTestId('rules-show-active')).toHaveAttribute('aria-pressed', 'true')
  // Every row has Findings, Runs and Clusters (CF-85), seen before a drawer narrows the table.
  for (const col of ['findings', 'runs', 'clusters']) await expect(table.locator(`thead button[data-col="${col}"]`)).toHaveCount(1)
  const priv = page.locator('[data-testid="rule-row"][data-id="pod.privileged"]')
  await expect(priv.getByTestId('rule-state')).toHaveText('active')
  await expect(priv.getByTestId('rule-origin')).toHaveText(/^Built-in/)
  // A rule of the alert diagnosis only is not a nightly audit's.
  await page.getByTestId('rules-job').selectOption('nightly-audit')
  await expect(page.locator('[data-testid="rule-row"][data-id="pod.crashloop"]')).toHaveCount(0)
  await page.getByTestId('rules-job').selectOption('alert-diagnosis')
  await expect(page.locator('[data-testid="rule-row"][data-id="pod.crashloop"]')).toHaveCount(1)
  await page.getByTestId('rules-job').selectOption('')
  await page.getByTestId('rules-search').fill('pod.privileged')
  await expect(page.getByTestId('rule-row')).toHaveCount(1)
  await page.getByTestId('rule-row').first().click()
  await expect(page).toHaveURL(/#\/cases\/jobs\/rules\/pod\.privileged$/)
  const d = page.getByTestId('rule-drawer')
  await expect(d.getByTestId('rule-fields')).toContainText('Nightly audit')
  // A built-in is never deleted: retired instead.
  await expect(d.getByTestId('rule-delete')).toHaveCount(0)
  await expect(d.getByTestId('rule-retire')).toBeVisible()
  // One table (CF-85): the ids found and the suggestions are filters of it, not sections; every row has Findings, Runs and Clusters.
  await expect(page.getByTestId('rules-show-suggested')).toBeVisible()
  await expect(page.getByTestId('rules-show-found')).toHaveText(/^Unlisted/)
  await expect(d.getByTestId('rule-cases-list')).toBeVisible()
  await expect(d.getByTestId('rule-findings-list')).toBeVisible()
})

test('a SuperAdmin adds a rule, edits its meaning, retires it, makes it active and deletes it while nothing holds it (ADR-138)', async ({ page }) => {
  await page.goto('/#/cases/jobs/rules')
  await page.getByTestId('rule-add').click()
  const f = page.getByTestId('rule-form')
  await expect(f.getByTestId('rule-form-submit')).toBeDisabled()
  await f.getByTestId('rule-form-id').fill(added)
  await f.getByTestId('rule-form-says').fill('a pod mounts a host path')
  await f.getByTestId('rule-form-type').selectOption('security')
  await f.getByTestId('rule-form-submit').click()
  await expect(f).toHaveCount(0)
  const row = page.locator(`[data-testid="rule-row"][data-id="${added}"]`)
  await expect(row.getByTestId('rule-origin')).toHaveText('Added')
  await expect(row.getByTestId('rule-jobs')).toHaveText('Nightly audit')

  await row.click()
  const d = page.getByTestId('rule-drawer')
  await d.getByTestId('rule-edit').click()
  await d.getByTestId('rule-says').fill('a pod mounts a path of its node')
  await d.getByTestId('rule-save').click()
  await expect(d.getByTestId('rule-fields')).toContainText('a pod mounts a path of its node')
  expect((await rules(page)).find((r) => r.id === added)?.says).toBe('a pod mounts a path of its node')

  await d.getByTestId('rule-retire').click()
  await expect(d.getByTestId('rule-retired')).toBeVisible()
  expect((await rules(page)).find((r) => r.id === added)?.state).toBe('retired')
  await d.getByTestId('rule-activate').click()
  await expect(d.getByTestId('rule-retire')).toBeVisible()

  await d.getByTestId('rule-delete').click()
  await page.getByTestId('confirm').getByRole('button', { name: 'Delete' }).click()
  await expect(page).toHaveURL(/#\/cases\/jobs\/rules$/)
  await expect(row).toHaveCount(0)
  expect((await rules(page)).some((r) => r.id === added)).toBe(false)
})

test('Replace by merges two rules: the old id is an alias of the new, until made active again (ADR-138)', async ({ page }) => {
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  for (const id of [oldId, newId]) expect(await send(page, '/api/v1/cases/rules', 'POST', { id, says: `e2e ${id}`, type: 'other', jobs: ['nightly-audit'] })).toBe(200)
  await page.goto(`/#/cases/jobs/rules/${oldId}`)
  const d = page.getByTestId('rule-drawer')
  await d.getByTestId('rule-replace').click()
  await d.getByTestId('rule-replace-by').selectOption(newId)
  await d.getByTestId('rule-replace-confirm').click()
  await expect(d.getByTestId('rule-replaced')).toContainText(newId)
  const now = await rules(page)
  expect(now.find((r) => r.id === oldId)).toMatchObject({ state: 'replaced', replacedBy: newId })
  expect(now.find((r) => r.id === newId)?.aliases).toEqual([oldId])
  // The replaced rule is under Replaced; the new one names its alias.
  await d.getByRole('button', { name: 'Close validation rule' }).last().click()
  await page.getByTestId('rules-search').fill(`${stamp}`)
  await expect(page.locator(`[data-testid="rule-row"][data-id="${newId}"]`).getByTestId('rule-aliases')).toHaveText(`also ${oldId}`)
  await page.getByTestId('rules-show-replaced').click()
  await expect(page.locator(`[data-testid="rule-row"][data-id="${oldId}"]`).getByTestId('rule-replaced-by')).toHaveText(`by ${newId}`)
  // Make active undoes the merge.
  await page.locator(`[data-testid="rule-row"][data-id="${oldId}"]`).click()
  await page.getByTestId('rule-drawer').getByTestId('rule-activate').click()
  await expect(page.getByTestId('rule-drawer').getByTestId('rule-retire')).toBeVisible()
  expect((await rules(page)).find((r) => r.id === oldId)?.state).toBe('active')
})

test('an id an agent made up is a row of the one table with its description; its drawer lists its findings by run (CF-85, CF-86)', async ({ page }) => {
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  const run = await runJob(page, 'nightly-audit')
  const found = (await rulesPage(page)).found.find((f) => f.id === SKEW)
  expect(found, 'the fake audit names other.node-clock-skew').toBeTruthy()
  await page.goto('/#/cases/jobs/rules')
  await page.getByTestId('rules-show-found').click()
  const row = page.locator(`[data-testid="rule-row"][data-kind="found"][data-id="${SKEW}"]`)
  await expect(row.getByTestId('rule-state')).toHaveText('unlisted')
  await expect(row.getByTestId('rule-description')).toContainText('not in step')
  // A cluster by its name, its id in the hover, as cases name it.
  await expect(row.locator('[data-testid="rule-cluster"][title="local"]')).toHaveCount(1)
  // A count links into the drawer, at its list.
  await row.getByTestId('rule-findings').click()
  await expect(page).toHaveURL(new RegExp(`#/cases/jobs/rules/${SKEW.replace('.', '\\.')}/findings$`))
  const d = page.getByTestId('rule-drawer')
  await expect(d.getByTestId('rule-descriptions')).toContainText('No rule given covers time keeping')
  await expect(d.getByTestId('rule-findings-list')).toHaveAttribute('data-at', 'true')
  const latest = d.locator(`[data-testid="rule-finding"][data-run="${run}"]`)
  await expect(latest.getByTestId('rule-finding-routing')).toHaveAttribute('data-routing', 'ignored')
  await expect(d.locator(`[data-testid="rule-run"]`, { hasText: run })).toHaveCount(1)
  await latest.getByTestId('rule-finding-run').click()
  await expect(page).toHaveURL(new RegExp(`#/cases/jobs/runs/${run}$`))
})

test('the housekeeping suggests the ids alike as one rule: a row under Suggested that says why, and a row of the SuperAdmin’s Inbox (CF-85, CF-87)', async ({ page }) => {
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  // Found in three runs, an id is a candidate.
  for (let i = 0; i < 3 && ((await rulesPage(page)).found.find((f) => f.id === SKEW)?.runs ?? 0) < 3; i++) await runJob(page, 'nightly-audit')
  await runJob(page, 'housekeeping')
  const s = (await rulesPage(page)).suggestions.find((x) => x.from.includes(SKEW))
  expect(s, JSON.stringify((await rulesPage(page)).suggestions)).toBeTruthy()
  expect(s!.from).toEqual(expect.arrayContaining([SKEW, DRIFT]))
  expect(s!.why).toMatch(/^Found \d+ times in \d+ runs on \d+ clusters? by Nightly audit; grouped with other\.node-clock-\w+ because their ids are alike in words/)

  await page.goto('/#/cases/jobs/rules')
  await page.getByTestId('rules-show-suggested').click()
  const row = page.locator(`[data-testid="rule-row"][data-kind="suggestion"][data-id="${s!.id}"]`)
  await expect(row.getByTestId('rule-state')).toHaveText('suggested')
  await expect(row.getByTestId('rule-origin')).toHaveText('Housekeeping')
  await row.click()
  const d = page.getByTestId('rule-drawer')
  await expect(d.getByTestId('suggestion-why')).toContainText('grouped with')
  await expect(d.getByTestId('suggestion-from-id')).toHaveCount(2)
  await expect(d.getByTestId('suggestion-promote')).toBeVisible()
  // A found id it groups says so.
  await page.goto('/#/cases/jobs/rules')
  await page.getByTestId('rules-show-found').click()
  await expect(page.locator(`[data-testid="rule-row"][data-kind="found"][data-id="${SKEW}"]`).getByTestId('rule-in-suggestion')).toContainText(s!.id)

  // The SuperAdmin's Inbox lists it, counted: the bell is not plain.
  await page.goto('/#/cases/inbox')
  const inboxRow = page.locator(`[data-inbox-row="rule:${s!.id}"]`)
  await expect(inboxRow.getByTestId('inbox-row-status')).toHaveText('Suggested rule')
  await expect(page.getByTestId('inbox-bell')).toHaveAttribute('data-state', /^(p01|waiting)$/)
  await inboxRow.click()
  await expect(page).toHaveURL(new RegExp(`#/cases/inbox/rule/${s!.id.replace(/\./g, '\\.')}`))
  const drawer = page.getByTestId('rule-drawer')
  await expect(drawer.getByTestId('suggestion-why')).toBeVisible()
  // Its link opens it in Jobs › Validation rules (CF-89).
  const link = drawer.getByTestId('suggestion-rules-link')
  await expect(link).toHaveAttribute('href', `#/cases/jobs/rules/${s!.id}`)
  await link.click()
  await expect(page).toHaveURL(new RegExp(`#/cases/jobs/rules/${s!.id.replace(/\./g, '\\.')}$`))
  await expect(page.getByTestId('jobs-sub-rules')).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByTestId('rules-page')).toBeVisible()
  await expect(page.getByTestId('rule-drawer').getByTestId('suggestion-why')).toBeVisible()
})

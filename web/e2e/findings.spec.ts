// Agent-found cases (Phase 12, wave 6; FX-13, JR-01 to JR-04): the nightly
// audit runs against the simulator's fake Claude API, and each of its
// findings becomes a case or joins one. The run says what its findings
// did, each linked to its case; the next run of the same problem joins
// the open case (seen ×N) instead of opening another. The case is closed
// as an exception, with a reason: the next run leaves the finding out and
// the exception counts the match, until the exception is removed. A case
// closed as a known issue is checked quietly: the next run records the
// problem on the known issue. Runs shows when each started and ended and
// its health. Jobs has Schedule and Runs, and an old link to a run opens
// it under Runs.
import type { Page } from '@playwright/test'
import { expect, test } from './test'

test.describe.configure({ mode: 'serial' })

const SAME = { 'Sec-Fetch-Site': 'same-origin' }
const KEY = 'sk-ant-e2e-fake-key-0123456789'
const ON = { clusters: { local: true }, model: 'claude-opus-5-5', dailyUSD: 20, monthlyUSD: 200 }

interface Key {
  cluster: string
  object: string
  check: string
}
interface Finding {
  id: string
  routing: string
  key?: Key
  check?: string
  case?: string
  knownIssue?: string
  exception?: string
}
interface Run {
  id: string
  status: string
  note?: string
  findings?: Finding[]
  routed?: Record<string, number>
}
interface Case {
  id: string
  number: number
  status: string
  outcome?: string
  agentFound: boolean
  seen?: { count: number }
  exception?: string
  knownIssue?: string
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

/** Runs the nightly audit now and waits for it to end. */
async function audit(page: Page): Promise<Run> {
  // The last run's turn is let go a moment after it is kept as ended.
  let started = await api(page, 'POST', '/api/v1/jobs/nightly-audit/run', {})
  for (let i = 0; i < 20 && started.status === 409; i++) {
    await page.waitForTimeout(250)
    started = await api(page, 'POST', '/api/v1/jobs/nightly-audit/run', {})
  }
  expect(started.status, JSON.stringify(started.json)).toBe(202)
  const id = (started.json as { id: string }).id
  let run: Run = { id, status: 'running' }
  await expect
    .poll(
      async () => {
        run = (await api(page, 'GET', `/api/v1/runs/${id}`)).json as Run
        return run.status
      },
      { timeout: 30_000 },
    )
    .not.toBe('running')
  return run
}

/** The audit's finding that went to a case (the fake audit finds one problem). */
function routedToCase(run: Run): Finding {
  const f = (run.findings ?? []).find((x) => !!x.case)
  expect(f, JSON.stringify(run.findings)).toBeTruthy()
  return f!
}

const sameKey = (a?: Key, b?: Key) => !!a && !!b && a.cluster === b.cluster && a.object === b.object && a.check === b.check

const getCase = async (page: Page, id: string) => (await api(page, 'GET', `/api/v1/cases/cases/${id}`)).json as Case

test.beforeAll(async ({ browser }) => {
  const page = await browser.newPage()
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  expect((await api(page, 'PUT', '/api/v1/sherlock/key', { key: KEY })).status).toBe(200)
  expect((await api(page, 'PUT', '/api/v1/sherlock/config', ON)).status).toBe(200)
  // The audit covers the simulated cluster alone, whatever others a spec connected.
  expect((await api(page, 'PUT', '/api/v1/jobs/nightly-audit', { clusters: ['local'] })).status).toBe(200)
  await page.close()
})

test.afterAll(async ({ browser }) => {
  const page = await browser.newPage()
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  // The fake audit proposes a change each run (CF-26): left waiting, it
  // would raise the gear's flag for the specs after.
  const queued = ((await api(page, 'GET', '/api/v1/cases/proposals?state=queued')).json as { proposals?: { id: string; source: { kind: string; id?: string } }[] } | null)?.proposals ?? []
  for (const c of queued.filter((p) => p.source.kind === 'job')) await api(page, 'POST', `/api/v1/cases/proposals/${c.id}/reject`, { reason: 'e2e: done' })
  await api(page, 'PUT', '/api/v1/jobs/nightly-audit', { clusters: [] })
  await api(page, 'PUT', '/api/v1/sherlock/config', { ...ON, clusters: {} })
  await page.close()
})

test('a run’s finding becomes a case; the next run of the same problem joins it, seen ×N; the run says what each finding did', async ({ page }) => {
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  const first = await audit(page)
  expect(first.status, first.note).toBe('ok')
  const f = routedToCase(first)
  expect(['opened', 'seen', 'reopened', 'regression']).toContain(f.routing)
  const before = await getCase(page, f.case!)
  expect(before.agentFound).toBe(true)

  // The run opens under Jobs › Runs: what its findings did, each linked to its case; no finding buttons.
  await page.goto(`/#/cases/jobs/runs/${first.id}`)
  const detail = page.getByTestId('run-detail')
  await expect(detail).toBeVisible()
  await expect(page.getByTestId('jobs-sub-runs')).toHaveAttribute('aria-pressed', 'true')
  await expect(detail.getByTestId('run-routed')).toBeVisible()
  const chip = detail.getByTestId(`finding-${f.id}-routing`)
  await expect(chip).toHaveAttribute('data-routing', f.routing)
  await expect(detail.getByTestId(`finding-${f.id}-ignore`)).toHaveCount(0)
  await expect(detail.getByTestId(`finding-${f.id}-priority`)).toHaveCount(0)

  // The next run sees the same problem: it joins the open case.
  const second = await audit(page)
  const again = (second.findings ?? []).find((x) => sameKey(x.key, f.key))
  expect(again, JSON.stringify(second.findings)).toBeTruthy()
  expect(again!.routing).toBe('seen')
  expect(again!.case).toBe(f.case)
  const after = await getCase(page, f.case!)
  expect(after.seen?.count ?? 0).toBeGreaterThan(before.seen?.count ?? 0)

  // The case's link opens its drawer in the Inbox, with the check and how often it was seen.
  await page.goto(`/#/cases/jobs/runs/${second.id}`)
  await page.getByTestId(`finding-${again!.id}-routing-case`).click()
  await expect(page).toHaveURL(new RegExp(`#/cases/inbox/case/${after.number}$`))
  const drawer = page.getByTestId('case-drawer')
  await expect(drawer.getByTestId('case-source-kind')).toHaveText('Agent')
  await expect(drawer.getByTestId('case-key')).toContainText(f.key?.check ?? '')
  await expect(drawer.getByTestId('case-seen')).toContainText(`×${after.seen?.count}`)
  // Agents never set P0; below P1 an agent-found case has no SLA.
  const p = (await drawer.getByTestId('case-priority-set').inputValue()) || ''
  if (Number(p) > 1) await expect(drawer.getByTestId('case-no-sla')).toBeVisible()
  // The Inbox lists it under the agents' rows.
  await page.goto('/#/cases/inbox?source=agents')
  await expect(page.locator(`[data-inbox-row="case:${after.number}"]`)).toBeVisible()
})

test('closed as an exception with a reason: the next run leaves the finding out and the exception counts it, until it is removed', async ({ page }) => {
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  const run = await audit(page)
  const f = routedToCase(run)
  const c = await getCase(page, f.case!)

  await page.goto(`/#/cases/inbox/case/${c.number}`)
  const drawer = page.getByTestId('case-drawer')
  // An agent-found case is never "not a problem": it is an exception. It may be a duplicate (CF-42).
  await drawer.getByTestId('case-close-open').click()
  await expect(drawer.getByTestId('case-outcome-notAProblem')).toHaveCount(0)
  await expect(drawer.getByTestId('case-outcome-duplicate')).toBeVisible()
  await drawer.getByTestId('case-outcome-exception').check()
  await expect(drawer.getByTestId('case-exception-save')).toBeDisabled()
  await drawer.getByTestId('case-exception-reason').fill('The analytics namespace sits behind its own firewall.')
  await drawer.getByTestId('case-exception-save').click()
  await expect(drawer.getByTestId('case-status')).toHaveText('Closed: exception')
  const link = drawer.getByTestId('case-closed-exception')
  await expect(link).toHaveAttribute('href', /^#\/cases\/mycroft\/exceptions\/EX-\d+$/)
  const exId = ((await link.textContent()) ?? '').trim()

  // The next run leaves it out, and the exception counts the match.
  const next = await audit(page)
  const left = (next.findings ?? []).find((x) => sameKey(x.key, f.key))
  expect(left, JSON.stringify(next.findings)).toBeTruthy()
  expect(left!.routing).toBe('excepted')
  expect(left!.exception).toBe(exId)
  expect(next.routed?.excepted ?? 0).toBeGreaterThan(0)
  const exception = (await api(page, 'GET', `/api/v1/cases/exceptions/${exId}`)).json as { reason?: string; matches?: number; lastMatched?: string }
  expect(exception.reason).toBe('The analytics namespace sits behind its own firewall.')
  expect(exception.matches ?? 0).toBeGreaterThan(0)
  expect(exception.lastMatched).toBeTruthy()

  // Mycroft › Exceptions shows how often it matched.
  await page.goto(`/#/cases/mycroft/exceptions/${exId}`)
  const card = page.locator(`[data-testid="exception"][data-id="${exId}"]`)
  await expect(card.getByTestId('exception-matches')).toContainText(/\d+ times?/)

  // Removed, it stops applying.
  expect((await api(page, 'DELETE', `/api/v1/cases/exceptions/${exId}`)).status).toBeLessThan(300)
  const third = await audit(page)
  const back = (third.findings ?? []).find((x) => sameKey(x.key, f.key))
  expect(back, JSON.stringify(third.findings)).toBeTruthy()
  expect(back!.routing).not.toBe('excepted')
})

test('closed as a known issue made from it: the next run records the problem on the known issue, still present', async ({ page }) => {
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  const run = await audit(page)
  const f = routedToCase(run)
  const c = await getCase(page, f.case!)
  await page.goto(`/#/cases/inbox/case/${c.number}`)
  const drawer = page.getByTestId('case-drawer')
  await drawer.getByTestId('case-close-open').click()
  await drawer.getByTestId('case-outcome-knownIssue').check()
  const title = `No network policy (e2e ${Date.now()})`
  await drawer.getByTestId('case-known-title').fill(title)
  await drawer.getByTestId('case-known-workaround').fill('The namespace is behind its own firewall.')
  await drawer.getByTestId('case-known-create').click()
  await expect(drawer.getByTestId('case-status')).toHaveText('Closed: known issue')
  const kiId = ((await drawer.getByTestId('case-closed-known').textContent()) ?? '').trim()
  expect(kiId).toMatch(/^KI-\d+$/)

  const next = await audit(page)
  const quiet = (next.findings ?? []).find((x) => sameKey(x.key, f.key))
  expect(quiet, JSON.stringify(next.findings)).toBeTruthy()
  expect(quiet!.routing).toBe('known')
  expect(quiet!.knownIssue).toBe(kiId)
  const known = (await api(page, 'GET', `/api/issues/v1/known/${kiId}`)).json as { title?: string; stillPresent?: { count: number }; cases?: string[] }
  expect(known.title).toBe(title)
  expect(known.stillPresent?.count ?? 0).toBeGreaterThan(0)
  expect(known.cases ?? []).toContain(c.id)
  // Mycroft › Known issues shows it still seen, and the case closed as it.
  await page.goto(`/#/cases/mycroft/known/${kiId}`)
  await expect(page.getByTestId('known-still-present')).toContainText('×')
  await expect(page.getByTestId('known-linked')).toContainText(c.id)
})

test('a run reads as what it found: Started, Ended and Health in Runs, and the status’s tone (JR-01, JR-03, JR-04)', async ({ page }) => {
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  const run = await audit(page)
  await page.goto('/#/cases/jobs/runs')
  const table = page.getByTestId('runs-table')
  await expect(table.locator('thead')).toContainText('Ended')
  await expect(table.locator('thead')).toContainText('Health')
  const row = page.getByTestId('run-row').first()
  await expect(row.getByTestId('run-health-cell')).toBeVisible()
  const health = await row.getByTestId('run-health-cell').getAttribute('data-health')
  expect(['critical', 'warning', 'info', 'none']).toContain(health)
  if (health === 'warning') await expect(row.getByTestId('run-status-cell')).toHaveText(/^Done · \d+ warnings?$/)
  // Schedule and Runs are as wide as each other.
  const runsWidth = (await page.getByTestId('runs-tab').boundingBox())!.width
  await page.getByTestId('jobs-sub-schedule').click()
  const jobsWidth = (await page.getByTestId('jobs-tab').boundingBox())!.width
  expect(Math.abs(runsWidth - jobsWidth)).toBeLessThan(2)
  expect(run.id).toMatch(/^run-/)
})

test('Jobs has Schedule and Runs, and an old link to a run opens it under Runs (FX-13)', async ({ page }) => {
  await page.goto('/#/cases/jobs')
  await expect(page.getByTestId('jobs-tab')).toBeVisible()
  await expect(page.getByTestId('jobs-sub-schedule')).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByTestId('cases-nav').locator('[data-cases-tab]').first()).toBeVisible()
  expect((await page.getByTestId('cases-nav').locator('[data-cases-tab]').allTextContents()).some((t) => /^Runs/.test(t))).toBe(false)
  await page.getByTestId('jobs-sub-runs').click()
  await expect(page).toHaveURL(/#\/cases\/jobs\/runs$/)
  const row = page.getByTestId('run-row').first()
  await expect(row).toBeVisible()
  await row.click()
  await expect(page).toHaveURL(/#\/cases\/jobs\/runs\/run-\d+$/)
  const id = page.url().split('/').pop() ?? ''
  await expect(page.getByTestId('run-drawer')).toBeVisible()
  // Closed, the drawer goes back to the Runs.
  await page.keyboard.press('Escape')
  await expect(page).toHaveURL(/#\/cases\/jobs\/runs$/)
  await page.getByTestId('jobs-sub-schedule').click()
  await expect(page).toHaveURL(/#\/cases\/jobs$/)
  await expect(page.getByTestId('job-row').first()).toBeVisible()
  // A link from before Jobs had sub-tabs.
  await page.goto(`/#/cases/runs/${id}`)
  await expect(page.getByTestId('run-drawer')).toBeVisible()
  await expect(page.getByTestId('jobs-sub-runs')).toHaveAttribute('aria-pressed', 'true')
})

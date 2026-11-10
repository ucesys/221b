// Cases (Phase 12, CW-01 to CW-14; wave 5, CU-01 to CU-14; wave 6, one
// flow for findings and cases): one Case for everything, whatever opened
// it, New → In progress → Closed with one outcome. A case opened by hand
// with New Case about any words, its drawer, its priority P0 to P4, Save
// Draft, Fix with an RCA (required at P1), Reopen with a reason and Close
// as no action needed (CF-90); a person's report, which is a case too, triaged in
// the same drawer with a reply and a note, and closed as a known issue
// made from it; duplicates, whose original is checked, with what fits
// instead when it is closed; New Case offering to add to a case like it;
// the old addresses; Settings › SLA and Cases; and, with the case analysis
// job on, the Troubleshooter's analysis with the fix it suggests, Open in
// Watson, the change it proposes, and the RCA it drafts. Since wave 6e the
// RCA is part of the case: its fix is what was done (no second field), and
// analysing a case leaves its status as it was.
import type { Page } from '@playwright/test'
import { expect, test } from './test'

test.describe.configure({ mode: 'serial' })

const SAME = { 'Sec-Fetch-Site': 'same-origin' }
const KEY = 'sk-ant-e2e-fake-key-0123456789'
const ON = { clusters: { local: true }, model: 'claude-opus-5-5', dailyUSD: 20, monthlyUSD: 200 }

async function call(page: Page, path: string, body: unknown, method = 'PUT'): Promise<number> {
  return page.evaluate(
    async ([p, b, m]) => (await fetch(p as string, { method: m as string, headers: { 'Content-Type': 'application/json' }, body: b === null ? undefined : JSON.stringify(b) })).status,
    [path, body, method] as const,
  )
}

/** Files a report through the issue API, for someone else, as support does: a case. */
async function file(page: Page, text: string, forUser = 'dev'): Promise<number> {
  const res = await page.request.post('/api/issues/v1/reports', { headers: SAME, data: { text, for: { id: forUser, name: 'Dev Person' } } })
  expect(res.status(), await res.text()).toBe(201)
  return ((await res.json()) as { report: { number: number } }).report.number
}

/** Opens a case by hand through the API. */
async function open(page: Page, title: string, more: Record<string, unknown> = {}): Promise<{ id: string; number: number }> {
  const res = await page.request.post('/api/v1/cases/cases', { headers: SAME, data: { title, priority: 3, ...more } })
  expect(res.status(), await res.text()).toBe(201)
  return (await res.json()) as { id: string; number: number }
}

test.afterAll(async ({ browser }) => {
  // The job paused, Sherlock off, the cards rejected and the cases closed
  // again, so the other specs see the console as before: a card waiting
  // turns the gear's flag towards the Inbox.
  const page = await browser.newPage()
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  await call(page, '/api/v1/jobs/case-analysis', { on: false })
  await call(page, '/api/v1/sherlock/config', { ...ON, clusters: {} })
  const cards = (await (await page.request.get('/api/v1/cases/proposals?state=queued')).json()) as { proposals: { id: string; source: { kind: string } }[] }
  for (const c of cards.proposals.filter((p) => p.source.kind === 'case')) await call(page, `/api/v1/cases/proposals/${c.id}/reject`, { reason: 'e2e: done' }, 'POST')
  for (const status of ['new', 'inProgress']) {
    const list = (await (await page.request.get(`/api/v1/cases/cases?status=${status}`)).json()) as { cases: { id: string }[] }
    for (const c of list.cases) await call(page, `/api/v1/cases/cases/${c.id}/close`, { outcome: 'noAction', explanation: 'e2e: done' }, 'POST')
  }
  await page.close()
})

test('a case opened by hand about any words: New Case, its drawer, priority P0 to P4, its title in place, Save of the shared draft, Fix with an RCA at P1, Reopen with a reason, Close as no action needed (CU-02, CU-03, CU-09, CF-58 to CF-60, CF-90)', async ({ page }) => {
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  expect(await call(page, '/api/v1/jobs/case-analysis', { on: false })).toBe(200)
  const title = `The disk of db-1 is 90% full ${Date.now()}`

  await page.goto('/#/cases/inbox')
  await page.getByTestId('cases-new-case').click()
  await expect(page).toHaveURL(/#\/cases\/inbox\/case\/new$/)
  await page.getByTestId('case-new-title').fill(title)
  await page.getByTestId('case-new-detail').fill('Since 10:00, /var grows by 1 GB an hour.')
  // Not a Kubernetes object (the owner's item 1): any words are fine.
  await page.getByTestId('case-new-resource').fill('the disk of db-1, /var at 90%')
  await page.getByTestId('case-new-priority').selectOption('3')
  // What is like it shows beside the form; opening a case anyway is always there.
  await expect(page.getByTestId('case-new-similar')).toBeVisible()
  await expect(page.getByTestId('case-new-open')).toBeEnabled()
  await page.getByTestId('case-new-open').click()

  const drawer = page.getByTestId('case-drawer')
  await expect(drawer).toBeVisible()
  await expect(page).toHaveURL(/#\/cases\/inbox\/case\/\d+$/)
  const number = Number(new URL(page.url()).hash.split('/').pop())
  await expect(drawer.getByTestId('case-kicker')).toContainText(`Case C-${number}`)
  await expect(drawer.getByTestId('case-status')).toHaveText(/^(New|In progress)$/)
  await expect(drawer.getByTestId('case-priority-set')).toHaveValue('3')
  await expect(drawer.getByTestId('case-source-kind')).toHaveText('Manual')
  await expect(drawer.getByTestId('case-detail')).toHaveText('Since 10:00, /var grows by 1 GB an hour.')
  await expect(drawer.getByTestId('case-object')).toHaveText('the disk of db-1, /var at 90%')
  // A person's case: the SLA applies, and there is no exception to offer.
  await expect(drawer.getByTestId('case-no-sla')).toHaveCount(0)
  await drawer.getByTestId('case-close-open').click()
  await expect(drawer.getByTestId('case-outcome-fix')).toBeVisible()
  await expect(drawer.getByTestId('case-outcome-exception')).toHaveCount(0)
  await drawer.getByRole('button', { name: 'Back' }).click()

  // The priority, P0 to P4, from the header (CF-58).
  await drawer.getByTestId('case-priority-set').selectOption('1')
  await expect(drawer.getByTestId('case-priority-set')).toHaveValue('1')
  expect(((await (await page.request.get(`/api/v1/cases/cases/C-${number}`)).json()) as { priority: number }).priority).toBe(1)

  // The title, edited in place: Escape leaves it and the drawer as they were, Enter saves (CF-60).
  await drawer.getByTestId('case-title-edit').click()
  await drawer.getByTestId('case-title-input').fill('Never saved')
  await drawer.getByTestId('case-title-input').press('Escape')
  await expect(drawer).toBeVisible()
  await expect(drawer.getByTestId('case-title-value')).toHaveText(title)
  await drawer.getByTestId('case-title-edit').click()
  await drawer.getByTestId('case-title-input').fill(`${title} (db-1)`)
  await drawer.getByTestId('case-title-input').press('Enter')
  await expect(drawer.getByTestId('case-title-value')).toHaveText(`${title} (db-1)`)

  // Save: the case's one draft, for everyone who works it, comes back after the drawer opens again, on the outcome picked (CF-59).
  await drawer.getByTestId('case-close-open').click()
  await drawer.getByTestId('case-outcome-fix').check()
  await drawer.getByTestId('case-fix-text').fill('Cleaned /var/tmp')
  await expect(drawer.getByTestId('case-keep')).toHaveText('Save')
  await drawer.getByTestId('case-keep').click()
  await expect(drawer.getByTestId('case-draft-at')).toContainText('Draft last edited by')
  // A save from a draft someone changed since is refused with theirs: 409.
  const stale = await page.request.put(`/api/v1/cases/cases/C-${number}/draft`, { headers: SAME, data: { fix: 'Someone else', base: '2020-01-01T00:00:00Z' } })
  expect(stale.status(), await stale.text()).toBe(409)
  expect(((await stale.json()) as { draft: { fix?: string } }).draft.fix).toBe('Cleaned /var/tmp')
  await page.reload()
  await expect(page.getByTestId('case-drawer')).toBeVisible()
  await page.getByTestId('case-close-open').click()
  await expect(page.getByTestId('case-outcome-fix')).toBeChecked()
  await expect(page.getByTestId('case-fix-text')).toHaveValue('Cleaned /var/tmp')

  // Fix at P1: the RCA is required; the case closes as fixed. What was done is the RCA's fix: no field asks for it twice.
  await page.getByTestId('case-fix-text').fill('Cleaned /var/tmp and rotated the logs.')
  await expect(drawer.getByTestId('case-rca-required')).toBeVisible()
  await expect(drawer.getByTestId('case-rca-fix')).toHaveCount(0)
  await drawer.getByTestId('case-rca-cause').fill('')
  await expect(drawer.getByTestId('case-fix-save')).toBeDisabled()
  await drawer.getByTestId('case-rca-cause').fill('Log rotation was off on db-1.')
  await drawer.getByTestId('case-rca-prevention').fill('Alert at 80% of the disk.')
  await drawer.getByTestId('case-fix-save').click()
  await expect(drawer.getByTestId('case-status')).toHaveText('Closed: fixed')
  await expect(drawer.getByTestId('case-rca')).toContainText('Log rotation was off on db-1.')
  // The fix shows once, as Fix applied above; the RCA stays on the case, with no id and no Mycroft link.
  await expect(drawer.getByTestId('case-fix-applied')).toContainText('Cleaned /var/tmp and rotated the logs.')
  await expect(drawer.getByTestId('case-rca-fix-applied')).toHaveCount(0)
  await expect(drawer.getByTestId('case-rca')).not.toContainText('Mycroft')
  const fixed = (await (await page.request.get(`/api/v1/cases/cases/C-${number}`)).json()) as { rca?: { id?: string; fix: string } }
  expect(fixed.rca).toMatchObject({ fix: 'Cleaned /var/tmp and rotated the logs.' })
  expect(fixed.rca?.id).toBeUndefined()

  // Reopen, with a reason, which is required.
  await drawer.getByTestId('case-reopen').click()
  await expect(drawer.getByTestId('case-reopen-save')).toBeDisabled()
  await drawer.getByTestId('case-reopen-reason').fill('The disk filled again overnight.')
  await drawer.getByTestId('case-reopen-save').click()
  await expect(drawer.getByTestId('case-status')).toHaveText(/^(New|In progress)$/)

  // Close as no action needed: the explanation is required. Not a
  // problem and No fix needed are no longer listed (CF-90).
  await drawer.getByTestId('case-close-open').click()
  await expect(drawer.getByTestId('case-outcome-notAProblem')).toHaveCount(0)
  await expect(drawer.getByTestId('case-outcome-noFixNeeded')).toHaveCount(0)
  await drawer.getByTestId('case-outcome-noAction').check()
  await expect(drawer.getByTestId('case-close-save')).toBeDisabled()
  await drawer.getByTestId('case-close-reason').fill('The disk was resized meanwhile.')
  await drawer.getByTestId('case-close-save').click()
  await expect(drawer.getByTestId('case-status')).toHaveText('Closed: no action needed')
  await expect(drawer.getByTestId('case-reason')).toHaveText('The disk was resized meanwhile.')

  // The API lists it among every case, with its kind.
  const all = (await (await page.request.get('/api/v1/cases/cases?q=' + encodeURIComponent('db-1'))).json()) as { cases: { number: number; pending: boolean; source: { kind: string } }[] }
  expect(all.cases.find((c) => c.number === number)).toMatchObject({ pending: false, source: { kind: 'manual' } })
})

test('a report is a case: triaged in the same drawer with an assignee, a reply and a note, and closed as a known issue made from it (CU-01, CU-09, CU-11)', async ({ page }) => {
  const stamp = Date.now()
  const n = await file(page, `Checkout hangs after I press Pay ${stamp}`)
  // Its old address opens the case it is.
  await page.goto(`/#/cases/reports/${n}`)
  const drawer = page.getByTestId('case-drawer')
  await expect(drawer).toBeVisible()
  await expect(drawer.getByTestId('case-kicker')).toContainText(`Case C-${n} · reported by Dev Person, filed by`)
  await expect(drawer.getByTestId('case-source-kind')).toHaveText('Report')
  await expect(drawer.getByTestId('case-detail')).toContainText('Checkout hangs')

  // Assigned to me, P2.
  await drawer.getByTestId('case-assignee-set').selectOption('me')
  await expect(drawer.getByTestId('case-assignee')).not.toHaveText('nobody')
  await drawer.getByTestId('case-priority-set').selectOption('2')
  await expect(drawer.getByTestId('case-priority-set')).toHaveValue('2')

  // A reply the reporter sees, and a note they do not.
  await drawer.getByTestId('case-reply').fill('We are looking at checkout now.')
  await drawer.getByTestId('case-send').click()
  await expect(drawer.getByTestId('case-message').filter({ hasText: 'We are looking at checkout now.' })).toBeVisible()
  await drawer.getByTestId('case-reply').fill('Looks like the payment gateway.')
  await drawer.getByTestId('case-note-add').click()
  await expect(drawer.getByTestId('case-note').filter({ hasText: 'payment gateway' })).toBeVisible()

  // The issue API (v1) keeps its shape: the report is the case, P2; priorities run 0 to 4.
  const asApi = (await (await page.request.get(`/api/issues/v1/reports/${n}`)).json()) as { priority: number; owner?: { id: string }; title: string }
  expect(asApi.priority).toBe(2)
  expect(asApi.owner?.id).toBeTruthy()
  expect(asApi.title).toContain('Checkout hangs')
  const p0 = await page.request.patch(`/api/issues/v1/reports/${n}`, { headers: SAME, data: { priority: 0 } })
  expect(p0.status(), await p0.text()).toBe(200)
  await page.reload()
  await expect(page.getByTestId('case-priority-set')).toHaveValue('0')

  // Known issue: made from the case, which closes as it.
  await page.getByTestId('case-close-open').click()
  await page.getByTestId('case-outcome-knownIssue').check()
  await page.getByTestId('case-known-title').fill(`Checkout hangs ${stamp}`)
  await page.getByTestId('case-known-workaround').fill('Payments are not taken twice; try again in a few minutes.')
  await page.getByTestId('case-known-create').click()
  await expect(page.getByTestId('case-status')).toHaveText('Closed: known issue')
  await expect(page.getByTestId('case-closed-known')).toHaveText(/^KI-\d+$/)
  // The reporter reads it as a known problem.
  const mine = (await (await page.request.get(`/api/issues/v1/reports/${n}`)).json()) as { status: string; outcome?: string }
  expect(mine).toMatchObject({ status: 'closed', outcome: 'knownIssue' })
})

test('a reply moves the case: a note leaves it New, Ask for info shows it Waiting for the reporter, kept In progress, in the drawer and the Inbox (CF-67)', async ({ page }) => {
  const stamp = Date.now()
  const n = await file(page, `The shop shows an error ${stamp}`)
  await page.goto(`/#/cases/inbox/case/${n}`)
  const drawer = page.getByTestId('case-drawer')
  await expect(drawer.getByTestId('case-status')).toHaveText('New')

  // An internal note changes nothing.
  await drawer.getByTestId('case-reply').fill('Probably the payment gateway.')
  await drawer.getByTestId('case-note-add').click()
  await expect(drawer.getByTestId('case-note').filter({ hasText: 'payment gateway' })).toBeVisible()
  await expect(drawer.getByTestId('case-status')).toHaveText('New')

  // Ask for info: shown Waiting for the reporter, kept In progress.
  await drawer.getByTestId('case-reply').fill('What app is it?')
  await drawer.getByTestId('case-ask').click()
  await expect(drawer.getByTestId('case-message').filter({ hasText: 'What app is it?' })).toBeVisible()
  await expect(drawer.getByTestId('case-status')).toHaveText('Waiting for the reporter')
  await expect(drawer.getByTestId('case-history-entry').first()).toContainText('Question sent, now Waiting for the reporter')
  const asApi = (await (await page.request.get(`/api/v1/cases/cases/C-${n}`)).json()) as { status: string; askedReporter?: boolean }
  expect(asApi).toMatchObject({ status: 'inProgress', askedReporter: true })

  // The Inbox shows it so, and its Status filter finds it.
  await page.goto('/#/cases/inbox?status=waiting')
  const row = page.locator(`[data-inbox-row="case:${n}"]`)
  await expect(row.getByTestId('inbox-row-status')).toHaveText('Waiting for the reporter')
  await expect(page.getByTestId('inbox-status')).toHaveValue('waiting')
  await page.getByTestId('inbox-status').selectOption('new')
  await expect(page).toHaveURL(/status=new/)
  await expect(row).toHaveCount(0)

  // A plain reply to another report moves it In progress.
  const m = await file(page, `Checkout is odd ${stamp}`)
  await page.goto(`/#/cases/inbox/case/${m}`)
  await drawer.getByTestId('case-reply').fill('We are looking at it.')
  await drawer.getByTestId('case-send').click()
  await expect(drawer.getByTestId('case-status')).toHaveText('In progress')
  await expect(drawer.getByTestId('case-history-entry').first()).toContainText('Reply sent, now In progress')
})

test('a duplicate names its original, which is checked; a closed original offers what fits instead', async ({ page }) => {
  const stamp = Date.now()
  const original = await open(page, `Grafana is down ${stamp}`)
  const dup = await open(page, `Grafana does not load ${stamp}`)
  await page.goto(`/#/cases/inbox/case/${dup.number}`)
  const drawer = page.getByTestId('case-drawer')
  await drawer.getByTestId('case-close-open').click()
  await drawer.getByTestId('case-outcome-duplicate').check()
  await drawer.getByTestId('case-close-original').fill(original.id)
  await expect(drawer.getByTestId('case-duplicate-ok')).toContainText(original.id)
  await drawer.getByTestId('case-close-reason').fill('The same outage.')
  await drawer.getByTestId('case-close-save').click()
  await expect(drawer.getByTestId('case-status')).toHaveText('Closed: duplicate')
  await expect(drawer.getByTestId('case-duplicate-of')).toContainText(original.id)

  // An original closed as no action needed: the case may not close as its
  // duplicate. noFixNeeded, its name before CF-90, is still taken as it.
  const gone = await open(page, `Backups slow ${stamp}`)
  expect(await call(page, `/api/v1/cases/cases/${gone.id}/close`, { outcome: 'noFixNeeded', explanation: 'A one-off during the upgrade.' }, 'POST')).toBe(200)
  expect(((await (await page.request.get(`/api/v1/cases/cases/${gone.id}`)).json()) as { outcome: string }).outcome).toBe('noAction')
  const later = await open(page, `Backups slow again ${stamp}`)
  await page.goto(`/#/cases/inbox/case/${later.number}`)
  await drawer.getByTestId('case-close-open').click()
  await drawer.getByTestId('case-outcome-duplicate').check()
  await drawer.getByTestId('case-close-original').fill(gone.id)
  await expect(drawer.getByTestId('case-duplicate-refused')).toBeVisible()
  await drawer.getByTestId('case-close-reason').fill('Same.')
  await expect(drawer.getByTestId('case-close-save')).toBeDisabled()
  await expect(drawer.getByTestId('case-offer-reopenOriginal')).toHaveText(`Reopen ${gone.id}`)
  await drawer.getByTestId('case-offer-sameAs').click()
  await expect(drawer.getByTestId('case-same-as')).toContainText(gone.id)
  await expect(drawer.getByTestId('case-status')).toHaveText(/^(New|In progress)$/)
})

test('New Case offers to add to an open case like it, instead of opening another', async ({ page }) => {
  const title = `Harbor pushes fail with 413 ${Date.now()}`
  const there = await open(page, title, { resource: 'registry/Deployment/harbor' })
  await page.goto('/#/cases/inbox/case/new')
  await page.getByTestId('case-new-title').fill(title)
  const join = page.getByTestId(`similar-join-${there.id}`)
  await expect(join).toBeVisible({ timeout: 10_000 })
  await expect(page.getByTestId('case-new-open')).toHaveText('Open a new case anyway')
  await page.getByTestId('case-new-detail').fill('From the CI runners too.')
  await join.click()
  await expect(page).toHaveURL(new RegExp(`#/cases/inbox/case/${there.number}$`))
  await expect(page.getByTestId('case-drawer')).toContainText('From the CI runners too.')
})

test('the old addresses open the cases: AIOps › Cases, Reports, and On behalf of from the Inbox', async ({ page }) => {
  const n = await file(page, `An old address ${Date.now()}`)
  // A case of before's address: with nothing carried over, the same number.
  await page.goto(`/#/cases/cases/${n}`)
  await expect(page.getByTestId('case-drawer')).toBeVisible()
  await expect(page.getByTestId('case-kicker')).toContainText(`Case C-${n}`)
  await page.goto('/#/cases/reports')
  await expect(page.getByTestId('cases-page')).toBeVisible()
  await expect(page.getByTestId('case-drawer')).toHaveCount(0)

  // On behalf of: the report page for the person whose problem it is.
  await page.goto('/#/cases/inbox')
  await page.getByTestId('cases-on-behalf').click()
  await page.getByTestId('cases-on-behalf-user').fill('dev')
  const [behalf] = await Promise.all([page.context().waitForEvent('page'), page.getByTestId('cases-on-behalf-open').click()])
  await behalf.waitForLoadState('domcontentloaded')
  await expect(behalf).toHaveURL(/\/report\?for=dev$/)
  await expect(behalf.getByTestId('for-note')).toContainText('Reporting for dev')
  await behalf.close()
})

test('with the case analysis job on, the Troubleshooter analyses a case, suggests a fix to run in Watson, proposes it, and drafts the RCA (CW-04, CW-06, CW-08)', async ({ page }) => {
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  expect(await call(page, '/api/v1/sherlock/key', { key: KEY })).toBe(200)
  expect(await call(page, '/api/v1/sherlock/config', ON)).toBe(200)
  expect(await call(page, '/api/v1/jobs/case-analysis', { on: true })).toBe(200)
  const res = await page.request.post('/api/v1/cases/cases', { headers: SAME, data: { title: `DNS is slow ${Date.now()}`, cluster: 'local', resource: 'kube-system/Deployment/coredns', priority: 2 } })
  expect(res.status(), await res.text()).toBe(201)
  const made = (await res.json()) as { id: string; number: number; analyzing: boolean }
  expect(made.analyzing).toBe(true)

  await page.goto(`/#/cases/inbox/case/${made.number}`)
  const drawer = page.getByTestId('case-drawer')
  await expect(drawer.getByTestId('case-analysis')).toContainText('The memory limit is too low for the load.', { timeout: 45_000 })
  // The analysis does not take the case in progress: only a person does.
  await expect(drawer.getByTestId('case-status')).toHaveText('New')
  await expect(drawer.getByTestId('case-fix-command')).toHaveText('$ kubectl rollout restart deployment/coredns -n kube-system')
  await expect(drawer.getByTestId('case-card')).toHaveCount(1)
  await expect(drawer.getByTestId('case-card')).toContainText('Waiting for approval')
  await expect(drawer.getByTestId('case-object').getByTestId('object-link')).toBeVisible()

  // The proposed change names the case.
  const href = await drawer.getByTestId('case-card').getByRole('link').getAttribute('href')
  const card = decodeURIComponent((href ?? '').split('proposal=')[1] ?? '')
  const got = (await (await page.request.get(`/api/v1/cases/proposals/${card}`)).json()) as { source: { kind: string; id: string } }
  expect(got.source).toMatchObject({ kind: 'case', id: made.id })

  // The run is the case analysis job's, and links back.
  await expect(drawer.getByTestId('case-analysis').getByRole('link').first()).toHaveAttribute('href', /#\/cases\/jobs\/runs\/run-\d+$/)

  // Fix with an RCA the agent drafts; a person saves it. At P2 the RCA's fields show, optional.
  // The draft fills cause and prevention; What was done, written already, stays as the person wrote it.
  await drawer.getByTestId('case-close-open').click()
  await drawer.getByTestId('case-outcome-fix').check()
  await drawer.getByTestId('case-fix-text').fill('Restarted coredns with more memory.')
  await expect(drawer.getByTestId('case-rca-optional')).toBeVisible()
  await drawer.getByTestId('case-rca-draft').click()
  await expect(drawer.getByTestId('case-rca-cause')).toHaveValue('The pods behind it ran out of memory under load.', { timeout: 45_000 })
  await expect(drawer.getByTestId('case-rca-prevention')).toHaveValue('Alert on memory use above 80% of the limit.')
  await expect(drawer.getByTestId('case-fix-text')).toHaveValue('Restarted coredns with more memory.')
  await drawer.getByTestId('case-fix-save').click()
  await expect(drawer.getByTestId('case-status')).toHaveText('Closed: fixed')
  await expect(drawer.getByTestId('case-rca')).toContainText('ran out of memory')

  // Run in Watson stays, for a person to run the fix as themselves.
  await expect(drawer.getByTestId('case-open-watson')).toBeEnabled()
})

test('Settings › SLA has P0 to P4, and Settings › Cases keeps its values within their ranges', async ({ page }) => {
  await page.goto('/#/admin/settings/sla')
  for (const p of [0, 1, 2, 3, 4]) await expect(page.getByTestId(`sla-P${p}`)).toBeVisible()
  await expect(page.getByTestId('sla-P0')).toContainText('P0: Outage')
  await page.locator('[data-testid="settings-section"][data-section="cases"]').click()
  await expect(page).toHaveURL(/#\/admin\/settings\/cases$/)
  const settings = page.getByTestId('cases-settings')
  await expect(settings.getByTestId('cases-newCasesPerRun')).toBeVisible()
  const was = await settings.getByTestId('cases-newCasesPerRun').inputValue()
  await settings.getByTestId('cases-newCasesPerRun').fill('501')
  await expect(settings.getByTestId('cases-setting-newCasesPerRun')).toContainText('From 1 to 500.')
  await settings.getByTestId('cases-newCasesPerRun').fill('40')
  await page.getByTestId('cases-save').click()
  await expect.poll(async () => ((await (await page.request.get('/api/v1/cases/settings')).json()) as { newCasesPerRun: number }).newCasesPerRun).toBe(40)
  await settings.getByTestId('cases-newCasesPerRun').fill(was)
  await page.getByTestId('cases-save').click()
  await expect.poll(async () => ((await (await page.request.get('/api/v1/cases/settings')).json()) as { newCasesPerRun: number }).newCasesPerRun).toBe(Number(was))
  await expect(settings.getByTestId('cases-duplicates')).toContainText('closed as duplicates')
})

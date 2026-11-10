// Changes that resolve cases (Phase 12, wave 6b, CF-25 to CF-31): the
// nightly audit runs against the simulator's fake Claude API; the change
// it proposes names the finding it fixes, and the console turns that into
// the case the finding went to: the run, the Inbox row and the proposal
// drawer say "Resolves C-n", and the case lists the change. Approved and
// applied, the change puts the case In progress with the fix waiting for
// the next run of the job to check it; the next run, which still sees the
// problem, says the fix did not hold. A person links a change asked for in
// a chat from the case drawer, and unlinks it; an agent's own link stays.
import type { Page } from '@playwright/test'
import { expect, test } from './test'

test.describe.configure({ mode: 'serial' })

const SAME = { 'Sec-Fetch-Site': 'same-origin' }
const KEY = 'sk-ant-e2e-fake-key-0123456789'
const ON = { clusters: { local: true }, model: 'claude-opus-5-5', dailyUSD: 20, monthlyUSD: 200 }

interface ProposalRef {
  id: string
  title: string
  error?: string
  resolves?: string[]
}
interface Run {
  id: string
  status: string
  note?: string
  findings?: { id: string; routing: string; case?: string }[]
  proposals?: ProposalRef[]
}
interface CardRef {
  id: string
  title?: string
  state: string
  linkedBy: string
  appliedAt?: string
}
interface Case {
  id: string
  number: number
  status: string
  verification?: string
  fixApplied?: { text: string; change?: string }
  proposals: CardRef[]
}
interface Card {
  id: string
  title: string
  state: string
  cluster: string
  source: { kind: string }
  resolves?: string[]
  resolvesBy?: Record<string, string>
  results?: { ok: boolean; summary: string }[]
  reason?: string
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

const getCase = async (page: Page, id: string) => (await api(page, 'GET', `/api/v1/cases/cases/${id}`)).json as Case
const getCard = async (page: Page, id: string) => (await api(page, 'GET', `/api/v1/cases/proposals/${id}`)).json as Card

// What the tests share, in order: the case the audit's finding went to,
// the change that resolves it, and the chat's change a person links.
let caseId = ''
let caseNumber = 0
let cardId = ''
let chatId = ''

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
  // The changes made here refused and the case closed again, so the other
  // specs see the console as before: a card waiting turns the gear's flag
  // towards the Inbox, and the next audit opens a case of its own.
  const page = await browser.newPage()
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  const queued = ((await api(page, 'GET', '/api/v1/cases/proposals?state=queued')).json as { proposals?: Card[] } | null)?.proposals ?? []
  for (const c of queued.filter((p) => p.id === chatId || (caseId && p.resolves?.includes(caseId)))) await api(page, 'POST', `/api/v1/cases/proposals/${c.id}/reject`, { reason: 'e2e: done' })
  if (caseId && (await getCase(page, caseId)).status !== 'closed') await api(page, 'POST', `/api/v1/cases/cases/${caseId}/close`, { outcome: 'noAction', explanation: 'e2e: done' })
  await api(page, 'PUT', '/api/v1/jobs/nightly-audit', { clusters: [] })
  await api(page, 'PUT', '/api/v1/sherlock/config', { ...ON, clusters: {} })
  await page.close()
})

test('the audit’s proposed change resolves the case its finding went to: the run, the Inbox row and the drawer say so, and the case lists it (CF-25, CF-26, CF-28)', async ({ page }) => {
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  const run = await audit(page)
  expect(run.status, run.note).toBe('ok')
  const f = (run.findings ?? []).find((x) => !!x.case)
  expect(f, JSON.stringify(run.findings)).toBeTruthy()
  caseId = f!.case!
  // The agent named the finding (its position); the console names the case.
  const p = (run.proposals ?? []).find((x) => !x.error && x.id && x.resolves?.includes(caseId))
  expect(p, JSON.stringify({ proposals: run.proposals, note: run.note })).toBeTruthy()
  cardId = p!.id
  // A proposed change is PC-<n> (CF-56).
  expect(cardId).toMatch(/^PC-\d+$/)
  const c = await getCase(page, caseId)
  caseNumber = c.number
  const ref = c.proposals.find((x) => x.id === cardId)
  expect(ref, JSON.stringify(c.proposals)).toBeTruthy()
  expect(ref!.linkedBy).toBe('agent')
  const card = await getCard(page, cardId)
  expect(card.resolves).toContain(caseId)
  expect(card.resolvesBy?.[caseId]).toBe('agent')

  // The run: what each change resolves, linked to the case.
  await page.goto(`/#/cases/jobs/runs/${run.id}`)
  const detail = page.getByTestId('run-detail')
  const row = detail.getByTestId('run-proposal').filter({ hasText: p!.title }).first()
  await expect(row).toHaveAttribute('data-filed', 'true')
  await expect(row.getByTestId('run-proposal-resolves')).toContainText(caseId)
  await row.getByTestId('run-proposal-resolves-link').filter({ hasText: caseId }).click()
  await expect(page).toHaveURL(new RegExp(`#/cases/inbox/case/${caseNumber}$`))

  // The case lists the change, named by the agent, linked to its drawer.
  const drawer = page.getByTestId('case-drawer')
  const listed = drawer.locator(`[data-testid="case-card"][data-id="${cardId}"]`)
  await expect(listed).toBeVisible()
  await expect(listed).toContainText('Waiting for approval')
  await expect(listed.getByTestId('case-card-linked')).toContainText('named by the agent')
  await expect(listed.getByTestId('case-card-unlink')).toHaveCount(0)

  // The Inbox row of the change, and its drawer, say what it resolves.
  await page.goto('/#/cases/inbox')
  const inboxRow = page.locator(`[data-inbox-row="proposal:${cardId}"]`)
  await expect(inboxRow.getByTestId('inbox-row-resolves')).toContainText(caseId)
  // Its address of before, c<n>, opens it as PC-<n> (CF-56).
  await page.goto(`/#/cases/inbox/proposal/${cardId.replace('PC-', 'c')}`)
  await expect(page).toHaveURL(new RegExp(`#/cases/inbox/proposal/${cardId}$`))
  const proposal = page.getByTestId('proposal-drawer')
  await expect(proposal.getByTestId('proposal-resolves')).toContainText(caseId)
  await expect(proposal.getByTestId('resolves-link').filter({ hasText: caseId })).toHaveAttribute('href', `#/cases/inbox/case/${caseNumber}`)
})

test('approved and applied, the change puts the case In progress, waiting for the next run to check; the next run still sees it: the fix did not hold (CF-29 to CF-31)', async ({ page }) => {
  // Approved in its drawer, as a person does: Run in Watson brings its
  // card to the drawer's Watson, which asks; Watson runs it as them (CF-62).
  await page.goto(`/#/cases/inbox/proposal/${cardId}`)
  const proposal = page.getByTestId('proposal-drawer')
  await expect(proposal.getByTestId('proposal-run-watson')).toBeEnabled()
  await proposal.getByTestId('proposal-run-watson').click()
  const inWatson = proposal.getByTestId('proposal-trouble').getByTestId('watson-proposal-card')
  await expect(inWatson.getByTestId('proposal-approve')).toBeEnabled()
  await inWatson.getByTestId('proposal-approve').click()
  let card: Card = { id: cardId, title: '', state: 'queued', cluster: 'local', source: { kind: 'job' } }
  await expect
    .poll(
      async () => {
        card = await getCard(page, cardId)
        return card.state
      },
      { timeout: 60_000 },
    )
    .toMatch(/^(applied|failed)$/)
  expect(card.state, JSON.stringify(card.results ?? card.reason)).toBe('applied')

  // The case records the fix by the change and waits for the next run.
  await expect.poll(async () => (await getCase(page, caseId)).verification, { timeout: 30_000 }).toBe('waiting')
  const c = await getCase(page, caseId)
  expect(c.status).toBe('inProgress')
  expect(c.fixApplied?.change).toBe(cardId)
  await page.goto(`/#/cases/inbox/case/${caseNumber}`)
  const drawer = page.getByTestId('case-drawer')
  await expect(drawer.getByTestId('case-status')).toHaveText('In progress')
  const banner = drawer.getByTestId('case-verification')
  await expect(banner).toHaveAttribute('data-state', 'waiting')
  await expect(banner).toContainText(`Fix applied by change ${cardId}`)
  await expect(banner).toContainText('waiting for the next run of')
  await expect(drawer.locator(`[data-testid="case-card"][data-id="${cardId}"]`)).toContainText('Applied')
  await expect(drawer.getByTestId('case-fix-applied')).toContainText(`Fix applied by change ${cardId}`)
  // The Inbox badges the case: read afresh, not at its half-minute turn.
  await page.goto('/#/cases/inbox?source=agents')
  await page.reload()
  await expect(page.locator(`[data-inbox-row="case:${caseNumber}"]`).getByTestId('inbox-row-verification')).toHaveText('fix applied')

  // The fake audit finds the same problem again: the fix did not hold, and the case stays In progress.
  await audit(page)
  await expect.poll(async () => (await getCase(page, caseId)).verification, { timeout: 30_000 }).toBe('notHeld')
  await page.goto(`/#/cases/inbox/case/${caseNumber}`)
  await expect(drawer.getByTestId('case-verification')).toHaveAttribute('data-state', 'notHeld')
  await expect(drawer.getByTestId('case-verification')).toContainText('The fix did not hold.')
  await expect(drawer.getByTestId('case-status')).toHaveText('In progress')
})

test('a person links a change asked for in a chat from the case drawer, and unlinks it; an agent’s own link stays (CF-28)', async ({ page }) => {
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  // A change Sherlock proposed in a chat, sent to Watson: kept as a card that names no case.
  const command = `kubectl label namespace default e2e-link=x${Date.now()} --overwrite`
  const sent = await api(page, 'POST', '/api/v1/watson/commands', { command, from: 'Sherlock', why: 'e2e: a change asked for in a chat' })
  expect(sent.status, JSON.stringify(sent.json)).toBe(202)
  let chat: Card | undefined
  await expect
    .poll(async () => {
      const list = ((await api(page, 'GET', '/api/v1/cases/proposals?cluster=local')).json as { proposals: Card[] }).proposals
      chat = list.find((p) => p.source.kind === 'chat' && p.title === command)
      return !!chat
    })
    .toBe(true)
  chatId = chat!.id
  expect(chat!.resolves ?? []).toHaveLength(0)

  // Link a change: found by what it does, picked, linked.
  await page.goto(`/#/cases/inbox/case/${caseNumber}`)
  const drawer = page.getByTestId('case-drawer')
  await drawer.getByTestId('case-link-change').click()
  const dialog = page.getByTestId('confirm')
  await expect(dialog).toBeVisible()
  await dialog.getByTestId('case-link-search').fill(command.split(' ')[4]!)
  await dialog.locator(`[data-testid="case-link-option"][data-id="${chatId}"]`).getByRole('radio').check()
  await dialog.getByTestId('confirm-ok').click()
  await expect(dialog).toHaveCount(0)
  const listed = drawer.locator(`[data-testid="case-card"][data-id="${chatId}"]`)
  await expect(listed).toBeVisible()
  await expect(listed.getByTestId('case-card-linked')).toContainText('linked by')
  // The card names the case too, linked by the person.
  const linked = await getCard(page, chatId)
  expect(linked.resolves).toContain(caseId)
  expect(linked.resolvesBy?.[caseId]).not.toBe('agent')
  // Linked twice is refused.
  expect((await api(page, 'POST', `/api/v1/cases/cases/${caseId}/changes`, { card: chatId })).status).toBe(409)

  // Unlinked by a person; an agent's own link cannot be.
  await listed.getByTestId('case-card-unlink').click()
  await expect(listed).toHaveCount(0)
  expect((await getCard(page, chatId)).resolves ?? []).not.toContain(caseId)
  expect((await api(page, 'DELETE', `/api/v1/cases/cases/${caseId}/changes/${cardId}`)).status).toBe(409)
  await expect(drawer.locator(`[data-testid="case-card"][data-id="${cardId}"]`)).toBeVisible()
})

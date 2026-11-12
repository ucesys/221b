// A proposed change's Sherlock and Watson in its drawer (Phase 12, CF-61,
// CF-62): the drawer opens near full screen, the proposal on the left and
// Sherlock and Watson on the right. Sherlock's conversation is the
// proposal's own and names who asked; Run in Watson on its commands hands
// them to the drawer's Watson; Run in Watson at the foot, which was
// Approve, brings the proposal's own card to that Watson, whose approval is
// the proposal's: it runs, from the proposal, and is logged on it. Decided,
// the conversation is deleted, and each case it resolves keeps the change
// for closing as Fixed.
import type { Page } from '@playwright/test'
import { expect, test } from './test'
import { cluster, consoleNode, nodeRows, snapshot } from './cluster'

test.describe.configure({ mode: 'serial' })

const SAME = { 'Sec-Fetch-Site': 'same-origin' }
const KEY = 'sk-ant-e2e-fake-key-0123456789'
const ON = { clusters: { local: true }, model: 'claude-opus-5-5', dailyUSD: 20, monthlyUSD: 200 }

interface Card {
  id: string
  title: string
  state: string
  source: { kind: string }
  approvals?: unknown[]
  commands?: { line: string; change: boolean; status: string; approver?: string }[]
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

let node = ''
let cardId = ''
const caseIds: string[] = []

test.afterAll(async ({ browser }) => {
  // Sherlock off, the node schedulable again, the cases closed and the card
  // decided, as the other specs expect.
  const page = await browser.newPage()
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  if (cardId) await api(page, 'POST', `/api/v1/aiops/proposals/${cardId}/reject`, { reason: 'e2e: done' })
  for (const id of caseIds) await api(page, 'POST', `/api/v1/aiops/cases/${id}/close`, { outcome: 'noAction', explanation: 'e2e: done' })
  await api(page, 'PUT', '/api/v1/sherlock/config', { ...ON, clusters: {} })
  if (node) await cluster.cordon(node, false)
  await page.close()
})

test('a proposed change is troubleshot in its drawer, and approved in its Watson: one approval, the change logged and given to its cases (CF-61, CF-62)', async ({ page }) => {
  test.setTimeout(180_000)
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  expect((await api(page, 'PUT', '/api/v1/jobs/case-analysis', { on: false })).status).toBe(200)
  expect((await api(page, 'PUT', '/api/v1/sherlock/config', ON)).status).toBe(200)
  expect((await api(page, 'PUT', '/api/v1/sherlock/key', { key: KEY })).status).toBe(200)
  // A pod to ask about, and a worker that does not run the console to cordon.
  type Pods = { items: { metadata: { name: string; namespace: string }; status: { phase?: string } }[] }
  const pods = (await cluster.getObject<Pods>('/api/v1/pods'))!.items.filter((p) => p.metadata.namespace !== 'kube-system' && p.status.phase === 'Running')
  const pod = pods[0]!.metadata
  const skip = await consoleNode()
  await expect
    .poll(async () => {
      node = nodeRows(await snapshot(page.request)).find((r) => r.role === 'worker' && r.status === 'Ready' && !r.cordoned && r.name !== skip)?.name ?? ''
      return node
    })
    .not.toBe('')
  // Two cases the change resolves: a proposal links several, so it has a conversation of its own.
  for (const what of ['restarts', 'is slow']) {
    const res = await page.request.post('/api/v1/aiops/cases', { headers: SAME, data: { title: `Proposal e2e: ${pod.name} ${what} ${Date.now()}`, cluster: 'local', resource: `${pod.namespace}/Pod/${pod.name}`, priority: 3 } })
    expect(res.status(), await res.text()).toBe(201)
    caseIds.push(((await res.json()) as { id: string }).id)
  }
  // A change Sherlock proposed in a chat, kept as a card, linked to both.
  const command = `kubectl cordon ${node}`
  const sent = await api(page, 'POST', '/api/v1/watson/commands', { command, from: 'Sherlock', why: 'e2e: a change asked for in a chat' })
  expect(sent.status, JSON.stringify(sent.json)).toBe(202)
  await expect
    .poll(async () => {
      const list = ((await api(page, 'GET', '/api/v1/aiops/proposals?state=queued&cluster=local')).json as { proposals: Card[] }).proposals
      cardId = list.find((p) => p.source.kind === 'chat' && p.title === command)?.id ?? ''
      return cardId
    })
    .not.toBe('')
  for (const id of caseIds) {
    const linked = await api(page, 'POST', `/api/v1/aiops/cases/${id}/changes`, { card: cardId })
    expect(linked.status, JSON.stringify(linked.json)).toBe(200)
  }

  // Near full screen, split: the proposal on the left, Troubleshoot on the right, its cases linked.
  await page.goto(`/#/aiops/inbox/proposal/${cardId}`)
  const drawer = page.getByTestId('proposal-drawer')
  await expect(drawer).toHaveAttribute('data-size', 'full')
  await expect(drawer.getByTestId('drawer-split')).toHaveAttribute('data-layout', 'split')
  const trouble = drawer.getByTestId('proposal-trouble')
  await expect(trouble).toBeVisible()
  for (const id of caseIds) await expect(drawer.getByTestId('resolves-link').filter({ hasText: id })).toBeVisible()
  // One approval is needed: no count at the foot, and no Approve.
  await expect(drawer.getByTestId('proposal-count')).toHaveCount(0)
  await expect(drawer.getByRole('button', { name: 'Approve', exact: true })).toHaveCount(0)

  // Sherlock on the proposal: its own conversation, the question naming who asked (CF-61).
  const sherlock = trouble.getByTestId('sherlock')
  await expect(sherlock.getByTestId('sherlock-empty')).toContainText(`Ask Sherlock about ${cardId}`)
  await sherlock.getByTestId('sherlock-input').fill(`Why is ${pod.namespace}/${pod.name} failing?`)
  await expect(sherlock.getByTestId('sherlock-send')).toBeEnabled({ timeout: 20_000 })
  await sherlock.getByTestId('sherlock-send').click()
  await expect(sherlock.getByTestId('sherlock-asker')).toContainText('asked')
  const answer = sherlock.getByTestId('sherlock-answer').last()
  await expect(answer).toHaveAttribute('data-status', 'done', { timeout: 30_000 })

  // Run in Watson hands Sherlock's command to the drawer's Watson, sent from the proposal.
  await answer.getByTestId('sherlock-run-watson').click()
  await expect(trouble).toHaveAttribute('data-pane', 'watson')
  const watson = trouble.getByTestId('watson')
  await expect(watson.getByTestId('watson-ran').last()).toHaveAttribute('data-status', 'done', { timeout: 30_000 })
  await expect(watson.getByTestId('watson-ran').last()).toContainText(`describe pod ${pod.name}`)

  // Run in Watson at the foot: the proposal's own card, in Watson; its approval is the proposal's (CF-62).
  await trouble.locator('[data-testid=proposal-trouble-sherlock]:visible').click()
  await expect(trouble).toHaveAttribute('data-pane', 'sherlock')
  await drawer.getByTestId('proposal-run-watson').click()
  await expect(trouble).toHaveAttribute('data-pane', 'watson')
  const card = watson.getByTestId('watson-proposal-card')
  await expect(card.getByTestId('watson-card-command')).toHaveText(command)
  await card.getByTestId('proposal-approve').click()
  await expect(drawer.getByTestId('proposal-state')).toHaveText(/^(Approved, running|Applied)/, { timeout: 15_000 })
  await expect(watson.getByTestId('watson-ran').filter({ hasText: command })).toHaveAttribute('data-status', 'done', { timeout: 30_000 })
  let decided: Card = { id: cardId, title: command, state: 'queued', source: { kind: 'chat' } }
  await expect
    .poll(
      async () => {
        decided = (await api(page, 'GET', `/api/v1/aiops/proposals/${cardId}`)).json as Card
        return decided.state
      },
      { timeout: 60_000 },
    )
    .toBe('applied')
  expect(decided.approvals).toHaveLength(1)
  // Decided: the log keeps the change that finished done, never output; the read goes.
  await expect.poll(async () => ((await api(page, 'GET', `/api/v1/aiops/proposals/${cardId}`)).json as Card).commands?.map((c) => `${c.change ? 'change' : 'read'}:${c.status}:${c.line}`)).toEqual([`change:done:${command}`])
  await expect(drawer.getByTestId('proposal-commands')).toContainText(command, { timeout: 15_000 })
  const chat = (await api(page, 'GET', `/api/v1/aiops/proposals/${cardId}/sherlock/conversation`)).json as { messages: unknown[] }
  expect(chat.messages).toHaveLength(0)
  cardId = ''

  // Each case it resolves starts closing as Fixed from the change (CF-49).
  for (const id of caseIds) {
    await expect.poll(async () => ((await api(page, 'GET', `/api/v1/aiops/cases/${id}`)).json as { fixCommands: string[] }).fixCommands, { timeout: 15_000 }).toEqual([command])
  }
})

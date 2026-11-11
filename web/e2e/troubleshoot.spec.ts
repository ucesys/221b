// Troubleshooting a case in its drawer (Phase 12, wave 6f, CF-46 to
// CF-49): the drawer opens near full screen, the case on the left and
// Sherlock and Watson on the right. Sherlock's conversation is the case's
// and names who asked; Run in Watson hands the command to the case's Watson; a read
// and an approved change run from the case and are logged on it; closing
// as Fixed starts What was done from the change, deletes the conversation
// and keeps only the change; reopened, the conversation starts empty.
import type { Page } from '@playwright/test'
import { expect, test } from './test'
import { cluster, consoleNode, nodeRows, snapshot } from './cluster'

test.describe.configure({ mode: 'serial' })

const SAME = { 'Sec-Fetch-Site': 'same-origin' }
const KEY = 'sk-ant-e2e-fake-key-0123456789'
const ON = { clusters: { local: true }, model: 'claude-opus-5-5', dailyUSD: 20, monthlyUSD: 200 }

async function call(page: Page, path: string, body: unknown, method = 'PUT'): Promise<number> {
  return page.evaluate(async ([p, b, m]) => (await fetch(p as string, { method: m as string, headers: { 'Content-Type': 'application/json' }, body: b === null ? undefined : JSON.stringify(b) })).status, [path, body, method] as const)
}

let node = ''

test.afterAll(async ({ browser }) => {
  // Sherlock off and the node schedulable again, as the other specs expect.
  const page = await browser.newPage()
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  await call(page, '/api/v1/sherlock/config', { ...ON, clusters: {} })
  if (node) await cluster.cordon(node, false)
  const list = (await (await page.request.get('/api/v1/aiops/cases?open=1')).json()) as { cases: { id: string; title: string }[] }
  for (const c of list.cases.filter((x) => x.title.startsWith('Troubleshoot e2e'))) await call(page, `/api/v1/aiops/cases/${c.id}/close`, { outcome: 'noAction', explanation: 'e2e: done' }, 'POST')
  await page.close()
})

test('a case is troubleshot in its drawer: Sherlock on the case, Watson from it, the fix kept and reused (CF-46 to CF-49)', async ({ page }) => {
  test.setTimeout(180_000)
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  expect(await call(page, '/api/v1/jobs/case-analysis', { on: false })).toBe(200)
  expect(await call(page, '/api/v1/sherlock/config', ON)).toBe(200)
  expect(await call(page, '/api/v1/sherlock/key', { key: KEY })).toBe(200)
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
  const res = await page.request.post('/api/v1/aiops/cases', { headers: SAME, data: { title: `Troubleshoot e2e: ${pod.name} restarts ${Date.now()}`, cluster: 'local', resource: `${pod.namespace}/Pod/${pod.name}`, priority: 3 } })
  expect(res.status(), await res.text()).toBe(201)
  const { id, number } = (await res.json()) as { id: string; number: number }

  // Near full screen, split: the case on the left, Troubleshoot on the right (CF-46).
  await page.goto(`/#/aiops/inbox/case/${number}`)
  const drawer = page.getByTestId('case-drawer')
  await expect(drawer.getByTestId('case-kicker')).toContainText(`Case ${id}`)
  await expect(drawer).toHaveAttribute('data-size', 'full')
  await expect(drawer.getByTestId('drawer-split')).toHaveAttribute('data-layout', 'split')
  const box = (await drawer.boundingBox())!
  expect(box.width).toBeGreaterThan(1440 * 0.9)
  const trouble = drawer.getByTestId('case-trouble')
  await expect(trouble).toBeVisible()
  await expect(drawer.getByTestId('case-actions')).toBeVisible()
  // The dock's round button is hidden over the split drawer, whose buttons it would cover.
  await expect(page.getByTestId('sherlock-fab')).toBeHidden()
  await expect(drawer.getByTestId('case-age')).toContainText('age: ')
  // The priority, type and assignee are in the header, the footer keeps the actions, and Sherlock is here already: no Ask Sherlock (CF-58).
  await expect(drawer.getByTestId('case-controls').getByTestId('case-priority-set')).toHaveValue('3')
  await expect(drawer.getByTestId('case-actions').getByTestId('case-priority-set')).toHaveCount(0)
  await expect(drawer.getByTestId('ask-sherlock')).toHaveCount(0)
  // The console's own cluster, "local", by its name in the cluster list (CF-60).
  await expect(drawer.getByTestId('case-cluster-set')).toHaveValue('local')

  // Sherlock on the case: the question names who asked (CF-47).
  const sherlock = trouble.getByTestId('sherlock')
  await expect(sherlock.getByTestId('sherlock-empty')).toContainText(`Ask Sherlock about ${id}`)
  await sherlock.getByTestId('sherlock-input').fill(`Why is ${pod.namespace}/${pod.name} failing?`)
  // Asking waits for the case's stream to say Sherlock is available.
  await expect(sherlock.getByTestId('sherlock-send')).toBeEnabled({ timeout: 20_000 })
  await sherlock.getByTestId('sherlock-send').click()
  await expect(sherlock.getByTestId('sherlock-asker')).toContainText('asked')
  const answer = sherlock.getByTestId('sherlock-answer').last()
  await expect(answer).toHaveAttribute('data-status', 'done', { timeout: 30_000 })
  await expect(sherlock.getByTestId('sherlock-new')).toHaveCount(0)

  // Run in Watson hands it to the case's Watson, as the dock's Send to
  // Watson does: a read runs (Auto-run reads is on), a change asks (CF-48).
  await answer.getByTestId('sherlock-run-watson').click()
  await expect(trouble).toHaveAttribute('data-pane', 'watson')
  const watson = trouble.getByTestId('watson')
  await expect(watson.getByTestId('watson-input')).toHaveValue('')
  await expect(watson.getByTestId('watson-ran').last()).toHaveAttribute('data-status', 'done', { timeout: 30_000 })
  await expect(watson.getByTestId('watson-ran').last()).toContainText(`describe pod ${pod.name}`)
  // Watson takes the side's height, not only what it holds.
  const side = (await drawer.getByTestId('drawer-split-side').boundingBox())!
  expect((await watson.boundingBox())!.height).toBeGreaterThan(side.height * 0.8)

  // A change, approved, from the case.
  await watson.getByTestId('watson-input').fill(`kubectl cordon ${node}`)
  await watson.getByTestId('watson-input').press('Enter')
  const card = watson.getByTestId('watson-card').last()
  await expect(card).toHaveAttribute('data-command', `kubectl cordon ${node}`)
  await card.getByTestId('watson-approve').click()
  await expect(watson.getByTestId('watson-ran').last()).toHaveAttribute('data-status', 'done', { timeout: 30_000 })

  // The case logs both, everyone's, with no output.
  const log = drawer.getByTestId('case-commands')
  await expect(log.getByTestId('case-command')).toHaveCount(2, { timeout: 15_000 })
  await expect(log.locator('[data-testid=case-command][data-kind=change][data-status=done]')).toContainText(`kubectl cordon ${node}`)
  await expect(log.locator('[data-testid=case-command][data-kind=read][data-status=done]')).toContainText('describe pod')
  const kept = (await (await page.request.get(`/api/v1/aiops/cases/${id}`)).json()) as { commands: { line: string; lines?: unknown }[]; fixCommands: string[] }
  expect(kept.fixCommands).toEqual([`kubectl cordon ${node}`])
  expect(JSON.stringify(kept.commands)).not.toContain('cordoned')

  // Fixed: What was done starts from the change (CF-49); the conversation goes, the change stays.
  await drawer.getByTestId('case-close-open').click()
  await drawer.getByTestId('case-outcome-fix').check()
  await expect(drawer.getByTestId('case-fix-text')).toHaveValue(`kubectl cordon ${node}`)
  await expect(drawer.getByTestId('case-fix-prefilled')).toBeVisible()
  await drawer.getByTestId('case-fix-save').click()
  await expect(drawer.getByTestId('case-status')).toHaveText('Closed: fixed')
  await expect(drawer.getByTestId('case-trouble-closed')).toBeVisible()
  await expect(log.getByTestId('case-command')).toHaveCount(1)
  await expect(log).toContainText(`kubectl cordon ${node}`)
  const chat = (await (await page.request.get(`/api/v1/aiops/cases/${id}/sherlock/conversation`)).json()) as { messages: unknown[] }
  expect(chat.messages).toHaveLength(0)

  // Reopened: an empty conversation, and only the change kept.
  await drawer.getByTestId('case-reopen').click()
  await drawer.getByTestId('case-reopen-reason').fill('It restarts again.')
  await drawer.getByTestId('case-reopen-save').click()
  await expect(drawer.getByTestId('case-status')).toHaveText(/^(New|In progress)$/)
  await expect(trouble.getByTestId('sherlock-empty')).toBeVisible()
  await expect(trouble.getByTestId('sherlock-question')).toHaveCount(0)
  await expect(log.getByTestId('case-command')).toHaveCount(1)
  await expect(log).toContainText(`kubectl cordon ${node}`)
})

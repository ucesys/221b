// Watson (Phase 6): the terminal beside Sherlock. Reads run at once and
// print what kubectl and helm print; changes wait on a card that names
// what they touch and what the server's dry run said, run as the person
// who approves them, lapse when their objects change, and are refused on
// read-only clusters. In kind every supported command is checked against
// the real kubectl and helm (P6-02).
import AxeBuilder from '@axe-core/playwright'
import type { APIRequestContext, Page } from '@playwright/test'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { expect, test } from './test'
import { cluster, consoleNode, healthyWorkers, inKind, kubectl, nodeRows, snapshot } from './cluster'
import { E2E_DEV, E2E_DEV_KUBE, E2E_USER, E2E_VIEWER, E2E_VIEWER_KUBE, signInAs } from './accounts'
import type { WatsonItem } from '../src/api/watson'

test.describe.configure({ mode: 'serial' })

const run = promisify(execFile)
const SAME = { 'Sec-Fetch-Site': 'same-origin' }
// Watson shows only where Sherlock is on (Phase 6.2); no key is needed for that.
const SHERLOCK_ON = { clusters: { local: true }, model: 'claude-opus-5-5', dailyUSD: 20, monthlyUSD: 200 }
const FINAL = ['done', 'failed', 'stopped', 'refused', 'lapsed', 'discarded']

const base = (c = 'local') => (c === 'local' ? '/api/v1/' : `/api/v1/clusters/${encodeURIComponent(c)}/`)

async function submit(request: APIRequestContext, command: string, o: { cluster?: string; run?: boolean } = {}): Promise<WatsonItem> {
  const res = await request.post(base(o.cluster) + 'watson/commands', { data: { command, from: 'End-to-end test', run: o.run ?? true }, headers: SAME })
  expect(res.status(), await res.text()).toBe(202)
  const items = (await res.json()) as WatsonItem[]
  expect(items).toHaveLength(1)
  return items[0]!
}

async function item(request: APIRequestContext, id: string, c?: string): Promise<WatsonItem> {
  const res = await request.get(base(c) + `watson/commands/${id}`)
  expect(res.status(), await res.text()).toBe(200)
  return (await res.json()) as WatsonItem
}

async function until(request: APIRequestContext, id: string, ok: (it: WatsonItem) => boolean, timeout = 90_000, c?: string): Promise<WatsonItem> {
  let last: WatsonItem | undefined
  await expect
    .poll(
      async () => {
        last = await item(request, id, c)
        return ok(last)
      },
      { timeout, intervals: [200, 500, 1000] },
    )
    .toBe(true)
  return last!
}

/** Runs a command through Watson: a read at once, a change once its card is ready and approved. */
async function watson(request: APIRequestContext, command: string, c?: string): Promise<WatsonItem> {
  const it = await submit(request, command, { cluster: c })
  if (it.kind === 'change' && it.status === 'pending') {
    await until(request, it.id, (x) => !!x.plan?.ready && !x.plan.dryRunning, 60_000, c)
    const res = await request.post(base(c) + `watson/commands/${it.id}/approve`, { headers: SAME })
    expect(res.status(), await res.text()).toBe(200)
  }
  return until(request, it.id, (x) => FINAL.includes(x.status), 120_000, c)
}

const text = (it: WatsonItem, k?: 'out' | 'err') => (it.lines ?? []).filter((l) => !k || l.k === k).map((l) => l.t).join('\n')

async function ok(request: APIRequestContext, command: string): Promise<WatsonItem> {
  const it = await watson(request, command)
  expect(it.status, `${command}\n${text(it)}`).toBe('done')
  expect(it.exit, `${command}\n${text(it)}`).toBe(0)
  return it
}

/** What the real kubectl or helm prints, in kind. */
async function real(tool: 'kubectl' | 'helm', ...args: string[]): Promise<string> {
  const { stdout } = await run(tool, args, { timeout: 60_000, maxBuffer: 16 << 20 })
  return stdout.replace(/\n$/, '')
}

async function open(page: Page, hash = '#/nodes') {
  await page.goto('/' + hash)
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  await expect(page.getByTestId('watson')).toBeVisible()
}

const pane = (page: Page) => page.getByTestId('watson')
const lastRan = (page: Page) => pane(page).getByTestId('watson-ran').last()

/** A ready worker the console does not run on, not cordoned, to cordon and drain. */
async function spareWorker(request: APIRequestContext): Promise<string> {
  const skip = await consoleNode()
  let name = ''
  await expect
    .poll(
      async () => {
        const rows = nodeRows(await snapshot(request)).filter((r) => r.role === 'worker' && r.status === 'Ready' && !r.cordoned && r.name !== skip)
        name = (rows.find((r) => r.problems.length === 0) ?? rows[0])?.name ?? ''
        return name
      },
      { timeout: 30_000 },
    )
    .not.toBe('')
  return name
}

let play: Awaited<ReturnType<typeof cluster.watsonPlayground>>

test.beforeAll(async ({ browser, browserName }) => {
  play = await cluster.watsonPlayground(browserName)
  const page = await browser.newPage()
  // The document, not the load event, which Firefox holds while a stream opens.
  await page.goto('/#/nodes', { waitUntil: 'domcontentloaded' })
  // A clean slate: the second-person rule off, and no commands left over.
  await page.request.put('/api/v1/watson/config', { data: { twoPerson: false }, headers: SAME })
  await page.request.post('/api/v1/watson/clear', { headers: SAME })
  const on = await page.request.put('/api/v1/sherlock/config', { data: SHERLOCK_ON, headers: SAME })
  expect(on.status(), await on.text()).toBe(200)
  await page.evaluate(() => localStorage.removeItem('221b.dock'))
  await page.close()
})

test.afterAll(async ({ browser }) => {
  const page = await browser.newPage()
  await page.request.put('/api/v1/sherlock/config', { data: { ...SHERLOCK_ON, clusters: {} }, headers: SAME })
  await page.close()
  await play?.cleanup()
})

test('Watson is not shown where Sherlock is off (Phase 6.2)', async ({ page }) => {
  await page.request.put('/api/v1/sherlock/config', { data: { ...SHERLOCK_ON, clusters: {} }, headers: SAME })
  try {
    // The assistants' stream starts with their status: once it has answered, the dock has decided.
    const stream = page.waitForResponse((r) => new URL(r.url()).pathname.endsWith('/assistants/events'))
    await page.goto('/#/nodes')
    await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
    await stream
    await expect(page.getByTestId('sherlock-dock')).toHaveCount(0)
    await expect(page.getByTestId('watson')).toHaveCount(0)
    await expect(page.getByTestId('sherlock-fab')).toHaveCount(0)
  } finally {
    const on = await page.request.put('/api/v1/sherlock/config', { data: SHERLOCK_ON, headers: SAME })
    expect(on.status(), await on.text()).toBe(200)
  }
  await open(page)
})

test('a typed read runs at once and prints what kubectl prints; shell syntax is refused (P6-01, P6-03)', async ({ page }) => {
  await open(page)
  await expect(pane(page).getByTestId('watson-empty')).toContainText('kubectl get nodes')
  await pane(page).getByTestId('watson-input').fill('kubectl get nodes')
  await pane(page).getByTestId('watson-input').press('Enter')
  await expect(lastRan(page)).toHaveAttribute('data-status', 'done', { timeout: 30_000 })
  const out = await lastRan(page).getByTestId('watson-output').innerText()
  expect(out.split('\n')[0]).toMatch(/^NAME\s+STATUS\s+ROLES\s+AGE\s+VERSION$/)
  const names = (await snapshot(page.request)).views.nodes!.rows.map((r) => (r as { name: string }).name).sort()
  const printed = out.split('\n').slice(1).map((l) => l.split(/\s+/)[0]!).filter(Boolean).sort()
  expect(printed).toEqual(names)
  await expect(lastRan(page).getByTestId('watson-status')).toContainText('done')
  if (inKind) {
    // The columns but the ages match the real kubectl's, byte for byte.
    const strip = (s: string) => s.split('\n').map((l) => l.replace(/\s+\S+\s+(v\d\S*)$/, ' $1').replace(/\s+/g, ' '))
    expect(strip(out)).toEqual(strip(await real('kubectl', 'get', 'nodes')))
  }

  // Pipes, redirections and command substitution are not run, and say why.
  await pane(page).getByTestId('watson-input').fill('kubectl get pods -A | grep Crash')
  await pane(page).getByTestId('watson-input').press('Enter')
  const refused = pane(page).getByTestId('watson-refused').last()
  await expect(refused.getByTestId('watson-reason')).toContainText('|')
  await expect(refused).toContainText('nothing ran')
  // So is a command Watson does not run.
  await pane(page).getByTestId('watson-input').fill('kubectl exec -it web-0 -- sh')
  await pane(page).getByTestId('watson-input').press('Enter')
  await expect(pane(page).getByTestId('watson-refused').last()).toContainText('kubectl exec')

  // The history comes back with the up arrow.
  await pane(page).getByTestId('watson-input').press('ArrowUp')
  await expect(pane(page).getByTestId('watson-input')).toHaveValue('kubectl exec -it web-0 -- sh')
})

test('a page holds two event streams: its views, and Sherlock and Watson together (Phase 6 review)', async ({ page }) => {
  // A browser opens at most six connections to a host over HTTP/1.1, for
  // all its tabs: each stream a page holds open takes one of them.
  const held = new Set<string>()
  const stream = (u: string) => new URL(u).pathname.endsWith('/events')
  page.on('request', (r) => {
    if (stream(r.url())) held.add(r.url())
  })
  page.on('requestfinished', (r) => held.delete(r.url()))
  page.on('requestfailed', (r) => held.delete(r.url()))
  await open(page)
  await page.waitForTimeout(1500)
  expect([...held].map((u) => new URL(u).pathname).sort()).toEqual(['/api/v1/assistants/events', '/api/v1/events'])
})

test('every supported read runs as kubectl and helm would (P6-02)', async ({ request }) => {
  const ns = play.namespace
  const node = healthyWorkers(await snapshot(request))[0]!.name
  const pods = await watson(request, `kubectl get pods -n ${ns} -l app=${play.deployment} -o name`)
  const pod = text(pods, 'out').split('\n')[0]!.replace('pod/', '')
  expect(pod).toBeTruthy()

  const names = await ok(request, 'kubectl get nodes -o name')
  const deploy = await ok(request, `kubectl get deployment ${play.deployment} -n ${ns} -o jsonpath={.spec.replicas}`)
  expect(text(deploy, 'out')).toBe('2')
  const described = await ok(request, `kubectl describe deployment ${play.deployment} -n ${ns}`)
  expect(text(described, 'out')).toMatch(new RegExp(`^Name:\\s+${play.deployment}$`, 'm'))
  const logs = await ok(request, `kubectl logs ${pod} -n ${ns} --tail=20`)
  const top = await ok(request, 'kubectl top nodes')
  expect(text(top, 'out')).toMatch(/^NAME\s+CPU\(cores\)/)
  await ok(request, `kubectl events -n ${ns}`)
  const canI = await ok(request, `kubectl auth can-i delete pods -n ${ns}`)
  expect(text(canI, 'out')).toBe('yes')
  const nodeYAML = await ok(request, `kubectl get node ${node} -o yaml`)
  expect(text(nodeYAML, 'out')).toContain(`name: ${node}`)
  const list = await ok(request, 'helm list -A')
  expect(text(list, 'out').split('\n')[0]).toMatch(/^NAME\s+NAMESPACE\s+REVISION\s+UPDATED\s+STATUS\s+CHART\s+APP VERSION\s*$/)

  // A Secret's values are never printed.
  const secret = await watson(request, `kubectl get secrets -n ${ns} -o yaml`)
  expect(secret.status).toBe('refused')
  expect(secret.rule).toBe('secret values')

  if (play.release) {
    const [rns, rname] = inKind ? [ns, play.release] : play.release.split('/')
    await ok(request, `helm status ${rname} -n ${rns}`)
    const hist = await ok(request, `helm history ${rname} -n ${rns}`)
    expect(text(hist, 'out').split('\n')[0]).toMatch(/^REVISION\s+UPDATED\s+STATUS\s+CHART\s+APP VERSION\s+DESCRIPTION\s*$/)
    const values = await ok(request, `helm get values ${rname} -n ${rns}`)
    if (inKind) {
      expect(text(values, 'out')).toBe(await real('helm', 'get', 'values', rname!, '-n', rns!))
      expect(text(list, 'out').split('\n').map((l) => l.split(/\s+/)[0]).sort()).toEqual((await real('helm', 'list', '-A')).split('\n').map((l) => l.split(/\s+/)[0]).sort())
    }
  }
  if (inKind) {
    expect(text(names, 'out')).toBe(await real('kubectl', 'get', 'nodes', '-o', 'name'))
    expect(text(logs, 'out')).toBe(await real('kubectl', 'logs', pod, '-n', ns, '--tail=20'))
    expect(text(logs, 'out')).toContain('hello from watson')
    expect(text(canI, 'out')).toBe(await real('kubectl', 'auth', 'can-i', 'delete', 'pods', '-n', ns))
    expect(text(described, 'out').split('\n').slice(0, 6)).toEqual((await real('kubectl', 'describe', 'deployment', play.deployment, '-n', ns)).split('\n').slice(0, 6))
  }
})

test('every supported change runs once approved, as the approver, and does what kubectl and helm would (P6-02, P6-05)', async ({ request }) => {
  test.setTimeout(300_000)
  const ns = play.namespace
  const d = play.deployment
  const dPath = `/apis/apps/v1/namespaces/${ns}/deployments/${d}`
  type Deploy = { metadata: { labels?: Record<string, string>; annotations?: Record<string, string> }; spec: { replicas: number; template: { metadata: { annotations?: Record<string, string> } } } }
  const deployment = async () => (await cluster.getObject<Deploy>(dPath))!

  // A worker that does not run the console, to cordon and drain.
  const node = await spareWorker(request)
  const unschedulable = async () => !!(await cluster.getObject<{ spec: { unschedulable?: boolean } }>(`/api/v1/nodes/${node}`))!.spec.unschedulable
  try {
    await ok(request, `kubectl cordon ${node}`)
    expect(await unschedulable()).toBe(true)
    await ok(request, `kubectl uncordon ${node}`)
    expect(await unschedulable()).toBe(false)
    const drained = await ok(request, `kubectl drain ${node} --ignore-daemonsets --delete-emptydir-data --pod-selector=app=${d} --timeout=90s`)
    expect(text(drained)).toContain(`node/${node} drained`)
    expect(await unschedulable()).toBe(true)
  } finally {
    await cluster.cordon(node, false)
  }
  // Taints, which fleets give and take (asked after 0.7.1).
  type Tainted = { spec: { taints?: { key: string; value?: string; effect: string }[] } }
  const taints = async () => ((await cluster.getObject<Tainted>(`/api/v1/nodes/${node}`))!.spec.taints ?? []).filter((t) => t.key === 'e2e-watson')
  try {
    await ok(request, `kubectl taint nodes ${node} e2e-watson=tainted:PreferNoSchedule`)
    expect(await taints()).toEqual([{ key: 'e2e-watson', value: 'tainted', effect: 'PreferNoSchedule' }])
    await ok(request, `kubectl taint nodes ${node} e2e-watson:PreferNoSchedule-`)
    expect(await taints()).toEqual([])
  } finally {
    await cluster.taint(node, { key: 'e2e-watson', value: 'tainted', effect: 'PreferNoSchedule' }, false)
  }

  await ok(request, `kubectl scale deployment/${d} -n ${ns} --replicas=3`)
  expect((await deployment()).spec.replicas).toBe(3)
  await ok(request, `kubectl rollout restart deployment/${d} -n ${ns}`)
  expect((await deployment()).spec.template.metadata.annotations?.['kubectl.kubernetes.io/restartedAt']).toBeTruthy()
  // The controller records the new revision before there is one to go back from.
  await expect.poll(async () => text(await ok(request, `kubectl get replicasets -n ${ns} -l app=${d} -o name`)).split('\n').length, { timeout: 60_000 }).toBeGreaterThanOrEqual(2)
  const undone = await ok(request, `kubectl rollout undo deployment/${d} -n ${ns}`)
  expect(text(undone)).toContain(`deployment.apps/${d} rolled back`)
  await expect.poll(async () => (await deployment()).spec.template.metadata.annotations?.['kubectl.kubernetes.io/restartedAt'] ?? '', { timeout: 30_000 }).toBe('')

  await ok(request, `kubectl label deployment ${d} -n ${ns} e2e-watson=labelled`)
  expect((await deployment()).metadata.labels?.['e2e-watson']).toBe('labelled')
  await ok(request, `kubectl annotate deployment ${d} -n ${ns} e2e-watson/note=annotated`)
  expect((await deployment()).metadata.annotations?.['e2e-watson/note']).toBe('annotated')

  // Once the rollout has settled, a pod of the Deployment.
  type Pods = { items: { metadata: { name: string; deletionTimestamp?: string }; status: { phase?: string } }[] }
  const settled = async () => {
    const x = (await cluster.getObject<Deploy & { metadata: { generation: number }; status: { observedGeneration?: number; replicas?: number; updatedReplicas?: number; readyReplicas?: number } }>(dPath))!
    const pods = (await cluster.getObject<Pods>(`/api/v1/namespaces/${ns}/pods?labelSelector=app%3D${d}`))!.items.filter((p) => !p.metadata.deletionTimestamp && p.status.phase === 'Running')
    const want = x.spec.replicas
    return (x.status.observedGeneration ?? 0) >= x.metadata.generation && x.status.updatedReplicas === want && x.status.readyReplicas === want && x.status.replicas === want && pods.length === want ? pods[0]!.metadata.name : ''
  }
  let pod = ''
  await expect.poll(async () => (pod = await settled()), { timeout: 120_000 }).not.toBe('')
  await ok(request, `kubectl delete pod ${pod} -n ${ns}`)
  await expect.poll(async () => cluster.getObject(`/api/v1/namespaces/${ns}/pods/${pod}`), { timeout: 60_000 }).toBeNull()

  if (inKind) {
    const rolled = await ok(request, `helm rollback ${play.release} 1 -n ${ns}`)
    expect(text(rolled)).toContain('Rollback was a success')
    expect(await real('helm', 'get', 'values', play.release, '-n', ns)).toContain('greeting: first')
    expect(await kubectl('-n', ns, 'get', 'configmap', play.release, '-o', 'jsonpath={.data.greeting}')).toBe('first')
  }

  // Each change is in the audit log, naming the person and the cluster.
  await expect
    .poll(async () => (await cluster.auditLog()).filter((r) => r.action === 'watson.result' && String(r.detail ?? JSON.stringify(r)).includes(`deployment ${d}`)).length, { timeout: 15_000 })
    .toBeGreaterThan(0)
  const results = (await cluster.auditLog()).filter((r) => r.action === 'watson.approve')
  expect(results.length).toBeGreaterThan(0)
  expect(JSON.stringify(results.at(-1))).toContain(E2E_USER)
  expect(JSON.stringify(results.at(-1))).toMatch(/"cluster":"local"/)
})

test('a cordon asks on a card that names the node, the pods, and the dry run; approving runs it (P6-04, P6-06, P6-08)', async ({ page }) => {
  const node = await spareWorker(page.request)
  // Changes come from Watson's line or Sherlock's answers; the node details have no buttons for them.
  await open(page, `#/nodes?open=${node}`)
  await expect(page.getByTestId('node-cordon')).toHaveCount(0)
  await page.keyboard.press('Escape')
  try {
    await pane(page).getByTestId('watson-input').fill(`kubectl cordon ${node}`)
    await pane(page).getByTestId('watson-input').press('Enter')
    const card = pane(page).getByTestId('watson-card').last()
    await expect(card).toHaveAttribute('data-command', `kubectl cordon ${node}`)
    await expect(card.getByTestId('watson-targets')).toContainText(`node ${node}`)
    await expect(card.getByTestId('watson-impact')).toContainText('No new pods will be scheduled')
    await expect(card.getByTestId('watson-dryrun')).toContainText('nothing changed, no error')
    await expect(card.getByTestId('watson-dryrun')).toContainText(`node/${node} cordoned (server dry run)`)
    await expect(card.getByTestId('watson-expiry')).toContainText(/lapses in (9:[0-5]\d|10:00)/)
    expect(await cluster.getObject<{ spec: { unschedulable?: boolean } }>(`/api/v1/nodes/${node}`)).not.toMatchObject({ spec: { unschedulable: true } })
    await card.getByTestId('watson-approve').click()
    const ran = lastRan(page)
    await expect(ran).toHaveAttribute('data-status', 'done', { timeout: 30_000 })
    await expect(ran.getByTestId('watson-output')).toHaveText(`node/${node} cordoned`)
    await expect(ran.getByTestId('watson-status')).toContainText('approved by')
    await expect.poll(async () => (await cluster.getObject<{ spec: { unschedulable?: boolean } }>(`/api/v1/nodes/${node}`))!.spec.unschedulable ?? false).toBe(true)
    await pane(page).getByTestId('watson-input').fill(`kubectl uncordon ${node}`)
    await pane(page).getByTestId('watson-input').press('Enter')
    await pane(page).getByTestId('watson-card').last().getByTestId('watson-approve').click()
    await expect(lastRan(page)).toContainText(`node/${node} uncordoned`, { timeout: 30_000 })

    // The audit log has the request, the approval and the result, naming the person and the cluster.
    await expect
      .poll(async () => {
        const log = (await cluster.auditLog()).filter((r) => String(r.action).startsWith('watson.') && JSON.stringify(r).includes(`kubectl cordon ${node}`))
        return ['watson.command', 'watson.approve', 'watson.result'].every((a) => log.some((r) => r.action === a && JSON.stringify(r).includes(E2E_USER) && JSON.stringify(r).includes('"cluster":"local"')))
      }, { timeout: 15_000 })
      .toBe(true)
  } finally {
    await cluster.cordon(node, false)
  }
})

test('a drain’s card names the pods it evicts and the budgets that may hold it up (P6-04)', async ({ page }) => {
  const node = await spareWorker(page.request)
  await open(page)
  await pane(page).getByTestId('watson-input').fill(`kubectl drain ${node} --ignore-daemonsets --delete-emptydir-data`)
  await pane(page).getByTestId('watson-input').press('Enter')
  const card = pane(page).getByTestId('watson-card').last()
  await expect(card).toHaveAttribute('data-command', new RegExp(`^kubectl drain ${node} `))
  await expect(card.getByTestId('watson-impact')).toContainText(/Cordons .+ (evicts \d+ pods?|no pod needs to be evicted)/, { timeout: 30_000 })
  await expect(card.getByTestId('watson-dryrun')).toContainText(/dry run/, { timeout: 30_000 })
  await card.getByTestId('watson-discard').click()
  await expect(pane(page).getByTestId('watson-discarded').last()).toContainText(`kubectl drain ${node}`)
  expect((await cluster.getObject<{ spec: { unschedulable?: boolean } }>(`/api/v1/nodes/${node}`))!.spec.unschedulable ?? false).toBe(false)
})

test('a card lapses when its objects change before anyone approves it (P6-04)', async ({ page }) => {
  const node = await spareWorker(page.request)
  await open(page)
  await pane(page).getByTestId('watson-input').fill(`kubectl cordon ${node}`)
  await pane(page).getByTestId('watson-input').press('Enter')
  const card = pane(page).getByTestId('watson-card').last()
  await expect(card.getByTestId('watson-dryrun')).toContainText('nothing changed', { timeout: 30_000 })
  try {
    await cluster.cordon(node, true)
    const lapsed = pane(page).getByTestId('watson-lapsed').last()
    await expect(lapsed.getByTestId('watson-reason')).toContainText(`node ${node} changed after this card appeared`, { timeout: 30_000 })
    await expect(lapsed).toContainText('nothing ran')
    await expect.poll(async () => (await cluster.auditLog()).some((r) => r.action === 'watson.lapse' && JSON.stringify(r).includes(`kubectl cordon ${node}`)), { timeout: 15_000 }).toBe(true)
    // A new card for the same command, against the node as it is now.
    await lapsed.getByTestId('watson-again').click()
    const again = pane(page).getByTestId('watson-card').last()
    await expect(again).toHaveAttribute('data-command', `kubectl cordon ${node}`)
    await expect(again.getByTestId('watson-impact')).toContainText('already', { timeout: 30_000 })
    await again.getByTestId('watson-discard').click()
  } finally {
    await cluster.cordon(node, false)
  }
})

test('a read-only connection runs reads and refuses changes, naming the rule (P6-05)', async ({ page, request, browserName }) => {
  const other = await cluster.other()
  const id = `${browserName}-watson-ro`
  await request.delete(`/api/v1/clusters/${id}`, { headers: SAME })
  const added = await request.post('/api/v1/clusters', { data: { name: id, env: 'staging', description: 'Watson read-only test', server: other.server, ca: other.ca, caPem: other.caPem, auth: 'token', token: other.token, mode: 'read' }, headers: SAME })
  expect(added.status(), await added.text()).toBe(201)
  try {
    await expect.poll(async () => ((await (await request.get(`/api/v1/clusters/${id}`)).json()) as { state: string }).state, { timeout: 30_000 }).toBe('connected')
    // Watson shows only where Sherlock is on; settings for a removed connection are dropped with it.
    const on = await request.put('/api/v1/sherlock/config', { data: { ...SHERLOCK_ON, clusters: { local: true, [id]: true } }, headers: SAME })
    expect(on.status(), await on.text()).toBe(200)
    await page.goto(`/#/c/${id}/nodes`)
    const statusNow = await (await request.get(`/api/v1/clusters/${id}/watson`)).text()
    await expect(pane(page).getByTestId('watson-readonly'), `watson status: ${statusNow.slice(0, 400)}`).toContainText('read-only')
    await pane(page).getByTestId('watson-input').fill('kubectl get nodes -o name')
    await pane(page).getByTestId('watson-input').press('Enter')
    await expect(lastRan(page)).toHaveAttribute('data-status', 'done', { timeout: 30_000 })
    const node = (await lastRan(page).getByTestId('watson-output').innerText()).split('\n')[0]!.replace('node/', '')
    await pane(page).getByTestId('watson-input').fill(`kubectl cordon ${node}`)
    await pane(page).getByTestId('watson-input').press('Enter')
    const refused = pane(page).getByTestId('watson-refused').last()
    await expect(refused).toContainText('read-only cluster')
    await expect(refused.getByTestId('watson-reason')).toContainText('Read and change')
    await expect.poll(async () => (await cluster.auditLog()).some((r) => r.action === 'watson.refuse' && JSON.stringify(r).includes(`"cluster":"${id}"`)), { timeout: 15_000 }).toBe(true)
  } finally {
    await request.delete(`/api/v1/clusters/${id}`, { headers: SAME })
  }
})

test('a change runs as the person who approves it, so their own access decides (P6-05)', async ({ browser, request }) => {
  await cluster.limitDev()
  const node = await spareWorker(request)
  const dev = await signInAs(browser, E2E_DEV)
  try {
    await expect(dev.getByTestId('cluster-state')).toHaveText('Connected', { timeout: 30_000 })
    const can = inKind
      ? await kubectl('auth', 'can-i', 'patch', 'nodes', `--as=${E2E_DEV_KUBE}`, '--as-group=221b:console-users', '--as-group=system:authenticated').catch((e: unknown) => String(e))
      : 'n/a'
    const it = await watson(dev.request, `kubectl cordon ${node}`)
    expect(it.status, `can-i patch nodes as ${E2E_DEV_KUBE}: ${can} ${JSON.stringify({ status: it.status, exit: it.exit, reason: it.reason, plan: it.plan?.dryRun })} ${text(it)}`).toBe('failed')
    expect(text(it, 'err')).toMatch(/forbidden/i)
    expect((await cluster.getObject<{ spec: { unschedulable?: boolean } }>(`/api/v1/nodes/${node}`))!.spec.unschedulable ?? false).toBe(false)
  } finally {
    await dev.context().close()
  }
})

test('on a production cluster with the rule on, a second person approves (P6-07)', async ({ page, browser }) => {
  const local = (await (await page.request.get('/api/v1/clusters/local')).json()) as { name: string; env: string; description: string }
  const setEnv = (env: string) => page.request.put('/api/v1/clusters/local', { data: { name: local.name, env, description: local.description, ca: 'system', auth: 'token', mode: 'read' }, headers: SAME })
  const node = await spareWorker(page.request)
  const revoke = await cluster.allowNodeChanges(E2E_VIEWER_KUBE)
  let viewer: Page | undefined
  try {
    expect((await setEnv('production')).status()).toBe(200)
    await open(page)
    await page.getByTestId('admin-link').click()
    await page.locator('[data-admin-page=settings]').click()
    await page.locator('[data-testid=settings-section][data-section=watson]').click()
    await page.getByTestId('watson-settings').getByTestId('watson-two-person').check()
    await page.getByTestId('watson-settings').getByTestId('watson-settings-save').click()
    await expect(page.getByTestId('watson-settings').getByText('Saved.')).toBeVisible()
    await page.keyboard.press('Escape')

    await pane(page).getByTestId('watson-input').fill(`kubectl cordon ${node}`)
    await pane(page).getByTestId('watson-input').press('Enter')
    const card = pane(page).getByTestId('watson-card').last()
    await expect(card.getByTestId('watson-waiting')).toContainText('second person')
    await expect(card.getByTestId('watson-approve')).toHaveCount(0)

    viewer = await signInAs(browser, E2E_VIEWER)
    await expect(viewer.getByTestId('watson')).toBeVisible({ timeout: 30_000 })
    const theirs = viewer.getByTestId('watson').locator(`[data-testid=watson-card][data-command="kubectl cordon ${node}"]`)
    await expect(theirs).toContainText('asks for approval')
    await expect(theirs.getByTestId('watson-discard')).toHaveCount(0)
    await theirs.getByTestId('watson-approve').click()
    // It runs as the viewer, and the requester sees it run.
    await expect(lastRan(page)).toContainText(`node/${node} cordoned`, { timeout: 30_000 })
    await expect(lastRan(page).getByTestId('watson-status')).toContainText('approved by')
    await expect
      .poll(async () => (await cluster.auditLog()).some((r) => r.action === 'watson.approve' && JSON.stringify(r).includes(E2E_VIEWER) && JSON.stringify(r).includes(`kubectl cordon ${node}`)), { timeout: 15_000 })
      .toBe(true)
  } finally {
    await viewer?.context().close()
    await page.request.put('/api/v1/watson/config', { data: { twoPerson: false }, headers: SAME })
    await setEnv(local.env)
    await revoke()
    await cluster.cordon(node, false)
  }
})

test('a helm rollback asks on a card with what changes; the Helm tab has no buttons for it (P6-04, Phase 6 review)', async ({ page }) => {
  test.skip(!play.release, 'no release with more than one revision')
  const [ns, name] = inKind ? [play.namespace, play.release] : play.release.split('/')
  await open(page, `#/helm/${ns}/${name}`)
  await expect(page.getByTestId('app-panel').getByTestId('app-title')).toContainText(name!)
  // Removed at the Phase 6 review: no Roll back and no Ask Sherlock on a release; the Phase 10 drawer's wrench offers Upgrade… and History a rollback icon per revision, each a Watson card.
  await expect(page.getByTestId('release-rollback')).toHaveCount(0)
  await expect(page.getByTestId('ask-sherlock')).toHaveCount(0)
  await pane(page).getByTestId('watson-input').fill(`helm rollback ${name} 1 -n ${ns}`)
  await pane(page).getByTestId('watson-input').press('Enter')
  const card = pane(page).getByTestId('watson-card').last()
  await expect(card).toHaveAttribute('data-command', `helm rollback ${name} 1 -n ${ns}`)
  await expect(card.getByTestId('watson-targets')).toContainText(`release ${ns}/${name}`)
  await expect(card.getByTestId('watson-impact')).toContainText(/revision/i, { timeout: 30_000 })
  await expect(card.getByTestId('watson-dryrun')).toContainText(/dry run/i, { timeout: 30_000 })
  await card.getByTestId('watson-discard').click()
})

test('Watson moves to a window of its own, with Sherlock, approvals there run, and it docks back (P6-09)', async ({ page, context }) => {
  await open(page)
  const [popup] = await Promise.all([context.waitForEvent('page'), page.locator('[data-testid=dock-popout]:visible').click()])
  await popup.waitForLoadState('domcontentloaded')
  await expect(popup).toHaveURL(/#\/assistants$/)
  await expect(popup.getByTestId('watson')).toBeVisible()
  await expect(page.getByTestId('watson')).toHaveCount(0)
  await expect(page.getByTestId('assistants-popped')).toBeVisible()

  // A change typed in the window runs there.
  const node = await spareWorker(page.request)
  try {
    await popup.getByTestId('watson-input').fill(`kubectl cordon ${node}`)
    await popup.getByTestId('watson-input').press('Enter')
    const card = popup.getByTestId('watson-card').last()
    await expect(card).toHaveAttribute('data-command', `kubectl cordon ${node}`)
    await card.getByTestId('watson-approve').click()
    await expect(popup.getByTestId('watson-ran').last()).toContainText(`node/${node} cordoned`, { timeout: 30_000 })
  } finally {
    await cluster.cordon(node, false)
  }

  // Docking it back closes the window; the commands are in the panel.
  await Promise.all([popup.waitForEvent('close'), popup.locator('[data-testid=assistants-dock-back]:visible').click().catch(() => undefined)])
  await expect(pane(page)).toBeVisible({ timeout: 10_000 })
  await expect(lastRan(page)).toContainText(`node/${node} cordoned`)
})

test('the panel meets WCAG 2.2 AA, with a card and output, in both themes (X-12)', async ({ page }) => {
  const TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa']
  for (const theme of ['light', 'dark'] as const) {
    await page.addInitScript((t) => window.localStorage.setItem('221b.theme', JSON.stringify(t)), theme)
    await open(page)
    await pane(page).getByTestId('watson-input').fill('kubectl get nodes')
    await pane(page).getByTestId('watson-input').press('Enter')
    await expect(lastRan(page)).toHaveAttribute('data-status', 'done', { timeout: 30_000 })
    await pane(page).getByTestId('watson-input').fill(`kubectl scale deployment/${play.deployment} -n ${play.namespace} --replicas=2`)
    await pane(page).getByTestId('watson-input').press('Enter')
    await expect(pane(page).getByTestId('watson-card').last().getByTestId('watson-dryrun')).toBeVisible({ timeout: 30_000 })
    await page.evaluate(() => Promise.allSettled(document.getAnimations().map((a) => a.finished)))
    const r = await new AxeBuilder({ page }).include('[data-testid=watson]').withTags(TAGS).analyze()
    expect(r.violations.map((v) => `${v.id}: ${v.help} — ${v.nodes.slice(0, 3).map((n) => n.target.join(' ')).join(', ')}`), theme).toEqual([])
    await pane(page).getByTestId('watson-card').last().getByTestId('watson-discard').click()
  }
})

test('a standing approval approves cards as they are ready, within the limit the settings set (R2-04)', async ({ page, request }) => {
  const node = await spareWorker(page.request)
  const config = async (approveAllMinutes: number) => expect((await request.put('/api/v1/watson/config', { data: { twoPerson: false, approveAllMinutes }, headers: SAME })).status()).toBe(200)
  // Off by default: no control, and the API refuses.
  await open(page, '#/nodes')
  expect((await request.post('/api/v1/watson/approvals', { data: { minutes: 5 }, headers: SAME })).status()).toBe(409)
  await config(30)
  try {
    await page.reload()
    await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
    // The control sits on the card, next to Approve and run; picking a length approves this card and the next ones.
    await pane(page).getByTestId('watson-input').fill(`kubectl cordon ${node}`)
    await pane(page).getByTestId('watson-input').press('Enter')
    const first = pane(page).getByTestId('watson-card').last()
    const pick = first.getByTestId('watson-approve-all')
    await expect(pick).toBeVisible()
    await expect(pick.locator('option')).toHaveCount(4) // the empty one, 5, 15 and 30 minutes
    await pick.selectOption('5')
    await expect(pane(page).getByTestId('watson-standing-header')).toContainText('approving all until')
    const ran = lastRan(page)
    await expect(ran).toHaveAttribute('data-status', 'done', { timeout: 30_000 })
    await expect(ran.getByTestId('watson-output')).toHaveText(`node/${node} cordoned`)
    await expect(ran.getByTestId('watson-status')).toContainText('approved by')
    await expect
      .poll(async () => (await cluster.auditLog()).filter((r) => r.action === 'watson.approveAll' || (r.action === 'watson.approve' && JSON.stringify(r).includes(`kubectl cordon ${node}`))).length, { timeout: 15_000 })
      .toBeGreaterThanOrEqual(2)
    // Stop ends it: the next card waits, with the control back on it.
    await pane(page).getByTestId('watson-standing-end-header').click()
    await expect(pane(page).getByTestId('watson-standing-header')).toHaveCount(0)
    await pane(page).getByTestId('watson-input').fill(`kubectl uncordon ${node}`)
    await pane(page).getByTestId('watson-input').press('Enter')
    const card = pane(page).getByTestId('watson-card').last()
    await expect(card.getByTestId('watson-approve')).toBeEnabled({ timeout: 30_000 })
    await expect(card.getByTestId('watson-approve-all')).toBeVisible()
    await card.getByTestId('watson-approve').click()
    await expect(lastRan(page)).toContainText(`node/${node} uncordoned`, { timeout: 30_000 })
  } finally {
    await config(0)
    await cluster.cordon(node, false)
  }
})

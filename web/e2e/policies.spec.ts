// The Policies tab (P4-04 to P4-06), checked against the cluster itself:
// connections the network plugin allows or drops, server-side dry-run
// drains, and the API server's Pod Security warnings for a dry-run label
// change.
import type { APIRequestContext, Page } from '@playwright/test'
import { expect, test } from './test'
import { cluster, healthyWorkers, inKind, kubectl, kubectlExecOK, kubectlResult, nodeRows, sleep, snapshot, type KubeObject } from './cluster'
import type { DrainRow, NetCheckResult, PolicyNSRow, QuotaRow, RolloutRow } from '../src/api/types'

const BUSYBOX = 'registry.k8s.io/e2e-test-images/busybox:1.36.1-1'
let ipSeq = 10

/** A pod; the simulator also gets the status a running pod has. */
function pod(ns: string, name: string, labels: Record<string, string>, spec: Record<string, unknown> = {}, opts: { node?: string; owner?: string } = {}): KubeObject {
  const ip = `10.244.200.${ipSeq++}`
  return {
    apiVersion: 'v1',
    kind: 'Pod',
    metadata: { name, namespace: ns, labels: { e2e: 'policies', ...labels }, ...(opts.owner ? { ownerReferences: [{ apiVersion: 'apps/v1', kind: 'ReplicaSet', name: opts.owner, uid: `e2e-${opts.owner}`, controller: true }] } : {}) },
    spec: { terminationGracePeriodSeconds: 0, containers: [{ name: 'main', image: BUSYBOX, command: ['sh', '-c', 'sleep 3600'] }], ...(opts.node ? { nodeName: opts.node } : {}), ...spec },
    ...(inKind ? {} : { status: { phase: 'Running', podIP: ip, podIPs: [{ ip }], hostIP: '192.168.0.2', conditions: [{ type: 'Ready', status: 'True' }], containerStatuses: [{ name: 'main', ready: true, restartCount: 0, image: BUSYBOX, imageID: '', state: { running: { startedAt: new Date().toISOString() } } }] } }),
  }
}

const ns = (name: string, labels: Record<string, string> = {}): KubeObject => ({ apiVersion: 'v1', kind: 'Namespace', metadata: { name, labels: { 'kubernetes.io/metadata.name': name, e2e: 'policies', ...labels } } })

async function policyRows<R>(request: APIRequestContext, prefix: string): Promise<R[]> {
  const snap = await snapshot(request, ['policies'])
  return ((snap.views.policies?.rows ?? []) as { key: string }[]).filter((r) => r.key.startsWith(prefix + ':')) as R[]
}

async function podIP(namespace: string, name: string): Promise<string> {
  return (await kubectl('-n', namespace, 'get', 'pod', name, '-o', 'jsonpath={.status.podIP}')).trim()
}

async function openPolicies(page: Page, path = '') {
  await page.goto('/#/policies' + path)
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
}

test.describe('network policies', () => {
  const A = 'e2e-np-a'
  const B = 'e2e-np-b'
  const tcp = { protocol: 'TCP', port: 8080 }
  const objs: KubeObject[] = [
    ns(A, { team: 'a' }),
    ns(B, { team: 'b' }),
    pod(A, 'server', { app: 'server' }, { containers: [{ name: 'main', image: BUSYBOX, command: ['sh', '-c', 'echo ok > /tmp/index.html && httpd -f -p 8080 -h /tmp'], ports: [{ name: 'http', containerPort: 8080 }] }] }),
    pod(A, 'client', { app: 'client' }),
    pod(A, 'stranger', { app: 'stranger' }),
    pod(B, 'client', { app: 'client' }),
    pod(B, 'other', { app: 'other' }),
    { apiVersion: 'networking.k8s.io/v1', kind: 'NetworkPolicy', metadata: { name: 'default-deny', namespace: A }, spec: { podSelector: {}, policyTypes: ['Ingress'] } },
    {
      apiVersion: 'networking.k8s.io/v1',
      kind: 'NetworkPolicy',
      metadata: { name: 'server-clients', namespace: A },
      spec: { podSelector: { matchLabels: { app: 'server' } }, ingress: [{ from: [{ podSelector: { matchLabels: { app: 'client' } } }, { namespaceSelector: { matchLabels: { team: 'b' } }, podSelector: { matchLabels: { app: 'client' } } }], ports: [tcp] }] },
    },
    { apiVersion: 'networking.k8s.io/v1', kind: 'NetworkPolicy', metadata: { name: 'dns-only', namespace: B }, spec: { podSelector: { matchLabels: { app: 'other' } }, policyTypes: ['Egress'], egress: [{ ports: [{ protocol: 'UDP', port: 53 }, { protocol: 'TCP', port: 53 }] }] } },
  ]
  // [from namespace, pod, allowed, the policies that decide]
  const CASES: [string, string, boolean, string][] = [
    [A, 'client', true, 'server-clients'],
    [B, 'client', true, 'server-clients'],
    [A, 'stranger', false, 'default-deny'],
    [B, 'other', false, 'dns-only'],
  ]
  let cleanup: () => Promise<void>

  test.beforeAll(async () => {
    cleanup = await cluster.applyObjects(objs)
    if (inKind) {
      for (const n of [A, B]) await kubectl('-n', n, 'wait', '--for=condition=Ready', 'pod', '--all', '--timeout=180s')
    }
  })
  test.afterAll(async () => {
    await cleanup?.()
  })

  test('answers match real connections', async ({ request }) => {
    const ip = inKind ? await podIP(A, 'server') : ''
    for (const [from, name, want, decides] of CASES) {
      const res = await request.get(`/api/v1/netcheck?from=${from}/${name}&to=${A}/server&port=8080&protocol=TCP`)
      expect(res.status()).toBe(200)
      const body = (await res.json()) as NetCheckResult
      const c = body.checks[0]!
      expect(c.allowed, `${from}/${name} → ${A}/server:8080`).toBe(want)
      expect([...c.egress.selecting, ...c.ingress.selecting].join(',')).toContain(decides)
      if (!inKind) continue
      // The plugin takes a moment to apply new policies: wait until the
      // real connection agrees, and fail if it never does.
      let real = !want
      const deadline = Date.now() + 45_000
      while (Date.now() < deadline) {
        real = await kubectlExecOK(from, name, 'wget', '-q', '-T', '3', '-O', '-', `http://${ip}:8080/`)
        if (real === want) break
        await sleep(2_000)
      }
      expect(real, `a real connection from ${from}/${name} to ${A}/server:8080`).toBe(want)
    }
  })

  test('the tab lists the policies and how isolated each namespace is', async ({ page }) => {
    await openPolicies(page, '/netpol')
    await expect(page.locator(`[data-netpol="np:${A}/server-clients"]`)).toContainText('app=server')
    await expect(page.locator(`[data-ns="${A}"]`)).toContainText('3 of 3')
    await expect(page.locator(`[data-ns="${B}"]`)).toContainText('0 of 2')
    await page.locator(`[data-netpol="np:${A}/server-clients"]`).click()
    const panel = page.getByTestId('policy-panel')
    await expect(panel).toContainText('from pods with app=client in e2e-np-a or pods with app=client in namespaces with team=b on TCP 8080')
    await expect(panel.getByTestId('panel-item')).toHaveCount(1)
  })
})

test.describe('drains', () => {
  const N = 'e2e-drain'
  let cleanup: () => Promise<void>
  let nodeA = ''
  let nodeB = ''

  test.beforeAll(async ({ request }) => {
    const workers = healthyWorkers(await snapshot(request)).map((r) => r.name)
    ;[nodeA = '', nodeB = ''] = workers
    const objs: KubeObject[] = [ns(N)]
    if (inKind) {
      objs.push({ apiVersion: 'apps/v1', kind: 'Deployment', metadata: { name: 'guarded', namespace: N }, spec: { replicas: 1, selector: { matchLabels: { app: 'guarded' } }, template: { metadata: { labels: { app: 'guarded' } }, spec: { nodeName: nodeA, terminationGracePeriodSeconds: 0, containers: [{ name: 'main', image: BUSYBOX, command: ['sh', '-c', 'sleep 3600'] }] } } } })
    } else {
      objs.push(pod(N, 'guarded-1', { app: 'guarded' }, {}, { node: nodeA, owner: 'guarded-abc' }))
    }
    objs.push(pod(N, 'bare', { app: 'bare' }, {}, { node: nodeB }))
    objs.push({ apiVersion: 'policy/v1', kind: 'PodDisruptionBudget', metadata: { name: 'guarded', namespace: N }, spec: { minAvailable: 1, selector: { matchLabels: { app: 'guarded' } } }, ...(inKind ? {} : { status: { currentHealthy: 1, desiredHealthy: 1, expectedPods: 1, disruptionsAllowed: 0 } }) })
    cleanup = await cluster.applyObjects(objs)
    if (inKind) {
      await kubectl('-n', N, 'wait', '--for=condition=Ready', 'pod', '--all', '--timeout=180s')
      // The disruption controller fills in the budget's status.
      await expect.poll(async () => (await kubectl('-n', N, 'get', 'pdb', 'guarded', '-o', 'jsonpath={.status.expectedPods}')).trim(), { timeout: 60_000 }).toBe('1')
    }
  })
  test.afterAll(async () => {
    await cleanup?.()
  })

  test('verdicts match a server-side dry-run drain of each node', async ({ request, page }) => {
    let rows: DrainRow[] = []
    await expect
      .poll(async () => {
        rows = await policyRows<DrainRow>(request, 'drain')
        const a = rows.find((r) => r.node === nodeA)
        const b = rows.find((r) => r.node === nodeB)
        return `${a?.verdict} ${a?.why.includes(`${N}/guarded allows no disruption now`)} ${b?.verdict} ${b?.why.includes(`${N}/bare: it has no controller`)}`
      }, { timeout: 30_000 })
      .toBe('blocked true blocked true')
    if (inKind) {
      for (const n of nodeRows(await snapshot(request))) {
        const ours = rows.find((r) => r.node === n.name)!
        const dry = await kubectlResult('drain', n.name, '--ignore-daemonsets', '--delete-emptydir-data', '--dry-run=server', '--timeout=10s')
        expect(!dry.ok, `kubectl drain ${n.name} --dry-run=server: ${dry.err.slice(0, 600)}`).toBe(ours.verdict === 'blocked')
        if (ours.verdict === 'blocked') for (const b of ours.blockers) expect(dry.out + dry.err).toContain(b.name)
      }
    }
    await openPolicies(page, '/pdb')
    await expect(page.locator(`[data-drain="${nodeA}"]`).getByTestId('drain-verdict')).toHaveText('Blocked')
    await expect(page.locator(`[data-pdb="pdb:${N}/guarded"]`)).toContainText('minAvailable 1')
  })
})

test.describe('pod security, quotas, webhooks and priority classes', () => {
  const P = 'e2e-psa'
  const Q = 'e2e-quota'
  let cleanup: () => Promise<void>
  const objs: KubeObject[] = [
    ns(P),
    ns(Q),
    pod(P, 'priv', { app: 'priv' }, { containers: [{ name: 'main', image: BUSYBOX, command: ['sh', '-c', 'sleep 3600'], securityContext: { privileged: true } }] }),
    pod(P, 'hostpath', { app: 'hostpath' }, { volumes: [{ name: 'proc', hostPath: { path: '/proc' } }] }),
    pod(P, 'hostpath-2', { app: 'hostpath' }, { volumes: [{ name: 'proc', hostPath: { path: '/proc' } }] }),
    pod(P, 'hostnet', { app: 'hostnet' }, { hostNetwork: true, containers: [{ name: 'main', image: BUSYBOX, command: ['sh', '-c', 'sleep 3600'], ports: [{ containerPort: 18089, hostPort: 18089 }] }] }),
    pod(P, 'plain', { app: 'plain' }),
    pod(P, 'hardened', { app: 'hardened' }, { securityContext: { runAsNonRoot: true, runAsUser: 65534, seccompProfile: { type: 'RuntimeDefault' } }, containers: [{ name: 'main', image: BUSYBOX, command: ['sh', '-c', 'sleep 3600'], securityContext: { allowPrivilegeEscalation: false, capabilities: { drop: ['ALL'] } } }] }),
    { apiVersion: 'v1', kind: 'ResourceQuota', metadata: { name: 'mem', namespace: Q }, spec: { hard: { 'requests.memory': '150Mi', pods: '5' } }, ...(inKind ? {} : { status: { hard: { 'requests.memory': '150Mi', pods: '5' }, used: { 'requests.memory': '100Mi', pods: '1' } } }) },
    { apiVersion: 'apps/v1', kind: 'Deployment', metadata: { name: 'app', namespace: Q }, spec: { replicas: 1, selector: { matchLabels: { app: 'app' } }, template: { metadata: { labels: { app: 'app' } }, spec: { terminationGracePeriodSeconds: 0, containers: [{ name: 'main', image: BUSYBOX, command: ['sh', '-c', 'sleep 3600'], resources: { requests: { memory: '100Mi' } } }] } } } },
    ...(inKind ? [] : [pod(Q, 'app-1', { app: 'app' }, { containers: [{ name: 'main', image: BUSYBOX, resources: { requests: { memory: '100Mi' } } }] }, { owner: 'app-abc' })]),
    {
      apiVersion: 'admissionregistration.k8s.io/v1',
      kind: 'ValidatingWebhookConfiguration',
      metadata: { name: 'e2e-hook' },
      webhooks: [{ name: 'never.e2e.example', clientConfig: { service: { namespace: Q, name: 'nothing', port: 443 } }, rules: [{ operations: ['CREATE'], apiGroups: [''], apiVersions: ['v1'], resources: ['configmaps'] }], objectSelector: { matchLabels: { 'e2e-never': 'yes' } }, failurePolicy: 'Ignore', sideEffects: 'None', admissionReviewVersions: ['v1'], timeoutSeconds: 2 }],
    },
    { apiVersion: 'scheduling.k8s.io/v1', kind: 'PriorityClass', metadata: { name: 'e2e-low' }, value: -5, preemptionPolicy: 'Never', description: 'For the end-to-end tests.' },
  ]

  test.beforeAll(async () => {
    cleanup = await cluster.applyObjects(objs)
  })
  test.afterAll(async () => {
    await cleanup?.()
  })

  test('Pod Security matches the API server’s warnings for a dry-run label change', async ({ request, page }) => {
    let row: PolicyNSRow | undefined
    await expect
      .poll(async () => {
        row = (await policyRows<PolicyNSRow>(request, 'ns')).find((r) => r.name === P)
        return row?.restricted.fail.length
      }, { timeout: 30_000 })
      .toBe(5)
    for (const level of ['baseline', 'restricted'] as const) {
      const ours = row![level].warnings
      if (inKind) {
        const dry = await kubectlResult('label', 'namespace', P, `pod-security.kubernetes.io/enforce=${level}`, '--overwrite', '--dry-run=server')
        const theirs = dry.err
          .split('\n')
          .map((l) => l.trim())
          .filter((l) => l.startsWith('Warning: '))
          .map((l) => l.slice('Warning: '.length))
        expect(ours, `kubectl label --dry-run=server … enforce=${level}`).toEqual(theirs)
      } else {
        expect(ours[0]).toBe(`existing pods in namespace "${P}" violate the new PodSecurity enforce level "${level}:latest"`)
      }
    }
    expect(row!.baseline.warnings).toContain('hostpath (and 1 other pod): hostPath volumes')
    expect(row!.baseline.warnings).toContain('hostnet: host namespaces, hostPort')
    expect(row!.baseline.warnings).toContain('priv: privileged')
    // The namespace's drawer shows the same lines.
    await openPolicies(page, '/psa')
    await page.locator(`[data-psa="${P}"]`).click()
    await expect(page.getByTestId('psa-warnings-baseline')).toContainText(`Warning: hostpath (and 1 other pod): hostPath volumes`)
  })

  test('quotas show what the API server counts, and a rollout that would stall', async ({ request, page }) => {
    let q: QuotaRow | undefined
    await expect
      .poll(async () => {
        q = (await policyRows<QuotaRow>(request, 'quota')).find((r) => r.key === `quota:${Q}/mem`)
        return q?.items.find((i) => i.resource === 'requests.memory')?.used
      }, { timeout: 60_000 })
      .toBe('100Mi')
    if (inKind) expect((await kubectl('-n', Q, 'get', 'resourcequota', 'mem', '-o', 'jsonpath={.status.used.requests\\.memory}')).trim()).toBe('100Mi')
    const r = (await policyRows<RolloutRow>(request, 'rollout')).find((x) => x.key === `rollout:${Q}/app`)!
    expect(r.stalls).toBe(true)
    expect(r.short).toEqual([{ quota: 'mem', resource: 'requests.memory', need: '100Mi', left: '50Mi' }])
    await openPolicies(page, '/quota')
    await expect(page.locator(`[data-quota="quota:${Q}/mem"] [data-resource="requests.memory"]`)).toContainText('100Mi of 150Mi')
    await expect(page.getByTestId('policies-review').getByTestId('finding').filter({ hasText: 'A rolling update of app would stall on mem' })).toBeVisible()
  })

  test('a webhook whose Service is missing, and a priority class', async ({ page }) => {
    await openPolicies(page, '/webhooks')
    const hook = page.locator('[data-hook="hook:Validating/e2e-hook/never.e2e.example"]')
    await expect(hook.getByTestId('hook-reach')).toHaveText('Service not found')
    await expect(hook).toContainText('Ignore')
    await expect(page.getByTestId('policies-review').getByTestId('finding').filter({ hasText: 'never.e2e.example is skipped' })).toBeVisible()
    await openPolicies(page, '/prio')
    await expect(page.locator('[data-pc="pc:e2e-low"]')).toContainText('-5')
    await expect(page.locator('[data-pc="pc:e2e-low"]')).toContainText('Never')
  })
})

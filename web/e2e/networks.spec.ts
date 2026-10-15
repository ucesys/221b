// The Networks tab (P4-02): the trace of a request to an Ingress host,
// broken one hop at a time. In kind the "controller" is a pod carrying
// ingress-nginx's labels, so the network-policy hop is checked against a
// real connection from it to the backend pod.
import { execFile } from 'node:child_process'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { promisify } from 'node:util'
import type { APIRequestContext } from '@playwright/test'
import { expect, test } from './test'
import { cluster, inKind, kubectl, kubectlExecOK, sleep, snapshot, type KubeObject } from './cluster'
import type { TraceRow } from '../src/api/types'

const run = promisify(execFile)
const BUSYBOX = 'registry.k8s.io/e2e-test-images/busybox:1.36.1-1'
const N = 'e2e-net'
const C = 'e2e-net-ctl'
const HOST = 'web.e2e.test'
const KEY = `route:${N}/web/0/0`
const ADDRESS = '192.0.2.10'
const CTL_LABELS = { 'app.kubernetes.io/name': 'ingress-nginx', 'app.kubernetes.io/component': 'controller' }

let ipSeq = 10
function pod(ns: string, name: string, labels: Record<string, string>, container: Record<string, unknown>): KubeObject {
  const ip = `10.244.201.${ipSeq++}`
  return {
    apiVersion: 'v1',
    kind: 'Pod',
    metadata: { name, namespace: ns, labels: { e2e: 'networks', ...labels }, ...(inKind ? {} : { ownerReferences: [{ apiVersion: 'apps/v1', kind: 'ReplicaSet', name: `${name}-abc`, uid: `e2e-${name}`, controller: true }] }) },
    spec: { terminationGracePeriodSeconds: 0, containers: [{ name: 'main', image: BUSYBOX, ...container }] },
    ...(inKind ? {} : { status: { phase: 'Running', podIP: ip, podIPs: [{ ip }], hostIP: '192.168.0.2', conditions: [{ type: 'Ready', status: 'True' }], containerStatuses: [{ name: 'main', ready: true, restartCount: 0, image: BUSYBOX, imageID: '', state: { running: { startedAt: new Date().toISOString() } } }] } }),
  }
}

const ns = (name: string): KubeObject => ({ apiVersion: 'v1', kind: 'Namespace', metadata: { name, labels: { 'kubernetes.io/metadata.name': name, e2e: 'networks' } } })
const server = pod(N, 'web-1', { app: 'web' }, { command: ['sh', '-c', 'echo ok > /tmp/index.html && httpd -f -p 8080 -h /tmp'], ports: [{ name: 'http', containerPort: 8080 }] })
const controller = pod(C, 'controller', CTL_LABELS, { command: ['sh', '-c', 'touch /tmp/ready && sleep 3600'], readinessProbe: { exec: { command: ['cat', '/tmp/ready'] }, periodSeconds: 2, failureThreshold: 1 } })

function service(selector: Record<string, string>): KubeObject {
  return { apiVersion: 'v1', kind: 'Service', metadata: { name: 'web', namespace: N }, spec: { selector, ports: [{ name: 'http', port: 80, targetPort: 'http', protocol: 'TCP' }] } }
}

/** The Service's endpoints, which the simulator has no controller to fill in. */
function slice(ready: boolean): KubeObject[] {
  if (inKind) return []
  const ip = (server.status as { podIP: string }).podIP
  return [
    {
      apiVersion: 'discovery.k8s.io/v1',
      kind: 'EndpointSlice',
      metadata: { name: 'web-e2e', namespace: N, labels: { 'kubernetes.io/service-name': 'web' } },
      addressType: 'IPv4',
      ports: [{ name: 'http', port: 8080, protocol: 'TCP' }],
      endpoints: ready ? [{ addresses: [ip], conditions: { ready: true }, targetRef: { kind: 'Pod', namespace: N, name: 'web-1' } }] : [],
    },
  ]
}

function ingress(backend: { name: string; port: Record<string, unknown> }): KubeObject {
  return {
    apiVersion: 'networking.k8s.io/v1',
    kind: 'Ingress',
    metadata: { name: 'web', namespace: N },
    spec: { ingressClassName: 'e2e-nginx', tls: [{ hosts: [HOST], secretName: 'web-tls' }], rules: [{ host: HOST, http: { paths: [{ path: '/', pathType: 'Prefix', backend: { service: backend } }] } }] },
    ...(inKind ? {} : { status: { loadBalancer: { ingress: [{ ip: ADDRESS }] } } }),
  }
}
const BACKEND = { name: 'web', port: { name: 'http' } }

/** A certificate made by openssl: valid for 30 days, or expired yesterday. */
async function certificate(cn: string, expired = false): Promise<{ crt: string; key: string }> {
  const dir = await mkdtemp(join(tmpdir(), 'e2e-cert-'))
  try {
    const key = join(dir, 'k.pem')
    const crt = join(dir, 'c.pem')
    if (expired) {
      await run('openssl', ['req', '-new', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:prime256v1', '-nodes', '-keyout', key, '-subj', `/CN=${cn}`, '-out', join(dir, 'r.csr')])
      await run('openssl', ['x509', '-req', '-in', join(dir, 'r.csr'), '-key', key, '-days', '-1', '-out', crt])
    } else {
      await run('openssl', ['req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:prime256v1', '-nodes', '-keyout', key, '-out', crt, '-days', '30', '-subj', `/CN=${cn}`, '-addext', `subjectAltName=DNS:${cn}`])
    }
    return { crt: await readFile(crt, 'utf8'), key: await readFile(key, 'utf8') }
  } finally {
    await rm(dir, { recursive: true, force: true })
  }
}

function tlsSecret(c: { crt: string; key: string }): KubeObject {
  const b64 = (s: string) => Buffer.from(s).toString('base64')
  return { apiVersion: 'v1', kind: 'Secret', metadata: { name: 'web-tls', namespace: N }, type: 'kubernetes.io/tls', data: { 'tls.crt': b64(c.crt), 'tls.key': b64(c.key) } }
}

async function put(...objs: KubeObject[]): Promise<() => Promise<void>> {
  return cluster.applyObjects(objs)
}

/** The Ingress's published address: kind has no controller to set it. */
async function setAddress(on: boolean): Promise<void> {
  if (!inKind) {
    const ing = ingress(BACKEND)
    if (!on) delete ing.status
    await put(ing)
    return
  }
  await kubectl('-n', N, 'patch', 'ingress', 'web', '--subresource=status', '--type=merge', '-p', JSON.stringify({ status: { loadBalancer: { ingress: on ? [{ ip: ADDRESS }] : null } } }))
}

async function route(request: APIRequestContext): Promise<TraceRow | undefined> {
  const snap = await snapshot(request, ['networks'])
  return ((snap.views.networks?.rows ?? []) as TraceRow[]).find((r) => r.key === KEY)
}

/** Waits for the trace to settle on a verdict and a hop state. */
async function expectHop(request: APIRequestContext, hop: string, state: string | RegExp, verdict: string): Promise<TraceRow> {
  let row: TraceRow | undefined
  const want = typeof state === 'string' ? state.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') : state.source
  await expect
    .poll(
      async () => {
        row = await route(request)
        const h = row?.hops.find((x) => x.k === hop)
        return `${row?.verdict} · ${hop}: ${h?.s}`
      },
      { timeout: 60_000 },
    )
    .toMatch(new RegExp(`^${verdict} · ${hop}: ${want}$`))
  return row!
}

/** A real request from the controller pod to the backend pod (kind only), retried while the plugin catches up. */
async function realConnection(want: boolean): Promise<boolean> {
  const ip = (await kubectl('-n', N, 'get', 'pod', 'web-1', '-o', 'jsonpath={.status.podIP}')).trim()
  let got = !want
  const deadline = Date.now() + 45_000
  while (Date.now() < deadline) {
    got = await kubectlExecOK(C, 'controller', 'wget', '-q', '-T', '3', '-O', '-', `http://${ip}:8080/`)
    if (got === want) break
    await sleep(2_000)
  }
  return got
}

test.describe('tracing a request', () => {
  test.describe.configure({ mode: 'serial' })
  let cleanup: () => Promise<void>
  const extra: (() => Promise<void>)[] = []

  test.beforeAll(async () => {
    const good = await certificate(HOST)
    cleanup = await put(
      ns(N),
      ns(C),
      { apiVersion: 'networking.k8s.io/v1', kind: 'IngressClass', metadata: { name: 'e2e-nginx' }, spec: { controller: 'k8s.io/ingress-nginx' } },
      controller,
      server,
      service({ app: 'web' }),
      ...slice(true),
      tlsSecret(good),
      ingress(BACKEND),
    )
    if (inKind) {
      for (const n of [N, C]) await kubectl('-n', n, 'wait', '--for=condition=Ready', 'pod', '--all', '--timeout=180s')
      await setAddress(true)
    }
  })
  test.afterAll(async () => {
    for (const c of extra.reverse()) await c()
    await cleanup?.()
  })

  test('a working route, and its hops in the tab', async ({ request, page }) => {
    const row = await expectHop(request, 'Pods', '1 ready', 'Works')
    expect(row.url).toBe(`https://${HOST}/`)
    expect(row.hops.map((h) => `${h.k}=${h.tone}`)).toEqual(['Address=ok', 'Ingress controller=ok', 'TLS certificate=ok', 'Ingress rule=ok', 'Service=ok', 'Endpoints=ok', 'Network policy=muted', 'Pods=ok'])
    expect(row.hops.find((h) => h.k === 'TLS certificate')?.s).toMatch(/^(29|30) days left$/)
    if (inKind) expect(await realConnection(true), 'the controller reaches the pod').toBe(true)

    await page.goto(`/#/networks?route=${encodeURIComponent(KEY)}`)
    await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
    await expect(page.getByTestId('trace-verdict')).toHaveText('Works')
    await expect(page.getByTestId('hop')).toHaveCount(8)
    await expect(page.locator(`[data-route="${KEY}"]`)).toContainText(HOST)
    // The Service's details open over the tab (asked at the Phase 4 review).
    await page.locator('[data-hop="Service"] a').click()
    await expect(page.getByTestId('service-panel').getByRole('heading', { level: 2 })).toHaveText('web')
    await expect(page).toHaveURL(new RegExp(`#/networks\\?.*peek=${encodeURIComponent(`service:${N}/web`)}`))
  })

  test('no address', async ({ request }) => {
    await setAddress(false)
    const row = await expectHop(request, 'Address', 'no address', 'Fails')
    expect(row.text).toBe('Fails at address: The Ingress has no address yet: no controller has admitted it.')
    await setAddress(true)
    await expectHop(request, 'Address', '1 address', 'Works')
  })

  test('the TLS certificate: missing, expired, for another host', async ({ request }) => {
    const restore = await certificate(HOST)
    // Deleting the Secret: applying it again hands back its removal.
    await (await put(tlsSecret(restore)))()
    await expectHop(request, 'TLS certificate', 'secret missing', 'Fails')
    await put(tlsSecret(await certificate(HOST, true)))
    await expectHop(request, 'TLS certificate', 'expired', 'Fails')
    await put(tlsSecret(await certificate('other.e2e.test')))
    const row = await expectHop(request, 'TLS certificate', 'doesn’t cover host', 'Fails')
    expect(row.hops.find((h) => h.k === 'TLS certificate')?.d).toBe(`The certificate in web-tls covers other.e2e.test, not ${HOST}.`)
    await put(tlsSecret(restore))
    await expectHop(request, 'TLS certificate', /(29|30) days left/, 'Works')
  })

  test('the backend: no such port, no Service, no endpoints', async ({ request }) => {
    await put(ingress({ name: 'web', port: { name: 'https' } }))
    if (inKind) await setAddress(true)
    await expectHop(request, 'Service', 'no such port', 'Fails')
    await put(ingress({ name: 'nope', port: { number: 80 } }))
    if (inKind) await setAddress(true)
    const row = await expectHop(request, 'Service', 'missing', 'Fails')
    expect(row.endpoints.text).toBe('no service')
    await put(ingress(BACKEND))
    if (inKind) await setAddress(true)
    await put(service({ app: 'nothing' }), ...slice(false))
    const eps = await expectHop(request, 'Endpoints', 'no endpoints', 'Fails')
    expect(eps.hops.find((h) => h.k === 'Endpoints')?.d).toBe(`The selector app=nothing matches no ready pod in ${N}, so the controller answers 503.`)
    await put(service({ app: 'web' }), ...slice(true))
    await expectHop(request, 'Pods', '1 ready', 'Works')
  })

  test('network policies, against a real connection', async ({ request }) => {
    const deny: KubeObject = { apiVersion: 'networking.k8s.io/v1', kind: 'NetworkPolicy', metadata: { name: 'deny-all', namespace: N }, spec: { podSelector: {}, policyTypes: ['Ingress'] } }
    extra.push(await put(deny))
    const row = await expectHop(request, 'Network policy', 'blocked', 'Fails')
    expect(row.policy.text).toBe('blocked')
    if (inKind) expect(await realConnection(false), 'the controller is blocked').toBe(false)
    const allow: KubeObject = {
      apiVersion: 'networking.k8s.io/v1',
      kind: 'NetworkPolicy',
      metadata: { name: 'from-ingress', namespace: N },
      spec: { podSelector: { matchLabels: { app: 'web' } }, ingress: [{ from: [{ namespaceSelector: { matchLabels: { 'kubernetes.io/metadata.name': C } }, podSelector: { matchLabels: CTL_LABELS } }], ports: [{ protocol: 'TCP', port: 8080 }] }] },
    }
    extra.push(await put(allow))
    const ok = await expectHop(request, 'Network policy', 'allowed', 'Works')
    expect(ok.policy.text).toBe('from-ingress')
    if (inKind) expect(await realConnection(true), 'the controller is let through').toBe(true)
  })

  test('a controller that is not ready', async ({ request }) => {
    test.skip(!inKind, 'the simulated cluster runs its own ingress-nginx pods, so one more not-ready pod only degrades the route')
    await kubectl('-n', C, 'exec', 'controller', '--', 'rm', '/tmp/ready')
    const row = await expectHop(request, 'Ingress controller', '0 of 1 ready', 'Fails')
    expect(row.text).toContain('No controller pod is ready: controller: ')
    await kubectl('-n', C, 'exec', 'controller', '--', 'touch', '/tmp/ready')
    await expectHop(request, 'Ingress controller', '1 of 1 ready', 'Works')
  })
})

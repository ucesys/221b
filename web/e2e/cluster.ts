// What the end-to-end tests do to the cluster. With E2E_MODE=sim they drive
// the simulated API server (make e2e-sim); with E2E_MODE=kind they use
// kubectl and docker against the kind cluster CI installs the chart in.

import { execFile, spawn } from 'node:child_process'
import { promisify } from 'node:util'
import type { APIRequestContext } from '@playwright/test'
import type { NodeRow, PodRow, ReleaseRow, ServiceRow, Snapshot } from '../src/api/types'

const run = promisify(execFile)

export type Mode = 'sim' | 'kind'
export const MODE: Mode = process.env.E2E_MODE === 'kind' ? 'kind' : 'sim'
export const inKind = MODE === 'kind'

const SIM = process.env.E2E_SIM_URL ?? 'http://127.0.0.1:7443'
/** The fake Claude API: the simulator's, or the kind run's own. */
const CLAUDE = process.env.E2E_CLAUDE_URL ?? SIM + '/claude'
// The person with limited access (P3-02): the simulator's dev account, or
// the local account the chart in kind is installed with.
const DEV = process.env.E2E_DEV ?? 'dev@corp.example'
const DEV_NAMESPACES = ['web', 'analytics']

/** A RoleBinding of the view role for the dev account in a namespace. */
function devBinding(namespace: string): unknown {
  return {
    apiVersion: 'rbac.authorization.k8s.io/v1',
    kind: 'RoleBinding',
    metadata: { name: 'e2e-dev-view', namespace, labels: { e2e: 'dev' } },
    roleRef: { apiGroup: 'rbac.authorization.k8s.io', kind: 'ClusterRole', name: 'view' },
    subjects: [{ apiGroup: 'rbac.authorization.k8s.io', kind: 'User', name: DEV }],
  }
}

// A second API server for the cluster connections (P3-05 to P3-09): a
// second simulator, or the kind cluster itself through its in-cluster
// address, which the console reaches as another connection.
const OTHER_SIM = process.env.E2E_OTHER_SIM_URL ?? 'http://127.0.0.1:7444'
/** An unsigned token shaped like a ServiceAccount's; the simulator checks it by value only. */
export const OTHER_TOKEN = 'eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJzeXN0ZW06c2VydmljZWFjY291bnQ6a3ViZS1zeXN0ZW06Y2x1c3Rlci1jb25zb2xlIiwiZXhwIjo0MTAyNDQ0ODAwfQ.c2ln'
const NAMESPACE = process.env.E2E_NAMESPACE ?? '221b'
const IMAGE = process.env.E2E_IMAGE ?? ''
const CONTROL_PLANE = process.env.E2E_CONTROL_PLANE ?? 'kind-control-plane'
const METRICS_MANIFEST = process.env.E2E_METRICS_MANIFEST ?? ''

export async function snapshot(request: APIRequestContext, views: string[] = ['nodes']): Promise<Snapshot> {
  const res = await request.get(`/api/v1/snapshot?views=${encodeURIComponent(views.join(','))}`)
  if (!res.ok()) throw new Error(`snapshot: ${res.status()}`)
  return (await res.json()) as Snapshot
}

export function nodeRows(snap: Snapshot): NodeRow[] {
  return (snap.views.nodes?.rows ?? []) as NodeRow[]
}

export function podRows(snap: Snapshot): PodRow[] {
  return (snap.views.pods?.rows ?? []) as PodRow[]
}

export function serviceRows(snap: Snapshot): ServiceRow[] {
  return (snap.views.services?.rows ?? []) as ServiceRow[]
}

export function releaseRows(snap: Snapshot): ReleaseRow[] {
  return (snap.views.helm?.rows ?? []) as ReleaseRow[]
}

/** Workers with no problems, which the tests may change. */
export function healthyWorkers(snap: Snapshot): NodeRow[] {
  return nodeRows(snap).filter((r) => r.role === 'worker' && r.status === 'Ready' && r.problems.length === 0)
}

/** The Secret value planted for P2-07, which must never appear anywhere. */
export const PLANTED = 'PLANTED-SECRET-VALUE-9f8e7d6c5b4a'

/** The simulator's Helm value that looks like a credential. */
export const PLANTED_HELM = 'PLANTED-HELM-PASSWORD-1a2b3c4d'

export interface KubeObject {
  apiVersion: string
  kind: string
  metadata: { name: string; namespace?: string; labels?: Record<string, string> }
  [k: string]: unknown
}

const RESOURCES: Record<string, string> = { ClusterRole: 'clusterroles', ClusterRoleBinding: 'clusterrolebindings', Role: 'roles', RoleBinding: 'rolebindings', ServiceAccount: 'serviceaccounts', NetworkPolicy: 'networkpolicies', PodDisruptionBudget: 'poddisruptionbudgets', ResourceQuota: 'resourcequotas', LimitRange: 'limitranges', PriorityClass: 'priorityclasses', Ingress: 'ingresses', IngressClass: 'ingressclasses', Service: 'services', Pod: 'pods', Deployment: 'deployments', Namespace: 'namespaces', Secret: 'secrets', ValidatingWebhookConfiguration: 'validatingwebhookconfigurations', MutatingWebhookConfiguration: 'mutatingwebhookconfigurations' }

/** Runs a command in a pod (kind only); resolves to whether it succeeded. */
export async function kubectlExecOK(namespace: string, pod: string, ...cmd: string[]): Promise<boolean> {
  try {
    await run('kubectl', ['-n', namespace, 'exec', pod, '--', ...cmd], { timeout: 20_000 })
    return true
  } catch {
    return false
  }
}

/** kubectl with its exit status and both streams, for commands that fail on purpose (kind only). */
export async function kubectlResult(...args: string[]): Promise<{ ok: boolean; out: string; err: string }> {
  try {
    const { stdout, stderr } = await run('kubectl', args, { timeout: 120_000, maxBuffer: 16 << 20 })
    return { ok: true, out: stdout, err: stderr }
  } catch (e) {
    const x = e as { stdout?: string; stderr?: string }
    return { ok: false, out: x.stdout ?? '', err: x.stderr ?? '' }
  }
}

/** What kubectl auth can-i answers (kind only). */
export async function kubectlCanI(...args: string[]): Promise<boolean> {
  try {
    const out = await kubectl('auth', 'can-i', ...args)
    return out.trim() === 'yes'
  } catch (err) {
    const out = String((err as { stdout?: string }).stdout ?? '').trim()
    if (out === 'no' || out.startsWith('no')) return false
    throw err
  }
}

export async function kubectl(...args: string[]): Promise<string> {
  const { stdout } = await run('kubectl', args, { timeout: 60_000, maxBuffer: 64 << 20 })
  return stdout
}

/** kubectl, retried while the API server is coming back. */
export async function kubectlRetry(...args: string[]): Promise<string> {
  const deadline = Date.now() + 120_000
  for (;;) {
    try {
      return await kubectl(...args)
    } catch (err) {
      if (Date.now() > deadline) throw err
      await sleep(1000)
    }
  }
}

/** kubectl apply of one object, passed on stdin. */
async function kubectlApply(obj: unknown): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const p = spawn('kubectl', ['apply', '-f', '-'], { stdio: ['pipe', 'ignore', 'pipe'] })
    let err = ''
    p.stderr.on('data', (d: Buffer) => (err += d.toString()))
    p.on('error', reject)
    p.on('close', (code) => (code === 0 ? resolve() : reject(new Error(`kubectl apply: ${err}`))))
    p.stdin.end(JSON.stringify(obj))
  })
}

async function docker(...args: string[]): Promise<string> {
  const { stdout } = await run('docker', args, { timeout: 60_000 })
  return stdout
}

/** POSTs JSON to the simulator's admin API. */
async function simPostJSON(path: string, body: unknown): Promise<void> {
  const res = await fetch(`${SIM}${path}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  if (!res.ok) throw new Error(`${path}: ${res.status}`)
}

async function simPost(path: string, method = 'POST'): Promise<void> {
  const res = await fetch(SIM + path, { method })
  if (!res.ok) throw new Error(`${path}: ${res.status} ${await res.text()}`)
}

/** Objects of a kind in a namespace, as the cluster itself counts them. */
export async function countKind(namespace: string, kind: string): Promise<number> {
  if (inKind) {
    const out = await kubectl('-n', namespace, 'get', kind, '-o', 'name', '--ignore-not-found')
    return out.trim() ? out.trim().split('\n').length : 0
  }
  const paths: Record<string, string> = { pods: '/api/v1', services: '/api/v1', configmaps: '/api/v1', secrets: '/api/v1', deployments: '/apis/apps/v1', replicasets: '/apis/apps/v1' }
  const res = await fetch(`${SIM}${paths[kind] ?? '/api/v1'}/namespaces/${namespace}/${kind}`)
  const list = (await res.json()) as { items: unknown[] }
  return list.items.length
}

export function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms))
}

/** `kubectl get -o yaml` of one object (kind only). */
export async function kubectlYAML(kind: string, name: string, namespace?: string): Promise<string> {
  return kubectl(...(namespace ? ['-n', namespace] : []), 'get', kind, name, '-o', 'yaml')
}

/**
 * YAML without what differs between two reads or by design: managed fields,
 * the resource version and the status, which changes under heartbeats.
 * What remains (apiVersion, kind, metadata, spec, data) must match exactly
 * between the console's YAML view and kubectl (P2-08).
 */
export function stableYAML(text: string): string {
  const out: string[] = []
  let skip: number | null = null
  for (const line of text.split('\n')) {
    const indent = line.length - line.trimStart().length
    const body = line.trim()
    if (skip !== null) {
      if (body === '' || indent > skip || (indent === skip && body.startsWith('- '))) continue
      skip = null
    }
    if (body.startsWith('managedFields:') || body === 'status:') {
      skip = indent
      continue
    }
    if (body.startsWith('resourceVersion:')) continue
    out.push(line)
  }
  return out.join('\n').trim()
}

/** Waits until every node is Ready again (kind only). */
export async function allNodesReady(): Promise<void> {
  if (inKind) await kubectlRetry('wait', '--for=condition=Ready', 'nodes', '--all', '--timeout=180s')
}

/** Whether the cluster shows a pod's first container crash-looping (kind only). */
export async function podCrashing(pod: string): Promise<boolean> {
  const [ns, name] = pod.split('/') as [string, string]
  type State = { waiting?: { reason?: string }; terminated?: { exitCode?: number } }
  const out = await kubectl('-n', ns, 'get', 'pod', name, '-o', 'json')
  const p = JSON.parse(out) as { status?: { containerStatuses?: { restartCount?: number; state?: State; lastState?: State }[] } }
  const cs = p.status?.containerStatuses?.[0]
  const crashing =
    cs?.state?.waiting?.reason === 'CrashLoopBackOff' || ((cs?.restartCount ?? 0) > 0 && (cs?.state?.terminated?.exitCode ?? 0) !== 0)
  if (!crashing) console.log(`${pod} is not crash-looping yet: ${JSON.stringify(cs ?? p.status ?? {})}`)
  return crashing
}

/** The node 221B itself runs on, which the tests leave alone. */
export async function consoleNode(): Promise<string> {
  if (!inKind) return ''
  return (await kubectl('-n', NAMESPACE, 'get', 'pods', '-l', 'app.kubernetes.io/name=221b', '-o', 'jsonpath={.items[0].spec.nodeName}')).trim()
}

export const cluster = {
  async cordon(node: string, on: boolean): Promise<void> {
    if (inKind) await kubectlRetry(on ? 'cordon' : 'uncordon', node)
    else await simPost(`/sim/nodes/${node}/cordon?on=${on}`)
  },

  /** The kubelet stops reporting, so the node goes Unknown. */
  async stopKubelet(node: string): Promise<void> {
    if (inKind) await docker('exec', node, 'systemctl', 'stop', 'kubelet')
    else await simPost(`/sim/nodes/${node}/ready?status=Unknown`)
  },

  async startKubelet(node: string): Promise<void> {
    if (inKind) await docker('exec', node, 'systemctl', 'start', 'kubelet')
    else await simPost(`/sim/nodes/${node}/ready?status=True`)
  },

  /** Puts a crash-looping pod on a node; returns its name and a cleanup. */
  async crashLoop(node: string): Promise<{ pod: string; cleanup: () => Promise<void> }> {
    if (inKind) {
      const pod = `crashloop-${Date.now().toString(36)}`
      // The console's own image, already loaded into kind: it exits at once
      // when the cluster name is empty.
      const manifest = {
        apiVersion: 'v1',
        kind: 'Pod',
        metadata: { name: pod, namespace: NAMESPACE, labels: { e2e: 'crashloop' } },
        spec: {
          nodeName: node,
          restartPolicy: 'Always',
          terminationGracePeriodSeconds: 0,
          // The namespace enforces the restricted Pod Security profile.
          securityContext: { runAsNonRoot: true, seccompProfile: { type: 'RuntimeDefault' } },
          containers: [
            {
              name: 'crash',
              image: IMAGE,
              imagePullPolicy: 'Never',
              args: ['-cluster-name', ''],
              resources: { requests: { cpu: '10m', memory: '16Mi' } },
              securityContext: { allowPrivilegeEscalation: false, capabilities: { drop: ['ALL'] } },
            },
          ],
        },
      }
      await kubectlApply(manifest)
      return { pod: `${NAMESPACE}/${pod}`, cleanup: async () => void (await kubectlRetry('-n', NAMESPACE, 'delete', 'pod', pod, '--wait=false')) }
    }
    const res = await fetch(`${SIM}/api/v1/pods`)
    const list = (await res.json()) as { items: { metadata: { namespace: string; name: string }; spec: { nodeName?: string }; status: { phase?: string } }[] }
    const p = list.items.find((i) => i.spec.nodeName === node && i.status.phase === 'Running')
    if (!p) throw new Error(`no running pod on ${node}`)
    const { namespace, name } = p.metadata
    await simPost(`/sim/pods/${namespace}/${name}/crash`)
    return { pod: `${namespace}/${name}`, cleanup: () => simPost(`/sim/pods/${namespace}/${name}/heal`) }
  },

  /** Stops or starts the Kubernetes API server; start waits until it answers. */
  async apiServer(up: boolean): Promise<void> {
    if (!inKind) return simPost(`/sim/down?on=${!up}`)
    const [from, to] = up
      ? ['/etc/kubernetes/kube-apiserver.yaml', '/etc/kubernetes/manifests/kube-apiserver.yaml']
      : ['/etc/kubernetes/manifests/kube-apiserver.yaml', '/etc/kubernetes/kube-apiserver.yaml']
    await docker('exec', CONTROL_PLANE, 'mv', from, to)
    if (up) {
      await kubectlRetry('get', '--raw', '/readyz')
      return
    }
    // The kubelet takes a few seconds to stop the static pod; the test's
    // clock starts when the API server is really gone.
    const deadline = Date.now() + 90_000
    while (Date.now() < deadline) {
      try {
        await run('kubectl', ['get', '--raw', '/readyz', '--request-timeout=2s'], { timeout: 5_000 })
      } catch {
        return
      }
      await sleep(500)
    }
    throw new Error('the API server is still answering 90 seconds after its manifest was moved')
  },

  async metricsServer(on: boolean): Promise<void> {
    if (!inKind) return simPost(`/sim/metrics?on=${on}`)
    if (on) await kubectlRetry('apply', '-f', METRICS_MANIFEST)
    else await kubectl('delete', 'apiservice', 'v1beta1.metrics.k8s.io')
  },

  /** Deletes a pod for good (not through a controller that would replace it). */
  async deletePod(pod: string): Promise<void> {
    const [ns, name] = pod.split('/') as [string, string]
    if (inKind) await kubectlRetry('-n', ns, 'delete', 'pod', name, '--wait=false')
    else await simPost(`/sim/pods/${ns}/${name}`, 'DELETE')
  },

  /**
   * A Secret holding the planted value, and the namespace it is in: the
   * simulator has one from the start; kind gets one made here.
   */
  async plantedSecret(): Promise<{ namespace: string; name: string; cleanup: () => Promise<void> }> {
    if (!inKind) return { namespace: 'default', name: 'planted-secret', cleanup: async () => {} }
    const name = `e2e-planted-${Date.now().toString(36)}`
    await kubectlApply({ apiVersion: 'v1', kind: 'Secret', metadata: { name, namespace: NAMESPACE, labels: { e2e: 'planted' } }, type: 'Opaque', stringData: { token: PLANTED, 'database-url': `postgres://app:${PLANTED}@db:5432/app` } })
    return { namespace: NAMESPACE, name, cleanup: async () => void (await kubectlRetry('-n', NAMESPACE, 'delete', 'secret', name, '--ignore-not-found')) }
  },

  /** A LoadBalancer Service no controller will ever give an address (P2-05). */
  async pendingLoadBalancer(): Promise<{ namespace: string; name: string; cleanup: () => Promise<void> }> {
    if (!inKind) return { namespace: 'web', name: 'gateway-lb', cleanup: async () => {} }
    const name = `e2e-lb-${Date.now().toString(36)}`
    await kubectlApply({ apiVersion: 'v1', kind: 'Service', metadata: { name, namespace: NAMESPACE, labels: { e2e: 'lb' } }, spec: { type: 'LoadBalancer', selector: { 'app.kubernetes.io/name': '221b' }, ports: [{ name: 'http', port: 80, targetPort: 8081 }] } })
    return { namespace: NAMESPACE, name, cleanup: async () => void (await kubectlRetry('-n', NAMESPACE, 'delete', 'service', name, '--ignore-not-found')) }
  },

  /** What `helm list -A` says (kind), or what the simulator planted. */
  async helmList(): Promise<{ name: string; namespace: string; revision: number; status: string; chart: string; app_version: string }[]> {
    if (inKind) {
      const out = await run('helm', ['list', '-A', '-o', 'json'], { timeout: 60_000 })
      return (JSON.parse(out.stdout) as { name: string; namespace: string; revision: string; status: string; chart: string; app_version: string }[]).map((r) => ({ ...r, revision: Number(r.revision) }))
    }
    return [
      { name: 'analytics-cache', namespace: 'analytics', revision: 1, status: 'deployed', chart: 'redis-19.6.4', app_version: '7.4.0' },
      { name: 'metrics-server', namespace: 'kube-system', revision: 1, status: 'deployed', chart: 'metrics-server-3.12.2', app_version: '0.7.2' },
      { name: 'node-agent', namespace: 'kube-system', revision: 2, status: 'deployed', chart: 'node-agent-0.9.2', app_version: '0.9.2' },
      { name: 'shop', namespace: 'web', revision: 3, status: 'deployed', chart: 'shop-frontend-3.2.1', app_version: '3.2.1' },
      { name: 'superset', namespace: 'analytics', revision: 3, status: 'failed', chart: 'superset-0.14.1', app_version: '4.1.1' },
    ]
  },

  /** What `helm history` says for a release (kind), or what the simulator planted. */
  async helmHistory(namespace: string, name: string): Promise<{ revision: number; status: string; chart: string }[]> {
    if (inKind) {
      const out = await run('helm', ['history', name, '-n', namespace, '-o', 'json'], { timeout: 60_000 })
      return (JSON.parse(out.stdout) as { revision: number; status: string; chart: string }[]).map((r) => ({ revision: r.revision, status: r.status, chart: r.chart }))
    }
    if (name === 'shop') {
      return [
        { revision: 1, status: 'superseded', chart: 'shop-frontend-3.1.9' },
        { revision: 2, status: 'failed', chart: 'shop-frontend-3.2.0' },
        { revision: 3, status: 'deployed', chart: 'shop-frontend-3.2.1' },
      ]
    }
    const r = (await this.helmList()).find((x) => x.name === name && x.namespace === namespace)!
    return Array.from({ length: r.revision }, (_, i) => ({ revision: i + 1, status: i + 1 === r.revision ? r.status : 'superseded', chart: r.chart }))
  },

  /**
   * Another cluster to connect to (P3-09): its address, how its certificate
   * is checked, a token that works there with the account it names, and
   * its node count. In kind it is the kind cluster itself, reached through
   * its in-cluster address with a token of the console's own ServiceAccount.
   */
  async other(): Promise<{ server: string; ca: 'pem' | 'system'; caPem: string; token: string; who: string; nodes: number }> {
    if (inKind) {
      const token = (await kubectl('-n', NAMESPACE, 'create', 'token', 'console-221b', '--duration=2h')).trim()
      const caPem = await kubectl('-n', NAMESPACE, 'get', 'configmap', 'kube-root-ca.crt', '-o', 'jsonpath={.data.ca\\.crt}')
      const nodes = (await kubectl('get', 'nodes', '-o', 'name')).trim().split('\n').filter(Boolean).length
      return { server: 'https://kubernetes.default.svc', ca: 'pem', caPem, token, who: `system:serviceaccount:${NAMESPACE}:console-221b`, nodes }
    }
    // The second simulator requires the token, as a real API server would.
    const res = await fetch(`${OTHER_SIM}/sim/token`, { method: 'POST', body: JSON.stringify({ token: OTHER_TOKEN }) })
    if (!res.ok) throw new Error(`the second simulator did not take a token: ${res.status}`)
    const list = (await (await fetch(`${OTHER_SIM}/api/v1/nodes`, { headers: { Authorization: `Bearer ${OTHER_TOKEN}` } })).json()) as { items: unknown[] }
    return { server: OTHER_SIM, ca: 'system', caPem: '', token: OTHER_TOKEN, who: 'system:serviceaccount:kube-system:cluster-console', nodes: list.items.length }
  },

  /**
   * Limits the dev account to some namespaces (P3-02) and returns them:
   * the simulator is told, or RoleBindings of the view role are made in
   * kind, where the dev may list the console's own namespace only.
   */
  async limitDev(): Promise<string[]> {
    if (inKind) {
      await kubectl('delete', 'rolebinding', '-A', '-l', 'e2e=dev', '--ignore-not-found')
      await kubectlApply(devBinding(NAMESPACE))
      return [NAMESPACE]
    }
    await simPostJSON('/sim/deny', { only: { [DEV]: DEV_NAMESPACES } })
    return DEV_NAMESPACES
  },

  /** Lets the dev account list one more namespace. */
  async allowDev(namespace: string): Promise<void> {
    if (inKind) {
      await kubectlApply(devBinding(namespace))
      return
    }
    await simPostJSON('/sim/deny', { only: { [DEV]: [...DEV_NAMESPACES, namespace] } })
  },

  /** Whether the Secret a connection is kept in exists (P3-07, P3-08). */
  async connectionSecret(id: string): Promise<boolean> {
    const name = `console-221b-cluster-${id}`
    if (inKind) {
      try {
        await kubectl('-n', NAMESPACE, 'get', 'secret', name, '-o', 'name')
        return true
      } catch {
        return false
      }
    }
    const res = await fetch(`${SIM}/api/v1/namespaces/221b/secrets/${name}`)
    return res.ok
  },

  /**
   * The three faults P4-07 names, on one node: a pod whose image cannot be
   * pulled, one no node can take, and one the kernel kills for running out
   * of memory. In kind they are real pods in the console's namespace; the
   * simulator is given pods in the states those faults leave, with the
   * events the kubelet and the scheduler would write.
   */
  async alertFaults(node: string): Promise<{ pull: string; unschedulable: string; oom: string; cleanup: () => Promise<void> }> {
    const id = Date.now().toString(36)
    const names = { pull: `e2e-pull-${id}`, unschedulable: `e2e-nowhere-${id}`, oom: `e2e-oom-${id}` }
    if (inKind) {
      const restricted = { runAsNonRoot: true, runAsUser: 65534, seccompProfile: { type: 'RuntimeDefault' } }
      const container = { allowPrivilegeEscalation: false, capabilities: { drop: ['ALL'] } }
      const pod = (name: string, spec: Record<string, unknown>, c: Record<string, unknown>) => ({
        apiVersion: 'v1',
        kind: 'Pod',
        metadata: { name, namespace: NAMESPACE, labels: { e2e: 'alerts' } },
        spec: { terminationGracePeriodSeconds: 0, securityContext: restricted, ...spec, containers: [{ name: 'main', securityContext: container, ...c }] },
      })
      await kubectlApply(pod(names.pull, { nodeName: node }, { image: 'registry.invalid/e2e/nope:1', resources: { requests: { cpu: '10m', memory: '16Mi' } } }))
      await kubectlApply(pod(names.unschedulable, { nodeSelector: { 'e2e.221b/nowhere': 'true' } }, { image: IMAGE, imagePullPolicy: 'Never', resources: { requests: { cpu: '10m', memory: '16Mi' } } }))
      // tail keeps everything after the last newline, and /dev/zero has none.
      await kubectlApply(pod(names.oom, { nodeName: node }, { image: 'registry.k8s.io/e2e-test-images/busybox:1.36.1-1', command: ['sh', '-c', 'tail /dev/zero'], resources: { requests: { cpu: '10m', memory: '24Mi' }, limits: { memory: '24Mi' } } }))
      const ns = NAMESPACE
      return {
        pull: `${ns}/${names.pull}`,
        unschedulable: `${ns}/${names.unschedulable}`,
        oom: `${ns}/${names.oom}`,
        cleanup: async () => void (await kubectlRetry('-n', ns, 'delete', 'pods', '-l', 'e2e=alerts', '--wait=false', '--grace-period=0', '--ignore-not-found')),
      }
    }
    const ns = 'default'
    const now = Date.now()
    const ts = (agoMs: number) => new Date(now - agoMs).toISOString().replace(/\.\d+Z$/, 'Z')
    const res = { requests: { cpu: '10m', memory: '16Mi' } }
    const pod = (name: string, spec: Record<string, unknown>, status: Record<string, unknown>) => ({
      apiVersion: 'v1',
      kind: 'Pod',
      metadata: { name, namespace: ns, labels: { e2e: 'alerts' }, uid: `e2e-${name}`, creationTimestamp: ts(20_000) },
      spec: { containers: [{ name: 'main', image: 'registry.example/e2e:1', resources: res }], ...spec },
      status,
    })
    const event = (name: string, kind: string, reason: string, message: string, component: string) => ({
      apiVersion: 'v1',
      kind: 'Event',
      metadata: { name: `${name}.${reason.toLowerCase()}`, namespace: ns },
      involvedObject: { kind, namespace: ns, name, uid: `e2e-${name}`, apiVersion: 'v1' },
      type: 'Warning',
      reason,
      message,
      count: 3,
      firstTimestamp: ts(18_000),
      lastTimestamp: ts(2_000),
      source: { component, host: component === 'kubelet' ? node : undefined },
    })
    const image = 'registry.invalid/e2e/nope:1'
    await simPostJSON('/sim/objects', pod(names.pull, { nodeName: node, containers: [{ name: 'main', image, resources: res }] }, {
      phase: 'Pending',
      startTime: ts(20_000),
      conditions: [{ type: 'PodScheduled', status: 'True' }, { type: 'Ready', status: 'False', reason: 'ContainersNotReady' }],
      containerStatuses: [{ name: 'main', image, imageID: '', ready: false, restartCount: 0, state: { waiting: { reason: 'ImagePullBackOff', message: `Back-off pulling image "${image}"` } } }],
    }))
    await simPostJSON('/sim/objects', event(names.pull, 'Pod', 'Failed', `Failed to pull image "${image}": failed to resolve reference "${image}": no such host`, 'kubelet'))
    await simPostJSON('/sim/objects', pod(names.unschedulable, { nodeSelector: { 'e2e.221b/nowhere': 'true' } }, {
      phase: 'Pending',
      conditions: [{ type: 'PodScheduled', status: 'False', reason: 'Unschedulable', message: "0/40 nodes are available: 40 node(s) didn't match Pod's node affinity/selector." }],
    }))
    await simPostJSON('/sim/objects', event(names.unschedulable, 'Pod', 'FailedScheduling', "0/40 nodes are available: 40 node(s) didn't match Pod's node affinity/selector.", 'default-scheduler'))
    await simPostJSON('/sim/objects', pod(names.oom, { nodeName: node }, {
      phase: 'Running',
      startTime: ts(20_000),
      conditions: [{ type: 'PodScheduled', status: 'True' }, { type: 'Ready', status: 'True' }],
      containerStatuses: [
        {
          name: 'main',
          image: 'registry.example/e2e:1',
          imageID: '',
          ready: true,
          started: true,
          restartCount: 1,
          state: { running: { startedAt: ts(5_000) } },
          lastState: { terminated: { exitCode: 137, reason: 'OOMKilled', startedAt: ts(19_000), finishedAt: ts(6_000) } },
        },
      ],
    }))
    return {
      pull: `${ns}/${names.pull}`,
      unschedulable: `${ns}/${names.unschedulable}`,
      oom: `${ns}/${names.oom}`,
      cleanup: async () => {
        for (const n of Object.values(names)) await simPost(`/sim/objects/pods/${ns}/${n}`, 'DELETE')
      },
    }
  },

  /** How many nodes a label selector picks, as the cluster counts them. */
  async countNodes(selector: string): Promise<number> {
    if (inKind) {
      const out = await kubectl('get', 'nodes', '-l', selector, '-o', 'name')
      return out.trim() ? out.trim().split('\n').length : 0
    }
    const res = await fetch(`${SIM}/api/v1/nodes?labelSelector=${encodeURIComponent(selector)}`)
    return ((await res.json()) as { items: unknown[] }).items.length
  },

  /**
   * A node with these labels and taints (P4-01): a new node in the
   * simulator, or a kind worker given them; the cleanup takes it away or
   * takes them off.
   */
  async nodeWith(worker: string, labels: Record<string, string>, taints: { key: string; value: string; effect: string }[] = []): Promise<{ node: string; cleanup: () => Promise<void> }> {
    if (inKind) {
      if (Object.keys(labels).length) await kubectlRetry('label', 'node', worker, '--overwrite', ...Object.entries(labels).map(([k, v]) => `${k}=${v}`))
      for (const t of taints) await kubectlRetry('taint', 'node', worker, '--overwrite', `${t.key}=${t.value}:${t.effect}`)
      return {
        node: worker,
        cleanup: async () => {
          if (Object.keys(labels).length) await kubectlRetry('label', 'node', worker, ...Object.keys(labels).map((k) => `${k}-`))
          for (const t of taints) await kubectl('taint', 'node', worker, `${t.key}:${t.effect}-`).catch(() => '')
        },
      }
    }
    const node = `e2e-fleet-${Date.now().toString(36)}`
    await simPostJSON('/sim/nodes', { name: node, labels, taints })
    return { node, cleanup: () => simPost(`/sim/nodes/${node}`, 'DELETE') }
  },

  /** Adds a taint to a node, or takes it off. */
  async taint(node: string, t: { key: string; value: string; effect: string }, on: boolean): Promise<void> {
    if (inKind) {
      if (on) await kubectlRetry('taint', 'node', node, '--overwrite', `${t.key}=${t.value}:${t.effect}`)
      else await kubectl('taint', 'node', node, `${t.key}:${t.effect}-`).catch(() => '')
      return
    }
    const res = await fetch(`${SIM}/api/v1/nodes/${node}`)
    const n = (await res.json()) as { apiVersion?: string; kind?: string; spec: { taints?: { key: string; value?: string; effect: string }[] } }
    n.apiVersion = 'v1'
    n.kind = 'Node'
    const rest = (n.spec.taints ?? []).filter((x) => !(x.key === t.key && x.effect === t.effect))
    n.spec.taints = on ? [...rest, t] : rest
    await simPostJSON('/sim/objects', n)
  },

  /**
   * Puts objects into the cluster (P4): kubectl apply in kind, the
   * simulator's object API otherwise. Returns a cleanup that deletes them.
   */
  async applyObjects(objs: KubeObject[]): Promise<() => Promise<void>> {
    for (const o of objs) {
      if (inKind) await kubectlApply(o)
      else await simPostJSON('/sim/objects', o)
    }
    return async () => {
      for (const o of [...objs].reverse()) {
        const resource = RESOURCES[o.kind] ?? o.kind.toLowerCase() + 's'
        if (inKind) await kubectlRetry('delete', resource, o.metadata.name, ...(o.metadata.namespace ? ['-n', o.metadata.namespace] : []), '--ignore-not-found', '--wait=false')
        else await fetch(`${SIM}/sim/objects/${resource}/${o.metadata.namespace ?? '_'}/${o.metadata.name}`, { method: 'DELETE' })
      }
    }
  },

  /** The server's log so far: the file the Makefile writes (sim) or the pod's (kind). */
  async serverLog(): Promise<string> {
    if (inKind) return kubectl('-n', NAMESPACE, 'logs', '-l', 'app.kubernetes.io/name=221b', '--tail=-1', '--all-containers')
    const { readFile } = await import('node:fs/promises')
    return readFile(process.env.E2E_SERVER_LOG ?? '../bin/e2e-server.log', 'utf8').catch(() => '')
  },

  /**
   * The requests the console sent to the Claude API (Phase 5): the fake
   * the simulator serves at /claude, or the one the kind run starts.
   */
  async claudeRequests(): Promise<string[]> {
    const res = await fetch(`${CLAUDE}/requests`)
    if (!res.ok) throw new Error(`the fake Claude API answered ${res.status}`)
    return ((await res.json()) as unknown[]).map((r) => JSON.stringify(r))
  },

  async resetClaude(): Promise<void> {
    await fetch(`${CLAUDE}/requests`, { method: 'DELETE' })
  },

  /** The audit records so far (P3-10): the server's standard output, one JSON object per line. */
  async auditLog(): Promise<Record<string, unknown>[]> {
    let text: string
    if (inKind) text = await kubectl('-n', NAMESPACE, 'logs', '-l', 'app.kubernetes.io/name=221b', '--tail=-1', '--all-containers')
    else {
      const { readFile } = await import('node:fs/promises')
      text = await readFile(process.env.E2E_AUDIT_LOG ?? '../bin/e2e-audit.log', 'utf8').catch(() => '')
    }
    const out: Record<string, unknown>[] = []
    for (const line of text.split('\n')) {
      if (!line.startsWith('{') || !line.includes('"action"')) continue
      try {
        out.push(JSON.parse(line) as Record<string, unknown>)
      } catch {
        // not a record
      }
    }
    return out
  },
}

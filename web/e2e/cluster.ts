// What the end-to-end tests do to the cluster. With E2E_MODE=sim they drive
// the simulated API server (make e2e-sim); with E2E_MODE=kind they use
// kubectl and docker against the kind cluster CI installs the chart in.

import { execFile, spawn } from 'node:child_process'
import { promisify } from 'node:util'
import type { APIRequestContext } from '@playwright/test'
import type { NodeRow, Snapshot } from '../src/api/types'

const run = promisify(execFile)

export type Mode = 'sim' | 'kind'
export const MODE: Mode = process.env.E2E_MODE === 'kind' ? 'kind' : 'sim'
export const inKind = MODE === 'kind'

const SIM = process.env.E2E_SIM_URL ?? 'http://127.0.0.1:7443'
const NAMESPACE = process.env.E2E_NAMESPACE ?? '221b'
const IMAGE = process.env.E2E_IMAGE ?? ''
const CONTROL_PLANE = process.env.E2E_CONTROL_PLANE ?? 'kind-control-plane'
const METRICS_MANIFEST = process.env.E2E_METRICS_MANIFEST ?? ''

export async function snapshot(request: APIRequestContext): Promise<Snapshot> {
  const res = await request.get('/api/v1/snapshot')
  if (!res.ok()) throw new Error(`snapshot: ${res.status()}`)
  return (await res.json()) as Snapshot
}

/** Workers with no problems, which the tests may change. */
export function healthyWorkers(snap: Snapshot): NodeRow[] {
  return snap.rows.filter((r) => r.role === 'worker' && r.status === 'Ready' && r.problems.length === 0)
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

async function simPost(path: string): Promise<void> {
  const res = await fetch(SIM + path, { method: 'POST' })
  if (!res.ok) throw new Error(`${path}: ${res.status} ${await res.text()}`)
}

export function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms))
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
}

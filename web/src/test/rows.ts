// Node rows for unit tests, shaped like the server's.
import type { NodeRow } from '../api/types'

const GI = 1024 ** 3

export function row(name: string, over: Partial<NodeRow> = {}): NodeRow {
  return {
    name,
    role: 'worker',
    roles: ['worker'],
    status: 'Ready',
    tone: 'ok',
    statusTip: 'The kubelet is healthy and the node accepts pods.',
    cordoned: false,
    problems: [],
    errors: 0,
    warnings: 0,
    cpu: { requested: 4000, limits: 8000, allocatable: 16000, capacity: 16000, used: 2000 },
    memory: { requested: 16 * GI, limits: 32 * GI, allocatable: 64 * GI, capacity: 64 * GI, used: 8 * GI },
    pods: { running: 20, capacity: 110, notReady: 0, failing: 0, starting: 0, unready: 0, completed: 0 },
    taints: [],
    zone: 'zone-a',
    kubelet: 'v1.37.1',
    created: '2026-09-01T00:00:00Z',
    internalIP: '10.0.0.1',
    os: 'Ubuntu 24.04.3 LTS',
    kernel: '6.8.0-60-generic',
    runtime: 'containerd://2.1.4',
    arch: 'amd64',
    labels: { 'kubernetes.io/hostname': name, 'node.kubernetes.io/instance-type': 'general-16x64' },
    conditions: [{ type: 'Ready', status: 'True' }],
    ...over,
  }
}

export function controlPlane(name: string, over: Partial<NodeRow> = {}): NodeRow {
  return row(name, {
    role: 'control-plane',
    roles: ['control-plane'],
    taints: [{ key: 'node-role.kubernetes.io/control-plane', effect: 'NoSchedule' }],
    labels: { 'kubernetes.io/hostname': name, 'node-role.kubernetes.io/control-plane': '' },
    ...over,
  })
}

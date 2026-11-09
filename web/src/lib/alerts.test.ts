import { describe, expect, it } from 'vitest'
import { alertKeyOf, alertReason, badgeOf, bannersOf, byNode, contextOf, sectionsOf, sevLabel, tabTone, tilesOf } from './alerts'
import type { AlertRow } from '../api/types'

const now = Date.parse('2026-10-14T12:00:00Z')
const ago = (min: number) => new Date(now - min * 60_000).toISOString()

const pull: AlertRow = { key: 'pod:web/a', kind: 'pod', severity: 'failed', name: 'a', namespace: 'web', status: 'ImagePullBackOff', statusTone: 'err', node: 'w1', restarts: 0, started: ago(5), waiting: 'ImagePullBackOff', waitingMessage: 'Back-off pulling image "x"', events: [] }
const pending: AlertRow = { key: 'pod:web/b', kind: 'pod', severity: 'pending', name: 'b', namespace: 'web', status: 'Pending', statusTone: 'warn', started: ago(12), why: "0/3 nodes are available: 3 node(s) didn't match Pod's node affinity/selector.", events: [] }
const oom: AlertRow = { key: 'pod:web/c', kind: 'pod', severity: 'recovered', name: 'c', namespace: 'web', status: 'Running', statusTone: 'ok', ready: 1, total: 1, restarts: 1, started: ago(30), lastReason: 'OOMKilled', lastExit: 137, lastAt: ago(4), oom: true, events: [] }
const cordoned: AlertRow = { key: 'node:w2', kind: 'node', severity: 'warning', name: 'w2', role: 'worker', status: 'Ready,SchedulingDisabled', statusTone: 'warn', problems: [{ title: 'Cordoned', severity: 'warning', summary: 'New pods are not scheduled here.' }], events: [] }
const down: AlertRow = { key: 'node:cp', kind: 'node', severity: 'error', name: 'cp', role: 'control-plane', status: 'Unknown', statusTone: 'err', problems: [{ title: 'Kubelet stopped reporting', severity: 'error', summary: 'No heartbeat.' }], events: [] }

describe('alerts', () => {
  it('colours the Alerts tab’s name: red for failures, amber once they are acknowledged, nothing when all is well', () => {
    expect(tabTone([pull, pending])).toBe('err')
    expect(tabTone([{ ...pull, ack: { by: 'ops@corp.example', at: ago(1) } }, pending])).toBe('warn')
    expect(tabTone([{ ...pull, ack: { by: 'ops@corp.example', at: ago(1) } }])).toBe('warn')
    expect(tabTone([oom])).toBeUndefined()
    expect(tabTone([])).toBeUndefined()
  })

  it('says why each pod is here', () => {
    expect(contextOf(pull, now)).toEqual({ text: 'ImagePullBackOff: Back-off pulling image "x"', mono: true })
    expect(contextOf(pending, now).text).toBe("Pending for 12m. 0/3 nodes are available: 3 node(s) didn't match Pod's node affinity/selector.")
    expect(contextOf(oom, now).text).toContain('killed for running out of memory 4m ago (OOMKilled, exit code 137)')
    expect(contextOf(cordoned, now).text).toBe('Cordoned. New pods are not scheduled here.')
  })

  it('counts tiles and the badge, leaving recovered pods out of the badge', () => {
    const rows = [pull, pending, oom, cordoned, down]
    expect(tilesOf(rows).map((t) => t.count)).toEqual([1, 1, 1, 1, 0, 1])
    expect(badgeOf(rows)).toEqual({ count: 4, tone: 'err' })
    expect(badgeOf([oom])).toEqual({ count: 0, tone: 'muted' })
    expect(badgeOf([pending])).toEqual({ count: 1, tone: 'warn' })
  })

  it('puts each pod under the node it runs on, the most pressing group first', () => {
    const onW2 = { ...pull, key: 'pod:web/d', name: 'd', node: 'w2' }
    const onCp = { ...oom, node: 'cp' }
    const quiet = { ...pull, key: 'pod:web/e', name: 'e', node: 'w3', ack: { by: 'ops@corp.example', at: ago(1) } }
    const g = byNode([quiet, cordoned, down, pull, pending, onW2, onCp])
    // cp's own error and w1's and w2's failed pods first, by name; w3's acknowledged pod last.
    expect(g.groups.map((x) => [x.node, x.row?.name ?? '', x.pods.map((p) => p.name).join(',')])).toEqual([
      ['cp', 'cp', 'c'],
      ['w1', '', 'a'],
      ['w2', 'w2', 'd'],
      ['w3', '', 'e'],
    ])
    // The pod no node took has no node to go under.
    expect(g.unscheduled.map((r) => r.name)).toEqual(['b'])
    expect(byNode([cordoned]).groups).toEqual([{ node: 'w2', row: cordoned, pods: [] }])
  })

  it('puts control-plane nodes apart and errors first', () => {
    const s = sectionsOf([oom, pending, pull, cordoned, down])
    expect(s.pods.map((r) => r.name)).toEqual(['a', 'b', 'c'])
    expect(s.controlPlane.map((r) => r.name)).toEqual(['cp'])
    expect(s.workers.map((r) => r.name)).toEqual(['w2'])
  })

  it('leaves resolved alerts of any kind out of the badge, the banners and the warning tiles (6.5)', () => {
    const quiet: AlertRow = { ...cordoned, key: 'node:w3', name: 'w3', severity: 'recovered', status: 'Ready', statusTone: 'ok' }
    const bound: AlertRow = { key: 'pvc:web/data', kind: 'pvc', severity: 'recovered', name: 'data', namespace: 'web', status: 'Bound', statusTone: 'ok', events: [] }
    expect(badgeOf([quiet, bound])).toEqual({ count: 0, tone: 'muted' })
    expect(bannersOf([quiet, bound], now)).toEqual([])
    expect(tilesOf([quiet, bound, oom]).map((t) => t.count)).toEqual([0, 0, 0, 0, 0, 3])
    expect(tabTone([quiet, bound])).toBeUndefined()
  })

  it('makes one banner for errors and one for warnings', () => {
    const b = bannersOf([pull, pending, oom, cordoned, down], now)
    expect(b).toHaveLength(2)
    expect(b[0]).toMatchObject({ tone: 'err', title: '1 pod failed to start · 1 node error' })
    expect(b[0]!.detail).toBe('web/a — ImagePullBackOff  ·  cp — kubelet stopped reporting')
    expect(b[1]).toMatchObject({ tone: 'warn', title: '1 pod pending · 1 node warning' })
    expect(b[1]!.detail).toBe('web/b — Unschedulable for 12m  ·  w2 — cordoned')
    expect(bannersOf([oom], now)).toEqual([])
  })

  it('counts acknowledged alerts on the badge but leaves them off the banners, after the others but before the recovered', () => {
    const acked: AlertRow = { ...pull, key: 'pod:web/d', name: 'd', ack: { by: 'dana@corp.example', at: ago(1) } }
    const rows = [acked, pull, oom]
    expect(badgeOf(rows)).toEqual({ count: 2, tone: 'err' })
    expect(badgeOf([acked])).toEqual({ count: 1, tone: 'warn' })
    expect(bannersOf(rows, now).map((b) => b.detail).join(' ')).not.toContain('web/d')
    expect(sectionsOf(rows).pods.map((r) => r.name)).toEqual(['a', 'd', 'c'])
    expect(sevLabel(oom)).toEqual({ label: 'Resolved', tone: 'ok' })
    expect(sevLabel(pending)).toEqual({ label: 'Pending', tone: 'warn' })
    expect(sevLabel({ ...pending, status: 'Running', node: 'w1' })).toEqual({ label: 'Not ready', tone: 'warn' })
  })
})

// An alert's name by its reason (CF-84), as the server's jobs.Alert.Named.
describe('an alert named by its reason (CF-84)', () => {
  const a = (over: Partial<AlertRow>) => ({ key: 'k', kind: 'pod', severity: 'failed', name: 'x', status: '', statusTone: 'err', events: [], ...over }) as AlertRow
  it('says why, short, as the server does', () => {
    expect(alertReason(a({ status: 'Unknown', node: 'r620-worker' }))).toBe('Pod status Unknown: node r620-worker stopped reporting')
    expect(alertReason(a({ status: 'Unknown' }))).toBe('Pod status Unknown: its node stopped reporting')
    expect(alertReason(a({ status: 'CrashLoopBackOff', waiting: 'CrashLoopBackOff', waitingMessage: 'back-off 5m0s restarting failed container=web pod=web-1_shop(0f9c)' }))).toBe('CrashLoopBackOff: back-off restarting failed container')
    expect(alertReason(a({ status: 'ImagePullBackOff', waiting: 'ImagePullBackOff', waitingMessage: 'Back-off pulling image "registry.example/web:9"' }))).toBe('ImagePullBackOff: back-off pulling image')
    const node = (status: string, title: string, summary: string) => a({ kind: 'node', severity: 'error', status, problems: [{ title, severity: 'error', summary }] })
    expect(alertReason(node('Unknown', 'Kubelet stopped reporting', 'The node controller has not heard from the kubelet for 5m. Pods on it are not being managed.'))).toBe('Node not ready: kubelet stopped reporting')
    expect(alertReason(node('Not ready', 'Not ready', 'The kubelet reports the node is not ready, so no new pods start here.'))).toBe('Node not ready: the kubelet reports the node is not ready, so no new pods start here')
    expect(alertReason(node('Ready', 'Disk pressure', 'Disk space or inodes are running low. The kubelet evicts pods.'))).toBe('Disk pressure: disk space or inodes are running low')
    expect(alertReason(a({ severity: 'pending', status: 'Pending', why: '0/3 nodes are available: 3 Insufficient cpu.' }))).toBe('Pending: 0/3 nodes are available: 3 Insufficient cpu')
    expect(alertReason(a({ status: 'Error', lastReason: 'OOMKilled' }))).toBe('Error: OOMKilled')
    expect(alertReason(a({ kind: 'pvc', status: 'Pending' }))).toBe('Pending')
    const long = alertReason(a({ status: 'Error', lastMessage: 'a very long message '.repeat(20) }))
    expect(long).toHaveLength(120)
    expect(long.endsWith('…')).toBe(true)
  })

  it('keys the alert about a case’s object as the Alerts tab does', () => {
    expect(alertKeyOf({ namespace: 'shop', kind: 'Pod', name: 'web-1' })).toBe('pod:shop/web-1')
    expect(alertKeyOf({ kind: 'Node', name: 'n1' })).toBe('node:n1')
    expect(alertKeyOf({ namespace: 'shop', kind: 'PersistentVolumeClaim', name: 'data' })).toBe('pvc:shop/data')
    expect(alertKeyOf({ namespace: 'shop', kind: 'Deployment', name: 'web' })).toBe('')
    expect(alertKeyOf(undefined)).toBe('')
  })
})

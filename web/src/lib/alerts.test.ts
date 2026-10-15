import { describe, expect, it } from 'vitest'
import { badgeOf, bannersOf, contextOf, sectionsOf, sevLabel, tabTone, tilesOf } from './alerts'
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

  it('puts control-plane nodes apart and errors first', () => {
    const s = sectionsOf([oom, pending, pull, cordoned, down])
    expect(s.pods.map((r) => r.name)).toEqual(['a', 'b', 'c'])
    expect(s.controlPlane.map((r) => r.name)).toEqual(['cp'])
    expect(s.workers.map((r) => r.name)).toEqual(['w2'])
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
    expect(sevLabel(oom)).toEqual({ label: 'Recovered', tone: 'ok' })
  })
})

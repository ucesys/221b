import { describe, expect, it } from 'vitest'
import type { HomeCluster } from '../api/home'
import { activeAlerts, alertTone, healthOf, nodeTone, podLine, podsReady, podTone, resourceLine, storageTone } from './home'

const GI = 1024 * 1024 * 1024

function cluster(over: Partial<HomeCluster> = {}): HomeCluster {
  return {
    id: 'local',
    name: 'prod-eu',
    env: 'production',
    state: 'connected',
    synced: true,
    metrics: 'available',
    nodes: { total: 3, ready: 3, controlPlane: 1, cordoned: 0, errors: 0, warnings: 0 },
    cpu: { allocatable: 12000, requested: 3000, used: 1500, usedNodes: 3 },
    memory: { allocatable: 48 * GI, requested: 12 * GI, used: 40 * GI, usedNodes: 3 },
    pods: { total: 30, running: 30, capacity: 330, notReady: 0, failing: 0, ready: 30, unready: 0, pending: 0, completed: 0, unknown: 0 },
    namespaces: 5,
    services: 8,
    storage: { claims: 4, bound: 4, attention: 0, errors: 0 },
    alerts: { failed: 0, unknown: 0, pending: 0, nodesDown: 0, nodeErrors: 0, nodeWarnings: 0, volumes: 0, acknowledged: 0, recovered: 0 },
    ...over,
  }
}

describe('home', () => {
  it('says a cluster is healthy when nothing is wrong', () => {
    expect(healthOf(cluster())).toEqual({ tone: 'ok', text: 'Healthy', reasons: [] })
    expect(alertTone(cluster())).toBeUndefined()
  })

  it('leaves the Nodes, Pods and Alerts boxes plain when nothing is wrong, and colours each by its own trouble', () => {
    const ok = cluster({ alerts: { ...cluster().alerts, recovered: 4 } })
    expect([nodeTone(ok), podTone(ok), alertTone(ok)]).toEqual([undefined, undefined, undefined])
    expect(nodeTone(cluster({ nodes: { total: 3, ready: 3, controlPlane: 1, cordoned: 1, errors: 0, warnings: 0 } }))).toBe('warn')
    expect(nodeTone(cluster({ nodes: { total: 3, ready: 2, controlPlane: 1, cordoned: 0, errors: 0, warnings: 0 } }))).toBe('err')
    const pods = (over: Partial<HomeCluster['pods']>) => cluster({ pods: { ...cluster().pods, ...over } })
    expect(podTone(pods({ ready: 20, unready: 10 }))).toBe('warn')
    expect(podTone(pods({ ready: 28, pending: 2 }))).toBe('warn')
    expect(podTone(pods({ ready: 29, failing: 1 }))).toBe('err')
    expect(podTone(pods({ ready: 20, unready: 10, unknown: 10 }))).toBe('err')
    // Completed pods are no trouble.
    expect(podTone(pods({ total: 33, ready: 30, completed: 3 }))).toBeUndefined()
    const store = (over: Partial<HomeCluster['storage']>) => cluster({ storage: { ...cluster().storage, ...over } })
    expect([storageTone(cluster()), storageTone(store({ bound: 3 })), storageTone(store({ attention: 1 })), storageTone(store({ attention: 1, errors: 1 }))]).toEqual([undefined, 'warn', 'warn', 'err'])
  })

  it('shows the pods ready of those that have not finished, like the nodes ready', () => {
    const c = cluster({ pods: { total: 108, running: 104, capacity: 990, notReady: 10, failing: 0, ready: 94, unready: 10, pending: 0, completed: 4, unknown: 10 } })
    expect(podsReady(c)).toEqual({ value: '94/104', tip: '94 of 104 pods ready; 4 completed pods are not counted' })
    expect(podsReady(cluster()).value).toBe('30/30')
  })

  it('puts failures before warnings, and acknowledged alerts among the warnings', () => {
    const bad = cluster({ nodes: { total: 4, ready: 2, controlPlane: 1, cordoned: 0, errors: 3, warnings: 0 }, alerts: { ...cluster().alerts, failed: 2, unknown: 5, pending: 1, nodesDown: 2, nodeErrors: 1 } })
    const h = healthOf(bad)
    expect(h.tone).toBe('err')
    // The nodes not ready are not also said to have errors, and their pods are Unknown.
    expect(h.reasons).toEqual([
      { text: '2 nodes are not ready.', tone: 'err', alerts: true },
      { text: '5 pods are Unknown.', tone: 'err', alerts: true },
      { text: '2 pods fail to start.', tone: 'err', alerts: true },
      { text: '1 node has errors.', tone: 'err', alerts: true },
    ])
    expect(activeAlerts(bad)).toBe(11)
    expect(alertTone(bad)).toBe('err')
    const acked = cluster({ alerts: { ...cluster().alerts, acknowledged: 1, recovered: 2 } })
    expect(healthOf(acked)).toMatchObject({ tone: 'warn', reasons: [{ text: '1 alert is acknowledged.', tone: 'warn', alerts: true }] })
    expect(alertTone(acked)).toBe('warn')
    expect(activeAlerts(acked)).toBe(0)
  })

  it('says why a cluster it cannot reach has no figures', () => {
    expect(healthOf(cluster({ state: 'disconnected', message: 'dial tcp: i/o timeout' }))).toEqual({ tone: 'err', text: 'Can’t connect', reasons: [{ text: 'dial tcp: i/o timeout' }] })
    expect(healthOf(cluster({ synced: false })).tone).toBe('info')
  })

  it('shows CPU and memory requested and in use, of what the nodes can take', () => {
    const cpu = resourceLine('CPU', cluster().cpu, true)
    expect(cpu).toMatchObject({ reqPct: 25, usePct: 12.5, useTone: 'ok' })
    expect(cpu.text).toBe('3 of 12 cores requested (25%) · 1.5 in use (13%)')
    const mem = resourceLine('Memory', cluster().memory, true)
    expect(mem.useTone).toBe('warn')
    expect(mem.text).toBe('12Gi of 48Gi requested (25%) · 40Gi in use (83%)')
    // Without metrics-server, only what is requested.
    expect(resourceLine('CPU', cluster().cpu, false)).toMatchObject({ usePct: null, text: '3 of 12 cores requested (25%)' })
    expect(podLine(cluster()).text).toBe('30 of 330 (9%)')
  })
})

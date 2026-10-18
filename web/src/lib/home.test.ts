import { describe, expect, it } from 'vitest'
import type { HomeCluster } from '../api/home'
import { activeAlerts, alertTone, healthOf, podLine, resourceLine } from './home'

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
    pods: { total: 30, running: 30, capacity: 330, notReady: 0, failing: 0 },
    namespaces: 5,
    services: 8,
    alerts: { failed: 0, pending: 0, nodeErrors: 0, nodeWarnings: 0, volumes: 0, acknowledged: 0, recovered: 0 },
    ...over,
  }
}

describe('home', () => {
  it('says a cluster is healthy when nothing is wrong', () => {
    expect(healthOf(cluster())).toEqual({ tone: 'ok', text: 'Healthy', reasons: [] })
    expect(alertTone(cluster())).toBeUndefined()
  })

  it('puts failures before warnings, and acknowledged alerts among the warnings', () => {
    const bad = cluster({ nodes: { total: 3, ready: 2, controlPlane: 1, cordoned: 0, errors: 1, warnings: 0 }, alerts: { failed: 2, pending: 1, nodeErrors: 1, nodeWarnings: 0, volumes: 0, acknowledged: 0, recovered: 0 } })
    const h = healthOf(bad)
    expect(h.tone).toBe('err')
    expect(h.reasons).toEqual([
      { text: '1 node is not ready.', tone: 'err', alerts: true },
      { text: '2 pods fail to start.', tone: 'err', alerts: true },
      { text: '1 node has errors.', tone: 'err', alerts: true },
    ])
    expect(activeAlerts(bad)).toBe(4)
    expect(alertTone(bad)).toBe('err')
    const acked = cluster({ alerts: { failed: 0, pending: 0, nodeErrors: 0, nodeWarnings: 0, volumes: 0, acknowledged: 1, recovered: 2 } })
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

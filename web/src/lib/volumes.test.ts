import { describe, expect, it } from 'vitest'
import type { StorageClassInfo, StoragePair } from '../api/storage'
import { byClass, byNamespace, bytes, bytesLong, comparePairs, heading, labelMatch, openByDefault, pairTerms, podsOpenByDefault, totals } from './volumes'
import { matchesQuery, queryWords } from './table'

const cls = (name: string, def = false, binding = 'Immediate'): StorageClassInfo => ({ name, provisioner: 'driver.longhorn.io', reclaim: 'Delete', binding, expansion: true, default: def, created: '2026-04-01T00:00:00Z' })
const pair = (o: Partial<StoragePair>): StoragePair => ({ state: 'bound', class: 'longhorn', phase: 'Bound', tone: 'ok', created: '2026-09-01T00:00:00Z', ...o })
const GI = 2 ** 30

describe('storage', () => {
  it('orders the groups: the default class, the others by name, no class, then the missing classes (ST-04)', () => {
    const classes = [cls('zeta'), cls('longhorn', true), cls('local-nvme', false, 'WaitForFirstConsumer')]
    const pairs = [pair({ claim: 'a', namespace: 'web', class: 'zeta', volume: 'v1' }), pair({ volume: 'legacy', class: '' }), pair({ state: 'pending', phase: 'Pending', claim: 'scratch', namespace: 'batch', class: 'fast-ssd', classMissing: true }), pair({ state: 'pending', phase: 'Pending', claim: 'x', namespace: 'web', class: 'ghost', classMissing: true })]
    expect(byClass(classes, pairs).map((g) => g.key)).toEqual(['class:longhorn', 'class:local-nvme', 'class:zeta', 'none', 'missing:fast-ssd', 'missing:ghost'])
  })

  it('orders the rows of a group: Lost or Failed first, bound by namespace and claim, Pending, Released, Available (ST-04)', () => {
    const rows = [
      pair({ state: 'available', phase: 'Available', volume: 'spare' }),
      pair({ state: 'pending', phase: 'Pending', claim: 'data-cache-2', namespace: 'analytics' }),
      pair({ claim: 'data-postgres-1', namespace: 'storage', volume: 'b' }),
      pair({ state: 'released', phase: 'Released', volume: 'old', wasClaim: 'ingest/landing-zone' }),
      pair({ claim: 'data-cache-0', namespace: 'analytics', volume: 'a' }),
      pair({ state: 'lost', phase: 'Lost', claim: 'orphan', namespace: 'web' }),
      pair({ claim: 'data-postgres-0', namespace: 'storage', volume: 'c' }),
    ]
    expect([...rows].sort(comparePairs).map((p) => p.claim ?? p.volume)).toEqual(['orphan', 'data-cache-0', 'data-postgres-0', 'data-postgres-1', 'data-cache-2', 'old', 'spare'])
  })

  it('totals a class: volumes, provisioned, used, pending, waiting, Available and Released (ST-03)', () => {
    const t = totals([
      pair({ volume: 'a', claim: 'a', namespace: 'x', capacity: 100 * GI, requested: 100 * GI, usage: { used: 60 * GI, capacity: 98 * GI, available: 38 * GI, pct: 61 } }),
      pair({ state: 'pending', phase: 'Pending', claim: 'b', namespace: 'x', requested: 20 * GI, reason: { text: 'provisioning failed', tone: 'err' } }),
      pair({ state: 'pending', phase: 'Pending', claim: 'c', namespace: 'x', requested: 500 * GI, reason: { text: 'waits for a pod', tone: 'info' } }),
      pair({ state: 'released', phase: 'Released', volume: 'r', capacity: 100 * GI }),
      pair({ state: 'available', phase: 'Available', volume: 'av', capacity: 500 * GI }),
    ])
    expect(t).toMatchObject({ volumes: 3, claims: 3, provisioned: 700 * GI, used: 60 * GI, pending: 1, waiting: 1, released: 1, available: 1 })
    expect(Math.round(t.usedPct)).toBe(60)
  })

  it('groups by namespace with the quota and the Helm releases, volumes no claim holds last (ST-11)', () => {
    const g = byNamespace(
      [pair({ claim: 'b', namespace: 'batch', release: 'reports' }), pair({ claim: 'a', namespace: 'analytics', release: 'dremio' }), pair({ claim: 'c', namespace: 'analytics', release: 'datasets' }), pair({ state: 'released', phase: 'Released', volume: 'old' })],
      [{ namespace: 'analytics', name: 'storage', hard: 8 * 2 ** 40, used: 4 * 2 ** 40 }],
    )
    expect(g.map((x) => [x.key, x.releases?.join(',') ?? '', x.quota?.name ?? ''])).toEqual([
      ['ns:analytics', 'datasets,dremio', 'storage'],
      ['ns:batch', 'reports', ''],
      ['unclaimed', '', ''],
    ])
  })

  it('writes the heading for a cluster-wide role and a namespace-bound one (ST-01)', () => {
    const d = { classes: [cls('longhorn', true), cls('nfs')], quotas: [], pairs: [pair({ volume: 'a', claim: 'a', namespace: 'web', capacity: 4 * 2 ** 40, requested: 4 * 2 ** 40 }), pair({ volume: 'b', claim: 'b', namespace: 'batch', capacity: 2 * 2 ** 40, requested: 300 * GI }), pair({ state: 'available', phase: 'Available', volume: 'c', capacity: 2.6 * 2 ** 40 })] }
    expect(heading(d, true).parts.map((p) => p.text).join('')).toBe('3 volumes · 8.6 TiB provisioned · 2 classes, default longhorn · 2 claims in 2 namespaces')
    expect(heading(d, false).parts.map((p) => p.text).join('')).toBe('2 claims in batch and web · 4.3 TiB requested · the namespaces your role can see')
  })

  it('formats sizes with three figures at most', () => {
    expect([bytes(740 * GI), bytes(61.2 * GI), bytes(1310 * GI), bytes(2 * 2 ** 40), bytes(20 * GI)]).toEqual(['740Gi', '61.2Gi', '1.28Ti', '2Ti', '20Gi'])
    expect(bytesLong(8.6 * 2 ** 40)).toBe('8.6 TiB')
  })

  it('searches names, namespaces, pods, classes and nodes; selects by label; opens pod sub-rows when a pod waits (ST-07, ST-10)', () => {
    const p = pair({ claim: 'data-kafka-0', namespace: 'ingest', volume: 'pvc-5e19', claimLabels: { app: 'kafka' }, pods: [{ name: 'kafka-0', node: 'worker-003', status: 'Running', tone: 'ok' }], nodes: ['worker-003'] })
    for (const q of ['kafka', 'ingest', 'worker-003', 'longhorn', 'pvc-5e19']) expect(matchesQuery(pairTerms(p), queryWords(q))).toBe(true)
    expect(matchesQuery(pairTerms(p), queryWords('postgres'))).toBe(false)
    expect(labelMatch(p, ['app=kafka'])).toBe(true)
    expect(labelMatch(p, ['app=kafka', 'app=zk'])).toBe(true)
    expect(labelMatch(p, ['app=zk'])).toBe(false)
    expect(podsOpenByDefault({ ...p, pods: [...p.pods!, { name: 'kafka-1', status: 'Pending', tone: 'warn' }] })).toBe(true)
    expect(podsOpenByDefault({ ...p, pods: [...p.pods!, { name: 'kafka-1', status: 'Running', tone: 'ok' }] })).toBe(false)
  })

  it('opens every group while there are 12 or fewer, beyond that only those that need attention', () => {
    const g = byClass([cls('a')], [pair({ class: 'a', volume: 'x', attention: true })])[0]!
    expect(openByDefault(g, 12)).toBe(true)
    expect(openByDefault({ ...g, attention: 0 }, 13)).toBe(false)
    expect(openByDefault(g, 50)).toBe(true)
  })

  it('groups and sorts 2,000 claims in 50 classes within 200 ms (ST-12, ST-25)', () => {
    const classes = Array.from({ length: 50 }, (_, i) => cls(`class-${i}`, i === 0))
    const pairs = Array.from({ length: 2000 }, (_, i) => pair({ claim: `data-${i}`, namespace: `ns-${i % 40}`, volume: `pvc-${i}`, class: `class-${i % 50}`, capacity: (i % 100) * GI, state: i % 97 ? 'bound' : 'pending', phase: i % 97 ? 'Bound' : 'Pending' }))
    const start = performance.now()
    const groups = byClass(classes, pairs)
    byNamespace(pairs, [])
    ;[...pairs].sort(comparePairs)
    ;[...pairs].sort((a, b) => (b.capacity ?? 0) - (a.capacity ?? 0))
    const took = performance.now() - start
    expect(groups).toHaveLength(50)
    expect(groups.reduce((n, g) => n + g.pairs.length, 0)).toBe(2000)
    expect(took).toBeLessThan(200)
  })
})

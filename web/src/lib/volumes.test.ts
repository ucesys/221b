import { describe, expect, it } from 'vitest'
import type { StorageClassInfo, StoragePair } from '../api/storage'
import { bytes, bytesLong, classEntries, classKeyOf, comparePairs, heading, labelMatch, namespaceEntries, NO_CLAIM, NO_CLASS, NO_NODE, nodeEntries, nodeKeysOf, nsKeyOf, pairTerms, podsOpenByDefault, quotaText, scopeText, sidebarMatch, totals, type Selections } from './volumes'
import { matchesQuery, queryWords } from './table'

const cls = (name: string, def = false, binding = 'Immediate'): StorageClassInfo => ({ name, provisioner: 'driver.longhorn.io', reclaim: 'Delete', binding, expansion: true, default: def, created: '2026-04-01T00:00:00Z' })
const pair = (o: Partial<StoragePair>): StoragePair => ({ state: 'bound', class: 'longhorn', phase: 'Bound', tone: 'ok', created: '2026-09-01T00:00:00Z', ...o })
const GI = 2 ** 30

describe('storage', () => {
  it('orders rows: Lost or Failed first, bound by namespace and claim, Pending, Released, Available (ST-04)', () => {
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

  it('lists the namespaces with claims and their quota, No claim first (ST-02)', () => {
    const e = namespaceEntries(
      [pair({ claim: 'b', namespace: 'batch' }), pair({ claim: 'a', namespace: 'analytics' }), pair({ claim: 'c', namespace: 'analytics' }), pair({ state: 'released', phase: 'Released', volume: 'old' })],
      [{ namespace: 'analytics', name: 'storage', hard: 8 * 2 ** 40, used: 4 * 2 ** 40 }],
    )
    expect(e.map((x) => [x.key, x.sub ?? ''])).toEqual([
      [NO_CLAIM, 'Available, Released, Failed volumes'],
      ['analytics', 'quota 4.0Ti of 8.0Ti · 50%'],
      ['batch', ''],
    ])
    expect(namespaceEntries([pair({ claim: 'a', namespace: 'web' })], []).map((x) => x.key)).toEqual(['web'])
    expect(quotaText({ namespace: 'x', name: 'q', hard: 100 * GI, used: 0 })).toBe('quota 0 of 100Gi · 0%')
  })

  it('lists the nodes rows are on, Not on a node first, control plane before workers; an RWX claim is under each of its nodes (ST-02)', () => {
    const rwx = pair({ claim: 'media', namespace: 'web', nodes: ['w2', 'w1'], attached: ['w1'] })
    const pairs = [rwx, pair({ state: 'pending', phase: 'Pending', claim: 'p', namespace: 'web' }), pair({ claim: 'etcd', namespace: 'kube-system', nodes: ['cp-1'] })]
    expect(nodeKeysOf(rwx).sort()).toEqual(['w1', 'w2'])
    expect(nodeKeysOf(pairs[1]!)).toEqual([NO_NODE])
    const withNodes = nodeEntries(pairs, [{ name: 'cp-1', role: 'control-plane', cordoned: false }, { name: 'w1', role: 'worker', cordoned: true }, { name: 'w2', role: 'worker', cordoned: false }])
    expect(withNodes.map((x) => [x.key, x.tag?.text ?? '', x.flag ?? ''])).toEqual([
      [NO_NODE, '', ''],
      ['cp-1', 'control-plane', ''],
      ['w1', 'worker', 'cordoned'],
      ['w2', 'worker', ''],
    ])
    // Names only for a role that may not list nodes.
    expect(nodeEntries(pairs, []).map((x) => [x.key, x.tag])).toEqual([
      [NO_NODE, undefined],
      ['cp-1', undefined],
      ['w1', undefined],
      ['w2', undefined],
    ])
  })

  it('lists every class with the default first, then No class and the missing ones; names from the claims for a role that may not list classes (ST-02)', () => {
    const pairs = [pair({ claim: 'a', namespace: 'x', class: 'nfs' }), pair({ volume: 'legacy', class: '' }), pair({ state: 'pending', phase: 'Pending', claim: 's', namespace: 'batch', class: 'fast-ssd', classMissing: true })]
    const full = classEntries(pairs, [cls('nfs'), cls('longhorn', true), cls('unused')], true)
    expect(full.map((x) => [x.key, x.tag?.text ?? '', !!x.err])).toEqual([
      ['longhorn', 'default', false],
      ['nfs', '', false],
      ['unused', '', false],
      [NO_CLASS, '', false],
      ['fast-ssd', 'not found', true],
    ])
    expect(full[0]!.sub).toBe('driver.longhorn.io')
    expect(classEntries(pairs, [], false).map((x) => x.key)).toEqual(['nfs', NO_CLASS, 'fast-ssd'])
    expect(classKeyOf(pairs[1]!)).toBe(NO_CLASS)
    expect(nsKeyOf(pairs[1]!)).toBe(NO_CLAIM)
  })

  it('filters by namespace, node and class; a section leaves itself out so its counts follow the other two (ST-02)', () => {
    const a = pair({ claim: 'a', namespace: 'analytics', nodes: ['w1'], class: 'longhorn' })
    const b = pair({ claim: 'b', namespace: 'batch', nodes: ['w2'], class: 'nfs' })
    const free = pair({ state: 'available', phase: 'Available', volume: 'spare', class: '' })
    const s: Selections = { ns: new Set(['analytics', NO_CLAIM]), node: null, cls: null }
    expect([a, b, free].filter((x) => sidebarMatch(x, s)).map((x) => x.claim ?? x.volume)).toEqual(['a', 'spare'])
    const noNode: Selections = { ns: null, node: new Set([NO_NODE]), cls: null }
    expect([a, b, free].filter((x) => sidebarMatch(x, noNode)).map((x) => x.volume)).toEqual(['spare'])
    const noClass: Selections = { ns: null, node: null, cls: new Set([NO_CLASS]) }
    expect([a, b, free].filter((x) => sidebarMatch(x, noClass)).map((x) => x.volume)).toEqual(['spare'])
    // Nothing selected ("-"): no row; leaving the section out, every row.
    const none: Selections = { ns: new Set(), node: null, cls: null }
    expect([a, b].filter((x) => sidebarMatch(x, none))).toEqual([])
    expect([a, b].filter((x) => sidebarMatch(x, none, 'ns'))).toHaveLength(2)
  })

  it('says the selection in the scope line', () => {
    const e = namespaceEntries([pair({ claim: 'a', namespace: 'analytics' }), pair({ claim: 'b', namespace: 'batch' }), pair({ claim: 'c', namespace: 'web' })], [])
    expect(scopeText(null, e, 'namespace', 'namespaces')).toBe('all namespaces')
    expect(scopeText(new Set(['analytics']), e, 'namespace', 'namespaces')).toBe('analytics')
    expect(scopeText(new Set(['analytics', 'web']), e, 'namespace', 'namespaces')).toBe('2 namespaces')
    expect(scopeText(new Set(), e, 'namespace', 'namespaces')).toBe('no namespaces')
    const n = nodeEntries([pair({ state: 'pending', phase: 'Pending', claim: 'p', namespace: 'x' }), pair({ claim: 'q', namespace: 'x', nodes: ['w1'] })], [])
    expect(scopeText(new Set([NO_NODE]), n, 'node', 'nodes')).toBe('not on a node')
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

  it('filters and sorts 2,000 claims in 50 classes within 200 ms (ST-12, ST-25)', () => {
    const classes = Array.from({ length: 50 }, (_, i) => cls(`class-${i}`, i === 0))
    const pairs = Array.from({ length: 2000 }, (_, i) => pair({ claim: `data-${i}`, namespace: `ns-${i % 40}`, volume: `pvc-${i}`, class: `class-${i % 50}`, nodes: [`w${i % 30}`], capacity: (i % 100) * GI, state: i % 97 ? 'bound' : 'pending', phase: i % 97 ? 'Bound' : 'Pending' }))
    const start = performance.now()
    const entries = [namespaceEntries(pairs, []), nodeEntries(pairs, []), classEntries(pairs, classes, true)]
    const s: Selections = { ns: new Set(['ns-1', 'ns-2']), node: null, cls: new Set(['class-1', 'class-2', 'class-11']) }
    const shown = pairs.filter((x) => sidebarMatch(x, s))
    ;[...shown].sort(comparePairs)
    ;[...pairs].sort((a, b) => (b.capacity ?? 0) - (a.capacity ?? 0))
    const took = performance.now() - start
    expect(entries.map((e) => e.length)).toEqual([40, 30, 50])
    expect(shown.length).toBeGreaterThan(0)
    expect(took).toBeLessThan(200)
  })
})

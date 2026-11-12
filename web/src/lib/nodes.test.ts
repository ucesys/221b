import { describe, expect, it } from 'vitest'
import { controlPlane, row } from '../test/rows'
import { facetMatch, facetText, memoryMeter, quantity, nodeFacets, ownLabels, problemsCell, reserved, reservedText, resourceSections, roleTag, taintShort, usedPct } from './nodes'

const GI = 1024 ** 3

const rows = [
  controlPlane('cp-1', { cpu: { requested: 1000, limits: 0, allocatable: 4000, capacity: 4000, used: 500 } }),
  row('worker-10', { cpu: { requested: 12000, limits: 16000, allocatable: 16000, capacity: 16000, used: 9000 }, zone: 'zone-b' }),
  row('worker-2', {
    status: 'Cordoned',
    tone: 'warn',
    cordoned: true,
    warnings: 1,
    problems: [{ title: 'Cordoned', severity: 'warning', summary: '20 pods are still running on it until the node is drained.', entries: [] }],
    taints: [{ key: 'node.kubernetes.io/unschedulable', effect: 'NoSchedule' }],
  }),
  row('worker-3', {
    status: 'Not ready',
    tone: 'err',
    errors: 1,
    warnings: 1,
    problems: [
      { title: 'Not ready', severity: 'error', summary: 'The kubelet stopped posting status.', entries: [] },
      { title: 'Disk pressure', severity: 'warning', summary: 'The kubelet is evicting pods.', entries: [] },
    ],
    labels: { workload: 'analytics' },
    pods: { running: 3, capacity: 110, notReady: 2, failing: 1, starting: 1, unready: 0, completed: 4 },
    cpu: { requested: 2000, limits: 4000, allocatable: 16000, capacity: 16000, used: null },
    memory: { requested: 8 * GI, limits: 8 * GI, allocatable: 64 * GI, capacity: 64 * GI, used: null },
  }),
]

describe('label and taint facets', () => {
  it('lists labels by key and taints with counts, leaving kubernetes.io labels out', () => {
    const f = nodeFacets(rows)
    expect(f.labels).toEqual([{ key: 'workload', values: [{ text: 'workload=analytics', value: 'analytics', count: 1 }] }])
    expect(f.taints.map((t) => [t.id, t.count])).toEqual([
      ['node-role.kubernetes.io/control-plane:NoSchedule', 1],
      ['node.kubernetes.io/unschedulable:NoSchedule', 1],
    ])
  })

  it('matches any value of a label key, and every key and taint', () => {
    const analytics = { labels: ['workload=analytics'], taints: [] }
    expect(rows.filter((r) => facetMatch(r, analytics)).map((r) => r.name)).toEqual(['worker-3'])
    expect(rows.filter((r) => facetMatch(r, { labels: ['workload=analytics', 'workload=web'], taints: [] })).map((r) => r.name)).toEqual(['worker-3'])
    expect(rows.filter((r) => facetMatch(r, { labels: [], taints: ['node.kubernetes.io/unschedulable:NoSchedule'] })).map((r) => r.name)).toEqual(['worker-2'])
    expect(rows.filter((r) => facetMatch(r, { labels: ['workload=analytics'], taints: ['node.kubernetes.io/unschedulable:NoSchedule'] }))).toEqual([])
    expect(facetText({ labels: ['zone=a', 'zone=b', 'tier=db'], taints: ['x:NoSchedule'] })).toBe('zone=a or zone=b, tier=db, x:NoSchedule')
  })
})

describe('cells', () => {
  it('describes problems in the table', () => {
    expect(problemsCell(rows[3]!)).toMatchObject({ text: '1 error · 1 warn', tone: 'err' })
    expect(problemsCell(rows[2]!)).toMatchObject({ text: '1 warning', tone: 'warn' })
    expect(problemsCell(rows[0]!)).toMatchObject({ text: '—', tone: 'muted' })
  })

  it('shortens taints', () => {
    expect(taintShort({ key: 'node-role.kubernetes.io/control-plane', effect: 'NoSchedule' })).toBe('control-plane')
    expect(taintShort({ key: 'dedicated', value: 'analytics', effect: 'NoSchedule' })).toBe('dedicated=analytics')
  })

  it("tags roles and lists a node's own labels", () => {
    expect(roleTag(rows[0]!)).toEqual({ text: 'master', title: 'Control plane' })
    expect(roleTag(controlPlane('cp-2', { roles: ['control-plane', 'etcd'] }))).toEqual({ text: 'master', title: 'Control plane (also etcd)' })
    expect(roleTag(rows[1]!)).toEqual({ text: 'worker', title: 'Worker (worker)' })
    expect(ownLabels(rows[3]!)).toEqual(['workload=analytics'])
    expect(ownLabels(rows[1]!)).toEqual([])
  })
})

describe('resource sections', () => {
  it('counts in use against the machine, so software beside Kubernetes does not read as over 100%', () => {
    const r = { ...rows[1]!, memory: { requested: 0, limits: 0, allocatable: 32 * GI, capacity: 64 * GI, used: 48 * GI } }
    expect(usedPct(r.memory)).toBe(75)
    expect(memoryMeter(r).usePct).toBe(75)
    expect(resourceSections(r, true, '')[1]!.usePct).toBe(75)
    expect(reserved(r.memory)).toBe(32 * GI)
  })

  it('lists a total, each reservation and what is allocatable, as the kubelet sets them', () => {
    const r = { ...rows[1]!, cpu: { ...rows[1]!.cpu, capacity: 16000, allocatable: 13500 }, memory: { ...rows[1]!.memory, capacity: 64 * GI, allocatable: 47 * GI } }
    const cfg = { systemReserved: { cpu: '2', memory: '12Gi' }, kubeReserved: { cpu: '500m', memory: '4Gi' }, evictionHard: { 'memory.available': '1Gi', 'nodefs.available': '5%' } }
    const [cpu, memory] = resourceSections(r, true, '', cfg)
    expect(cpu!.lines.slice(0, 4).map((l) => [l.k, l.v])).toEqual([
      ['Total on the node', '16 cores'],
      ['Reserved for the system', '2'],
      ['Reserved for Kubernetes', '0.5'],
      ['Allocatable', '13.5 cores'],
    ])
    expect(memory!.lines.slice(0, 5).map((l) => [l.k, l.v])).toEqual([
      ['Total on the node', '64Gi'],
      ['Reserved for the system', '12Gi'],
      ['Reserved for Kubernetes', '4Gi'],
      ['Eviction threshold', '1Gi'],
      ['Allocatable', '47Gi'],
    ])
    const unread = resourceSections(r, true, '')[0]!
    expect(unread.lines.slice(0, 3).map((l) => [l.k, l.v])).toEqual([
      ['Total on the node', '16 cores'],
      ['Reserved', '2.5'],
      ['Allocatable', '13.5 cores'],
    ])
    expect(quantity('500m', 'cpu')).toBe(500)
    expect(quantity('5%', 'memory')).toBe(0)
  })

  it('says what a node keeps from pods', () => {
    const one = (cpu: number, memory: number) => ({ cpu: { allocatable: 8000 - cpu, capacity: 8000 }, memory: { allocatable: 64 * GI - memory, capacity: 64 * GI } })
    expect(reservedText(one(0, 0))).toBe('none')
    expect(reservedText(one(2000, 16 * GI))).toBe('2 cores · 16Gi')
    expect(reservedText(one(0, 16 * GI))).toBe('16Gi')
  })

  it('lists allocatable, requested, free, in use and limits', () => {
    const [cpu, memory, pods] = resourceSections(rows[1]!, true, '')
    expect(cpu!.lines.map((l) => [l.k, l.v, l.sub])).toEqual([
      ['Total on the node', '16 cores', undefined],
      ['Allocatable', '16 cores', 'pods may have this'],
      ['Requested', '12', '75%'],
      ['Free to schedule', '4', '25%'],
      ['In use', '9', '56% of the machine'],
      ['Limits', '16', '100%'],
    ])
    expect(memory!.lines[1]).toMatchObject({ k: 'Allocatable', v: '64Gi' })
    expect(memory!.lines[3]).toMatchObject({ k: 'Free to schedule', v: '48Gi', sub: '75%' })
    expect(pods!.lines.map((l) => [l.k, l.v])).toEqual([
      ['Capacity', '110'],
      ['Scheduled', '20'],
      ['Free slots', '90'],
      ['Not ready', '0'],
      ['Completed', '0'],
    ])
  })

  it('says why usage is missing', () => {
    const [cpu] = resourceSections(rows[3]!, true, '')
    expect(cpu!.lines.find((l) => l.k === 'In use')).toMatchObject({ v: '—' })
    expect(cpu!.note).toBe('metrics-server has no reading for this node.')
    const [without] = resourceSections(rows[1]!, false, 'metrics-server is not installed, so CPU and memory in use are not shown.')
    expect(without!.note).toContain('metrics-server is not installed')
    expect(without!.usePct).toBe(0)
    const [, , pods] = resourceSections(rows[3]!, false, '')
    expect(pods!.lines[3]).toMatchObject({ k: 'Not ready', v: '2', sub: '1 failing, 1 still starting', tone: 'err' })
  })
})

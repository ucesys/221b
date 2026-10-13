import { describe, expect, it } from 'vitest'
import { controlPlane, row } from '../test/rows'
import {
  COLUMNS,
  DEFAULT_HIDDEN,
  exportJSON,
  exportSheet,
  filterRows,
  gridTemplate,
  ownLabels,
  problemsCell,
  resourceSections,
  roleTag,
  sortRows,
  taintShort,
  visibleColumns,
  type ColumnId,
} from './nodes'

const GI = 1024 ** 3
const now = Date.parse('2026-10-12T12:00:00Z')

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
    labels: { 'workload': 'analytics' },
    pods: { running: 3, capacity: 110, notReady: 2, failing: 1, starting: 1, unready: 0, completed: 4 },
    cpu: { requested: 2000, limits: 4000, allocatable: 16000, capacity: 16000, used: null },
    memory: { requested: 8 * GI, limits: 8 * GI, allocatable: 64 * GI, capacity: 64 * GI, used: null },
  }),
]

describe('filtering', () => {
  it('filters by role', () => {
    expect(filterRows(rows, 'control-plane', '').map((r) => r.name)).toEqual(['cp-1'])
    expect(filterRows(rows, 'worker', '').length).toBe(3)
    expect(filterRows(rows, 'all', '').length).toBe(4)
  })

  it('searches names, labels and taints', () => {
    expect(filterRows(rows, 'all', 'WORKER-1').map((r) => r.name)).toEqual(['worker-10'])
    expect(filterRows(rows, 'all', 'workload=analytics').map((r) => r.name)).toEqual(['worker-3'])
    expect(filterRows(rows, 'all', 'unschedulable').map((r) => r.name)).toEqual(['worker-2'])
    expect(filterRows(rows, 'all', 'nothing-matches')).toEqual([])
  })
})

describe('sorting', () => {
  const names = (col: ColumnId, dir: 'asc' | 'desc') => sortRows(rows, col, dir, now).map((r) => r.name)

  it('sorts names naturally', () => {
    expect(names('name', 'asc')).toEqual(['cp-1', 'worker-2', 'worker-3', 'worker-10'])
    expect(names('name', 'desc')).toEqual(['worker-10', 'worker-3', 'worker-2', 'cp-1'])
  })

  it('sorts CPU by share of allocatable, ties by name', () => {
    expect(names('cpu', 'desc')).toEqual(['worker-10', 'cp-1', 'worker-2', 'worker-3'])
  })

  it('puts problems first', () => {
    expect(names('status', 'desc')[0]).toBe('worker-3')
    expect(names('problems', 'desc').slice(0, 2)).toEqual(['worker-3', 'worker-2'])
  })

  it('puts tainted nodes first', () => {
    expect(names('taints', 'asc').slice(0, 2)).toEqual(['cp-1', 'worker-2'])
  })

  it('sorts roles, free resources and labels', () => {
    expect(names('role', 'asc')[0]).toBe('cp-1')
    expect(names('role', 'desc')[3]).toBe('cp-1')
    // Free CPU: cp-1 3, worker-10 4, worker-2 12, worker-3 14 cores.
    expect(names('cpuFree', 'desc')).toEqual(['worker-3', 'worker-2', 'worker-10', 'cp-1'])
    expect(names('cpuAlloc', 'asc')[0]).toBe('cp-1')
    expect(names('memFree', 'asc')[0]).toBe('cp-1')
    expect(names('labels', 'desc')[0]).toBe('worker-3')
  })

  it('does not change the input', () => {
    const before = rows.map((r) => r.name)
    sortRows(rows, 'cpu', 'desc', now)
    expect(rows.map((r) => r.name)).toEqual(before)
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

  it('tags roles and lists a node\'s own labels', () => {
    expect(roleTag(rows[0]!)).toEqual({ text: 'cp', title: 'Control plane' })
    expect(roleTag(controlPlane('cp-2', { roles: ['control-plane', 'etcd'] }))).toEqual({ text: 'cp', title: 'Control plane (also etcd)' })
    expect(roleTag(rows[1]!)).toEqual({ text: 'worker', title: 'Worker (worker)' })
    expect(ownLabels(rows[3]!)).toEqual(['workload=analytics'])
    expect(ownLabels(rows[1]!)).toEqual([])
  })
})

describe('resource sections', () => {
  it('lists allocatable, requested, free, in use and limits', () => {
    const [cpu, memory, pods] = resourceSections(rows[1]!, true, '')
    expect(cpu!.lines.map((l) => [l.k, l.v, l.sub])).toEqual([
      ['Allocatable', '16 cores', ''],
      ['Requested', '12', '75%'],
      ['Free to schedule', '4', '25%'],
      ['In use', '9', '56%'],
      ['Limits', '16', '100%'],
    ])
    expect(memory!.lines[0]).toMatchObject({ k: 'Allocatable', v: '64Gi' })
    expect(memory!.lines[2]).toMatchObject({ k: 'Free to schedule', v: '48Gi', sub: '75%' })
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
    expect(cpu!.lines[3]).toMatchObject({ k: 'In use', v: '—' })
    expect(cpu!.note).toBe('metrics-server has no reading for this node.')
    const [without] = resourceSections(rows[1]!, false, 'metrics-server is not installed, so CPU and memory in use are not shown.')
    expect(without!.note).toContain('metrics-server is not installed')
    expect(without!.usePct).toBe(0)
    const [, , pods] = resourceSections(rows[3]!, false, '')
    expect(pods!.lines[3]).toMatchObject({ k: 'Not ready', v: '2', sub: '1 failing, 1 still starting', tone: 'err' })
  })
})

describe('columns and export', () => {
  it('hides taints, zone, labels and kubelet until picked', () => {
    expect([...DEFAULT_HIDDEN].sort()).toEqual(['kubelet', 'labels', 'taints', 'zone'])
    expect(visibleColumns(DEFAULT_HIDDEN, false).map((c) => c.id)).toEqual([
      'name',
      'role',
      'status',
      'problems',
      'cpu',
      'cpuAlloc',
      'cpuFree',
      'memory',
      'memAlloc',
      'memFree',
      'pods',
      'age',
    ])
  })

  it('always shows the first column and drops wide-only ones beside the panel', () => {
    const all = new Set<ColumnId>(COLUMNS.map((c) => c.id))
    expect(visibleColumns(all, false).map((c) => c.id)).toEqual(['name'])
    expect(visibleColumns(new Set(), true).map((c) => c.id)).toEqual(['name', 'role', 'status', 'problems', 'cpu', 'memory', 'pods'])
    expect(visibleColumns(new Set(['zone']), false).map((c) => c.id)).not.toContain('zone')
  })

  it('sizes the grid from the columns', () => {
    const { template, minWidth } = gridTemplate(visibleColumns(new Set(['role', 'taints', 'zone', 'labels', 'kubelet', 'age', 'memory', 'memAlloc', 'memFree', 'cpu', 'cpuAlloc', 'cpuFree']), false), false)
    expect(template).toBe('minmax(180px, 1.4fr) 100px 106px 84px')
    expect(minWidth).toBe(180 + 100 + 106 + 84)
  })

  it('exports the rows and columns on screen, in screen order', () => {
    const cols = visibleColumns(new Set(['problems', 'taints', 'kubelet']), false)
    const shown = sortRows(rows, 'cpu', 'desc', now)
    const sheet = exportSheet(shown, cols)
    expect(sheet.head).toEqual([
      'Node',
      'Role',
      'Status',
      'CPU requested (cores)',
      'CPU requested %',
      'CPU in use (cores)',
      'CPU allocatable (cores)',
      'CPU free (cores)',
      'Memory requested (GiB)',
      'Memory requested %',
      'Memory in use (GiB)',
      'Memory allocatable (GiB)',
      'Memory free (GiB)',
      'Pods',
      'Pod capacity',
      'Zone',
      'Labels',
      'Joined',
    ])
    expect(sheet.rows.map((r) => r[0])).toEqual(shown.map((r) => r.name))
    expect(sheet.rows[0]).toEqual(['worker-10', 'Worker', 'Ready', 12, 75, 9, 16, 4, 16, 25, 8, 64, 48, 20, 110, 'zone-b', '', '2026-09-01 00:00:00 UTC'])
    // No reading from metrics-server stays empty rather than 0.
    expect(sheet.rows[3]![5]).toBeNull()
  })

  it('exports the same columns as JSON, with lists as arrays', () => {
    const cols = visibleColumns(new Set<ColumnId>(['cpu', 'cpuAlloc', 'cpuFree', 'memory', 'memAlloc', 'memFree', 'pods', 'zone', 'kubelet', 'age']), false)
    const out = exportJSON(rows, cols)
    expect(out.length).toBe(4)
    expect(out[0]).toEqual({ Node: 'cp-1', Role: 'Control plane', Status: 'Ready', Problems: [], Taints: ['node-role.kubernetes.io/control-plane:NoSchedule'], Labels: [] })
    expect(out[3]).toMatchObject({ Node: 'worker-3', Problems: ['Not ready', 'Disk pressure'], Labels: ['workload=analytics'] })
    expect(JSON.parse(JSON.stringify(out))).toEqual(out)
  })
})

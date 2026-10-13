import { describe, expect, it } from 'vitest'
import { controlPlane, row } from '../test/rows'
import {
  COLUMNS,
  capacityCards,
  exportSheet,
  filterRows,
  gridTemplate,
  problemsCell,
  sortRows,
  summaryLine,
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

  it('does not change the input', () => {
    const before = rows.map((r) => r.name)
    sortRows(rows, 'cpu', 'desc', now)
    expect(rows.map((r) => r.name)).toEqual(before)
  })
})

describe('summary', () => {
  it('counts the whole cluster', () => {
    expect(summaryLine(rows, rows)).toBe(
      '1 control-plane node · 3 workers · 63 pods · 1 cordoned · 1 node with errors · 1 with warnings — cluster totals below',
    )
  })

  it('counts what a filter shows', () => {
    const shown = filterRows(rows, 'worker', 'worker-1')
    expect(summaryLine(rows, shown)).toBe('Showing 1 of 4 nodes · 20 pods — totals below are for these nodes')
    expect(summaryLine(rows, [])).toBe('No nodes match the filters below')
  })

  it('describes problems in the table', () => {
    expect(problemsCell(rows[3]!)).toMatchObject({ text: '1 error · 1 warn', tone: 'err' })
    expect(problemsCell(rows[2]!)).toMatchObject({ text: '1 warning', tone: 'warn' })
    expect(problemsCell(rows[0]!)).toMatchObject({ text: '—', tone: 'muted' })
  })

  it('shortens taints', () => {
    expect(taintShort({ key: 'node-role.kubernetes.io/control-plane', effect: 'NoSchedule' })).toBe('control-plane')
    expect(taintShort({ key: 'dedicated', value: 'analytics', effect: 'NoSchedule' })).toBe('dedicated=analytics')
  })
})

describe('capacity cards', () => {
  it('totals equal the sums over the rows', () => {
    const [cpu, memory, pods] = capacityCards(rows, 2, true, '')
    // Allocatable 4 + 16 + 16 + 16 cores; requested 1 + 12 + 4 + 2.
    expect(cpu!.total).toBe('52 cores allocatable')
    expect(cpu!.legend[0]).toMatchObject({ k: 'Requested', v: '19' })
    expect(cpu!.hero).toBe('33')
    // Used sums the nodes metrics-server reports on and says so.
    expect(cpu!.legend[1]).toMatchObject({ k: 'Used', v: '11.5' })
    expect(cpu!.tip).toContain('the 3 of 4 nodes')
    expect(memory!.total).toBe('256Gi allocatable')
    expect(pods!.total).toBe('440 max')
    expect(pods!.legend[0]).toMatchObject({ k: 'Scheduled', v: '63' })
    // 1 failing, 1 starting and 2 without a node.
    expect(pods!.legend[1]).toMatchObject({ k: 'Not ready', v: '4' })
    expect(pods!.note).toBe('4 pods are not ready: 1 failing, 3 pending (2 without a node).')
  })

  it('marks usage as unavailable without metrics-server', () => {
    const [cpu] = capacityCards(rows, 0, false, 'metrics-server is not installed, so CPU and memory in use are not shown.')
    expect(cpu!.legend[1]).toMatchObject({ v: '—', sub: '' })
    expect(cpu!.usePct).toBe(0)
    expect(cpu!.note).toContain('metrics-server is not installed')
  })
})

describe('columns and export', () => {
  it('always shows the first column and drops wide-only ones beside the panel', () => {
    const all = new Set<ColumnId>(COLUMNS.map((c) => c.id))
    expect(visibleColumns(all, false).map((c) => c.id)).toEqual(['name'])
    expect(visibleColumns(new Set(), true).map((c) => c.id)).toEqual(['name', 'status', 'problems', 'cpu', 'memory', 'pods'])
    expect(visibleColumns(new Set(['zone']), false).map((c) => c.id)).not.toContain('zone')
  })

  it('sizes the grid from the columns', () => {
    const { template, minWidth } = gridTemplate(visibleColumns(new Set(['taints', 'zone', 'kubelet', 'age', 'memory', 'cpu']), false), false)
    expect(template).toBe('minmax(190px, 1.4fr) 108px 112px 88px')
    expect(minWidth).toBe(190 + 108 + 112 + 88)
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
      'CPU allocatable (cores)',
      'CPU requested %',
      'CPU in use (cores)',
      'Memory requested (GiB)',
      'Memory allocatable (GiB)',
      'Memory requested %',
      'Memory in use (GiB)',
      'Pods',
      'Pod capacity',
      'Zone',
      'Joined',
    ])
    expect(sheet.rows.map((r) => r[0])).toEqual(shown.map((r) => r.name))
    expect(sheet.rows[0]).toEqual(['worker-10', 'Worker', 'Ready', 12, 16, 75, 9, 16, 64, 25, 8, 20, 110, 'zone-b', '2026-09-01 00:00:00 UTC'])
    // No reading from metrics-server stays empty rather than 0.
    expect(sheet.rows[3]![6]).toBeNull()
  })
})

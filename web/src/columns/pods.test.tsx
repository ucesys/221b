import { describe, expect, it } from 'vitest'
import { fmtTs } from '../lib/format'
import type { PodView } from '../api/types'
import { exportTable, sortRows, visibleColumns, defaultHidden } from '../lib/table'
import { POD_COLUMNS, podStatusesShown, podStatusGroup, podStatusZero, podTerms } from './pods'

const now = Date.parse('2026-10-13T12:00:00Z')

function pod(name: string, over: Partial<PodView> = {}): PodView {
  return {
    key: 'web/' + name,
    name,
    namespace: 'web',
    uid: 'u-' + name,
    node: 'worker-1',
    status: 'Running',
    tone: 'ok',
    phase: 'Running',
    ready: 1,
    total: 1,
    restarts: 0,
    cpuRequest: 250,
    cpuLimit: 500,
    memoryRequest: 256 * 1024 * 1024,
    memoryLimit: 512 * 1024 * 1024,
    started: '2026-10-13T10:00:00Z',
    created: '2026-10-13T10:00:00Z',
    labels: { app: 'web' },
    done: false,
    usage: { cpuMilli: 120, memoryBytes: 300 * 1024 * 1024 },
    ...over,
  }
}

const rows = [
  pod('web-1'),
  pod('web-2', { status: 'CrashLoopBackOff', tone: 'err', restarts: 6, lastRestart: '2026-10-13T11:59:00Z', lastTermination: 'Error, exit 1', usage: null }),
  pod('job-1', { status: 'Completed', tone: 'muted', phase: 'Succeeded', done: true, node: 'worker-2', labels: {} }),
  pod('web-3', { status: 'Pending', tone: 'warn', phase: 'Pending', node: '', usage: null }),
]

describe('pod columns', () => {
  it('groups statuses for the segments', () => {
    expect(rows.map(podStatusGroup)).toEqual(['running', 'failing', 'completed', 'pending'])
    expect(podStatusGroup(pod('x', { status: 'Init:1/2', phase: 'Pending' }))).toBe('pending')
    // Running but not ready: apart from running, as the home page counts it.
    expect(podStatusGroup(pod('x', { status: 'Running', tone: 'warn' }))).toBe('notready')
    // On a node that stopped reporting: unknown, not failing.
    expect(podStatusGroup(pod('x', { status: 'Unknown', tone: 'err', readyUnknown: true }))).toBe('unknown')
  })

  it('shows the segments with pods, and the selected one until another is chosen', () => {
    const counts = { ...podStatusZero(3), running: 2, unknown: 1 }
    expect(podStatusesShown(counts, 'all').map(([id]) => id)).toEqual(['all', 'running', 'unknown'])
    expect(podStatusesShown({ ...counts, unknown: 0 }, 'unknown').map(([id]) => id)).toEqual(['all', 'running', 'unknown'])
  })

  it('sorts failing pods first and missing usage last', () => {
    expect(sortRows(rows, POD_COLUMNS, { col: 'status', dir: 'desc' }, now).map((r) => r.name)).toEqual(['web-2', 'web-3', 'web-1', 'job-1'])
    expect(sortRows(rows, POD_COLUMNS, { col: 'cpuUse', dir: 'desc' }, now).map((r) => r.name)).toEqual(['job-1', 'web-1', 'web-2', 'web-3'])
    expect(sortRows(rows, POD_COLUMNS, { col: 'restarts', dir: 'desc' }, now)[0]!.name).toBe('web-2')
  })

  it('exports the last termination reason beside the restarts (P2-03)', () => {
    const cols = visibleColumns(POD_COLUMNS, defaultHidden(POD_COLUMNS), false)
    const t = exportTable(rows, cols, now)
    expect(t.head.slice(0, 8)).toEqual(['Pod', 'Namespace', 'Status', 'Ready', 'Restarts', 'Last restart', 'Last termination', 'CPU requested (cores)'])
    expect(t.rows[1]!.slice(2, 7)).toEqual(['CrashLoopBackOff', '1/1', 6, fmtTs('2026-10-13T11:59:00Z'), 'Error, exit 1'])
    expect(t.json[0]).toMatchObject({ Pod: 'web-1', 'CPU in use (cores)': 0.12 })
    expect(t.json[1]).toMatchObject({ 'CPU in use (cores)': null, Node: 'worker-1' })
  })

  it('offers names, namespaces, statuses, nodes and labels to the search', () => {
    expect(podTerms(rows[0]!)).toEqual([
      { kind: 'name', text: 'web-1' },
      { kind: 'namespace', text: 'web' },
      { kind: 'status', text: 'Running' },
      { kind: 'node', text: 'worker-1' },
      { kind: 'label', text: 'app=web' },
    ])
  })
})

import { describe, expect, it } from 'vitest'
import { row } from '../test/rows'
import { applyDelta, applySnapshot, initialState } from './live'
import type { ClusterInfo } from './types'

const cluster: ClusterInfo = {
  name: 'test',
  version: 'v1.37.1',
  mode: 'in-cluster',
  state: 'connected',
  since: '2026-10-12T12:00:00Z',
  synced: true,
  warnings: [],
  metrics: 'available',
}

const snap = applySnapshot(initialState, {
  version: 5,
  at: '2026-10-12T12:00:00Z',
  cluster,
  unscheduled: 1,
  rows: [row('worker-10'), row('worker-2'), row('cp-1')],
})

describe('live state', () => {
  it('takes a snapshot whole, in natural order', () => {
    expect(snap.rows.map((r) => r.name)).toEqual(['cp-1', 'worker-2', 'worker-10'])
    expect(snap.version).toBe(5)
    expect(snap.unscheduled).toBe(1)
    expect(snap.cluster?.name).toBe('test')
  })

  it('applies the next delta', () => {
    const next = applyDelta(snap, {
      version: 6,
      at: '2026-10-12T12:00:05Z',
      upserts: [row('worker-2', { status: 'Cordoned', cordoned: true }), row('worker-3')],
      removes: ['worker-10'],
      unscheduled: 0,
    })
    expect(next).not.toBeNull()
    expect(next!.rows.map((r) => r.name)).toEqual(['cp-1', 'worker-2', 'worker-3'])
    expect(next!.rows[1]!.cordoned).toBe(true)
    expect(next!.unscheduled).toBe(0)
    expect(next!.version).toBe(6)
    expect(next!.cluster).toBe(snap.cluster)
    expect(next!.updatedAt?.toISOString()).toBe('2026-10-12T12:00:05.000Z')
  })

  it('updates the cluster alone', () => {
    const next = applyDelta(snap, { version: 6, at: '2026-10-12T12:00:05Z', cluster: { ...cluster, state: 'disconnected' } })
    expect(next!.cluster?.state).toBe('disconnected')
    expect(next!.rows).toBe(snap.rows)
  })

  it('ignores deltas it already has', () => {
    expect(applyDelta(snap, { version: 5, at: '', upserts: [row('x')] })).toBe(snap)
    expect(applyDelta(snap, { version: 3, at: '' })).toBe(snap)
  })

  it('reports a gap so the stream reopens', () => {
    expect(applyDelta(snap, { version: 7, at: '' })).toBeNull()
  })
})

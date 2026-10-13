import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { row } from '../test/rows'
import { applyDelta, applyRefresh, applySnapshot, initialState, useLive } from './live'
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
  uptime: 'available',
}

const settings = { refreshSeconds: 5, storage: 'memory' as const }

const snap = applySnapshot(initialState, {
  version: 5,
  at: '2026-10-12T12:00:00Z',
  refreshedAt: '2026-10-12T12:00:03Z',
  cluster,
  settings,
  unscheduled: 1,
  rows: [row('worker-10'), row('worker-2'), row('cp-1')],
})

describe('live state', () => {
  it('takes a snapshot whole, in natural order', () => {
    expect(snap.rows.map((r) => r.name)).toEqual(['cp-1', 'worker-2', 'worker-10'])
    expect(snap.version).toBe(5)
    expect(snap.unscheduled).toBe(1)
    expect(snap.cluster?.name).toBe('test')
    expect(snap.settings).toEqual(settings)
    expect(snap.refreshedAt?.toISOString()).toBe('2026-10-12T12:00:03.000Z')
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

  it('takes new settings from a delta', () => {
    const next = applyDelta(snap, { version: 6, at: '2026-10-12T12:00:05Z', settings: { ...settings, refreshSeconds: 30 } })
    expect(next!.settings?.refreshSeconds).toBe(30)
    expect(next!.rows).toBe(snap.rows)
    expect(applyDelta(snap, { version: 6, at: '' })!.settings).toBe(snap.settings)
  })

  it('notes a refresh, and reports a missed delta', () => {
    const next = applyRefresh(snap, { version: 5, at: '2026-10-12T12:00:10Z' })
    expect(next!.refreshedAt?.toISOString()).toBe('2026-10-12T12:00:10.000Z')
    expect(next!.rows).toBe(snap.rows)
    expect(applyRefresh(snap, { version: 6, at: '2026-10-12T12:00:10Z' })).toBeNull()
  })

  it('ignores deltas it already has', () => {
    expect(applyDelta(snap, { version: 5, at: '', upserts: [row('x')] })).toBe(snap)
    expect(applyDelta(snap, { version: 3, at: '' })).toBe(snap)
  })

  it('reports a gap so the stream reopens', () => {
    expect(applyDelta(snap, { version: 7, at: '' })).toBeNull()
  })
})

describe('the event stream', () => {
  class FakeEventSource extends EventTarget {
    static CONNECTING = 0
    static OPEN = 1
    static CLOSED = 2
    static made: FakeEventSource[] = []
    readyState = 0
    url: string
    constructor(url: string) {
      super()
      this.url = url
      FakeEventSource.made.push(this)
    }
    close() {
      this.readyState = 2
    }
    send(type: string, data: unknown) {
      this.dispatchEvent(new MessageEvent(type, { data: JSON.stringify(data) }))
    }
  }

  beforeEach(() => {
    FakeEventSource.made = []
    vi.stubGlobal('EventSource', FakeEventSource)
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('reopens the stream when the server answers with an error status', () => {
    const { result, unmount } = renderHook(() => useLive('/events'))
    const first = FakeEventSource.made[0]!
    act(() => {
      first.readyState = 1
      first.dispatchEvent(new Event('open'))
      first.send('snapshot', { version: 1, at: '2026-10-12T12:00:00Z', refreshedAt: null, cluster, settings, unscheduled: 0, rows: [row('a')] })
    })
    expect(result.current.rows.map((r) => r.name)).toEqual(['a'])
    act(() => first.send('refresh', { version: 1, at: '2026-10-12T12:00:05Z' }))
    expect(result.current.refreshedAt?.toISOString()).toBe('2026-10-12T12:00:05.000Z')

    // A 503 closes an EventSource for good.
    act(() => {
      first.readyState = 2
      first.dispatchEvent(new Event('error'))
    })
    expect(result.current.stream).toBe('reconnecting')
    expect(result.current.rows.map((r) => r.name)).toEqual(['a'])
    act(() => void vi.advanceTimersByTime(4_000))
    expect(FakeEventSource.made.length).toBe(2)
    unmount()
  })

  it('leaves dropped connections to the browser', () => {
    const { unmount } = renderHook(() => useLive('/events'))
    act(() => {
      FakeEventSource.made[0]!.readyState = 0 // reconnecting by itself
      FakeEventSource.made[0]!.dispatchEvent(new Event('error'))
    })
    act(() => void vi.advanceTimersByTime(60_000))
    expect(FakeEventSource.made.length).toBe(1)
    unmount()
  })

  it('reopens at once on a gap in the versions', () => {
    const { unmount } = renderHook(() => useLive('/events'))
    const first = FakeEventSource.made[0]!
    act(() => {
      first.send('snapshot', { version: 1, at: '', refreshedAt: null, cluster, settings, unscheduled: 0, rows: [] })
      first.send('delta', { version: 3, at: '' })
    })
    act(() => void vi.advanceTimersByTime(150))
    expect(FakeEventSource.made.length).toBe(2)
    expect(first.readyState).toBe(2)
    unmount()
  })

  it('reopens when a refresh shows a delta was missed', () => {
    const { unmount } = renderHook(() => useLive('/events'))
    const first = FakeEventSource.made[0]!
    act(() => {
      first.send('snapshot', { version: 1, at: '', refreshedAt: null, cluster, settings, unscheduled: 0, rows: [] })
      first.send('refresh', { version: 2, at: '' })
    })
    act(() => void vi.advanceTimersByTime(150))
    expect(FakeEventSource.made.length).toBe(2)
    unmount()
  })
})

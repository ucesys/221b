import { act, renderHook } from '@testing-library/react'
import { createElement, type ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { row } from '../test/rows'
import { LiveContext, LiveStore, applyMeta, applyRefresh, applyRows, applySnapshot, initialState, useLiveMeta, useView, type LiveState } from './live'
import type { ClusterInfo, NodeRow } from './types'

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
  secrets: 'read',
}

const settings = { refreshSeconds: 5, storage: 'memory' as const }

const snap = applySnapshot(initialState, {
  at: '2026-10-12T12:00:00Z',
  refreshedAt: '2026-10-12T12:00:03Z',
  cluster,
  settings,
  unscheduled: 1,
  counts: { nodes: 3, pods: 12 },
  views: {
    nodes: { version: 5, rows: [row('worker-10'), row('worker-2'), row('cp-1')] },
    pods: { version: 2, rows: [{ key: 'a/p1', name: 'p1' }], usage: { 'a/p1': { cpuMilli: 5, memoryBytes: 1024 } } },
  },
})

function names(s: LiveState, view = 'nodes'): string[] {
  return [...s.views.get(view)!.rows.keys()]
}

describe('live state', () => {
  it('takes a snapshot whole', () => {
    expect(names(snap)).toEqual(['worker-10', 'worker-2', 'cp-1'])
    expect(snap.views.get('nodes')!.version).toBe(5)
    expect(snap.views.get('pods')!.usage.get('a/p1')).toEqual({ cpuMilli: 5, memoryBytes: 1024 })
    expect(snap.unscheduled).toBe(1)
    expect(snap.counts).toEqual({ nodes: 3, pods: 12 })
    expect(snap.cluster?.name).toBe('test')
    expect(snap.settings).toEqual(settings)
    expect(snap.refreshedAt?.toISOString()).toBe('2026-10-12T12:00:03.000Z')
  })

  it('applies the next change of a view', () => {
    const next = applyRows(snap, {
      view: 'nodes',
      version: 6,
      at: '2026-10-12T12:00:05Z',
      upserts: [row('worker-2', { status: 'Cordoned', cordoned: true }), row('worker-3')],
      removes: ['worker-10'],
    })
    expect(next).not.toBeNull()
    expect(names(next!).sort()).toEqual(['cp-1', 'worker-2', 'worker-3'])
    expect((next!.views.get('nodes')!.rows.get('worker-2') as NodeRow).cordoned).toBe(true)
    expect(next!.views.get('nodes')!.version).toBe(6)
    expect(next!.views.get('pods')).toBe(snap.views.get('pods'))
    expect(next!.updatedAt?.toISOString()).toBe('2026-10-12T12:00:05.000Z')
  })

  it('applies usage apart from the rows', () => {
    const next = applyRows(snap, { view: 'pods', version: 3, at: '', usage: { 'a/p1': { cpuMilli: 9, memoryBytes: 1 } }, usageRemoves: [] })!
    expect(next.views.get('pods')!.usage.get('a/p1')).toEqual({ cpuMilli: 9, memoryBytes: 1 })
    expect(next.views.get('pods')!.rows).toBe(snap.views.get('pods')!.rows)
    const gone = applyRows(next, { view: 'pods', version: 4, at: '', usageRemoves: ['a/p1'] })!
    expect(gone.views.get('pods')!.usage.size).toBe(0)
  })

  it('updates the cluster, settings and counts alone', () => {
    const next = applyMeta(snap, { at: '2026-10-12T12:00:05Z', cluster: { ...cluster, state: 'disconnected' }, counts: { nodes: 2 } })
    expect(next.cluster?.state).toBe('disconnected')
    expect(next.counts).toEqual({ nodes: 2 })
    expect(next.views).toBe(snap.views)
    expect(applyMeta(snap, { at: '', settings: { ...settings, refreshSeconds: 2 } }).settings?.refreshSeconds).toBe(2)
  })

  it('notes a refresh, counts the tick, and reports a missed change', () => {
    const ok = applyRefresh(snap, { at: '2026-10-12T12:00:10Z', versions: { nodes: 5, pods: 2 } })
    expect(ok?.refreshedAt?.toISOString()).toBe('2026-10-12T12:00:10.000Z')
    expect(ok?.tick).toBe(1)
    expect(applyRefresh(snap, { at: '', versions: { nodes: 6 } })).toBeNull()
    // A view the browser has not loaded is not checked.
    expect(applyRefresh(snap, { at: '', versions: { services: 9 } })).not.toBeNull()
  })

  it('ignores changes it already has', () => {
    expect(applyRows(snap, { view: 'nodes', version: 5, at: '', upserts: [row('x')] })).toBe(snap)
  })

  it('reports a gap so the stream reopens', () => {
    expect(applyRows(snap, { view: 'nodes', version: 7, at: '' })).toBeNull()
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

  function mount(views: string[]) {
    const store = new LiveStore('/events')
    const wrapper = ({ children }: { children: ReactNode }) => createElement(LiveContext.Provider, { value: store }, children)
    const hook = renderHook(
      () => {
        const meta = useLiveMeta()
        const v = views.map((name) => useView<NodeRow>(name))
        return { meta, v }
      },
      { wrapper },
    )
    const stop = store.start()
    return { store, hook, stop }
  }

  it('subscribes to the views the screens show and reopens on an error status', () => {
    const { hook, stop } = mount(['nodes', 'pods'])
    act(() => void vi.advanceTimersByTime(30))
    const first = FakeEventSource.made[0]!
    expect(first.url).toBe('/events?views=nodes%2Cpods')
    act(() => {
      first.readyState = 1
      first.dispatchEvent(new Event('open'))
      first.send('snapshot', { at: '2026-10-12T12:00:00Z', refreshedAt: null, cluster, settings, unscheduled: 0, views: { nodes: { version: 1, rows: [row('a')] }, pods: { version: 1, rows: [] } } })
    })
    expect(hook.result.current.v[0]!.rows.map((r) => r.name)).toEqual(['a'])
    expect(hook.result.current.v[0]!.loaded).toBe(true)
    act(() => first.send('refresh', { at: '2026-10-12T12:00:05Z', versions: { nodes: 1, pods: 1 } }))
    expect(hook.result.current.meta.refreshedAt?.toISOString()).toBe('2026-10-12T12:00:05.000Z')

    // A 503 closes an EventSource for good.
    act(() => {
      first.readyState = 2
      first.dispatchEvent(new Event('error'))
    })
    expect(hook.result.current.meta.stream).toBe('reconnecting')
    expect(hook.result.current.v[0]!.rows.map((r) => r.name)).toEqual(['a'])
    act(() => void vi.advanceTimersByTime(4_000))
    expect(FakeEventSource.made.length).toBe(2)
    hook.unmount()
    stop()
  })

  it('opens one stream when the page loads, however often the views changed before', () => {
    Object.defineProperty(document, 'readyState', { value: 'loading', configurable: true })
    try {
      const { store, hook, stop } = mount(['nodes'])
      act(() => void vi.advanceTimersByTime(30))
      // Another tab before the load event: the views change again.
      const off = store.want('pods')
      act(() => void vi.advanceTimersByTime(30))
      expect(FakeEventSource.made.length).toBe(0)
      Object.defineProperty(document, 'readyState', { value: 'complete', configurable: true })
      act(() => void window.dispatchEvent(new Event('load')))
      expect(FakeEventSource.made.map((e) => e.url)).toEqual(['/events?views=nodes%2Cpods'])
      off()
      hook.unmount()
      stop()
    } finally {
      Reflect.deleteProperty(document, 'readyState')
    }
  })

  it('leaves dropped connections to the browser', () => {
    const { hook, stop } = mount(['nodes'])
    act(() => void vi.advanceTimersByTime(30))
    act(() => {
      FakeEventSource.made[0]!.readyState = 0 // reconnecting by itself
      FakeEventSource.made[0]!.dispatchEvent(new Event('error'))
    })
    act(() => void vi.advanceTimersByTime(60_000))
    expect(FakeEventSource.made.length).toBe(1)
    hook.unmount()
    stop()
  })

  it('reopens at once on a gap in the versions', () => {
    const { hook, stop } = mount(['nodes'])
    act(() => void vi.advanceTimersByTime(30))
    const first = FakeEventSource.made[0]!
    act(() => {
      first.send('snapshot', { at: '', refreshedAt: null, cluster, settings, unscheduled: 0, views: { nodes: { version: 1, rows: [] } } })
      first.send('rows', { view: 'nodes', version: 3, at: '' })
    })
    act(() => void vi.advanceTimersByTime(150))
    expect(FakeEventSource.made.length).toBe(2)
    expect(first.readyState).toBe(2)
    hook.unmount()
    stop()
  })

  it('reopens when a refresh shows a change was missed', () => {
    const { hook, stop } = mount(['nodes'])
    act(() => void vi.advanceTimersByTime(30))
    const first = FakeEventSource.made[0]!
    act(() => {
      first.send('snapshot', { at: '', refreshedAt: null, cluster, settings, unscheduled: 0, views: { nodes: { version: 1, rows: [] } } })
      first.send('refresh', { at: '', versions: { nodes: 2 } })
    })
    act(() => void vi.advanceTimersByTime(150))
    expect(FakeEventSource.made.length).toBe(2)
    hook.unmount()
    stop()
  })

  it('reopens with the new views when a screen changes what it shows', () => {
    const store = new LiveStore('/events')
    const stop = store.start()
    const release = store.want('nodes')
    act(() => void vi.advanceTimersByTime(30))
    expect(FakeEventSource.made[0]!.url).toBe('/events?views=nodes')
    const releasePods = store.want('pods')
    act(() => void vi.advanceTimersByTime(30))
    expect(FakeEventSource.made.length).toBe(2)
    expect(FakeEventSource.made[1]!.url).toBe('/events?views=nodes%2Cpods')
    release()
    releasePods()
    store.want('services')
    act(() => void vi.advanceTimersByTime(30))
    expect(FakeEventSource.made[2]!.url).toBe('/events?views=services')
    stop()
  })
})

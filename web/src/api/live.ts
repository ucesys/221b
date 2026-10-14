import { createContext, useContext, useEffect, useMemo, useState, useSyncExternalStore } from 'react'
import { unauthorized } from './auth'
import type { ClusterInfo, Counts, MetaEvent, RefreshEvent, RowsEvent, Settings, Snapshot, Usage } from './types'

/** The browser's own connection to the 221B server. */
export type StreamState = 'connecting' | 'open' | 'reconnecting'

/** One view's rows as the browser holds them, keyed by the row's key. */
export interface ViewState<R = unknown> {
  version: number
  rows: Map<string, R>
  usage: Map<string, Usage>
  /** True once a snapshot carried this view. */
  loaded: boolean
}

export interface LiveState {
  views: Map<string, ViewState>
  cluster: ClusterInfo | null
  settings: Settings | null
  unscheduled: number
  counts: Counts
  /** When the server last published a change. */
  updatedAt: Date | null
  /** When the server last refreshed: every browser gets this at the same moment. */
  refreshedAt: Date | null
  /** Counts refresh ticks, so details can be fetched again on each. */
  tick: number
  stream: StreamState
}

export const initialState: LiveState = {
  views: new Map(),
  cluster: null,
  settings: null,
  unscheduled: 0,
  counts: {},
  updatedAt: null,
  refreshedAt: null,
  tick: 0,
  stream: 'connecting',
}

/** The key of a row: nodes are keyed by name, everything else by key. */
export function rowKey(row: unknown): string {
  const r = row as { key?: string; name?: string }
  return r.key ?? r.name ?? ''
}

function emptyView(): ViewState {
  return { version: 0, rows: new Map(), usage: new Map(), loaded: false }
}

/** Replaces the views the snapshot carries; others are kept as they were. */
export function applySnapshot(state: LiveState, snap: Snapshot): LiveState {
  const views = new Map(state.views)
  for (const [name, v] of Object.entries(snap.views)) {
    const rows = new Map<string, unknown>()
    for (const r of v.rows) rows.set(rowKey(r), r)
    const usage = new Map<string, Usage>(Object.entries(v.usage ?? {}))
    views.set(name, { version: v.version, rows, usage, loaded: true })
  }
  return {
    ...state,
    views,
    cluster: snap.cluster,
    settings: snap.settings ?? null,
    unscheduled: snap.unscheduled,
    counts: snap.counts ?? state.counts,
    updatedAt: snap.at ? new Date(snap.at) : null,
    refreshedAt: snap.refreshedAt ? new Date(snap.refreshedAt) : null,
  }
}

/**
 * Applies a change to a view. Changes the view already has are ignored; a
 * gap returns null, which means the stream must be reopened for a new
 * snapshot.
 */
export function applyRows(state: LiveState, e: RowsEvent): LiveState | null {
  const v = state.views.get(e.view) ?? emptyView()
  if (e.version <= v.version) return state
  if (e.version !== v.version + 1) return null
  let rows = v.rows
  if (e.upserts?.length || e.removes?.length) {
    rows = new Map(v.rows)
    for (const k of e.removes ?? []) rows.delete(k)
    for (const r of e.upserts ?? []) rows.set(rowKey(r), r)
  }
  let usage = v.usage
  if (e.usage || e.usageRemoves?.length) {
    usage = new Map(v.usage)
    for (const k of e.usageRemoves ?? []) usage.delete(k)
    for (const [k, u] of Object.entries(e.usage ?? {})) usage.set(k, u)
  }
  const views = new Map(state.views)
  views.set(e.view, { version: e.version, rows, usage, loaded: true })
  return { ...state, views, updatedAt: e.at ? new Date(e.at) : state.updatedAt }
}

export function applyMeta(state: LiveState, m: MetaEvent): LiveState {
  return {
    ...state,
    cluster: m.cluster ?? state.cluster,
    settings: m.settings ?? state.settings,
    unscheduled: m.unscheduled ?? state.unscheduled,
    counts: m.counts ?? state.counts,
    updatedAt: m.at ? new Date(m.at) : state.updatedAt,
  }
}

/**
 * Notes a refresh. The server sends the versions of the views subscribed,
 * so a browser that missed a change finds out at the next refresh: null
 * means reopen.
 */
export function applyRefresh(state: LiveState, r: RefreshEvent): LiveState | null {
  for (const [name, version] of Object.entries(r.versions)) {
    const v = state.views.get(name)
    if (v && v.loaded && v.version !== version) return null
  }
  return { ...state, refreshedAt: r.at ? new Date(r.at) : state.refreshedAt, tick: state.tick + 1 }
}

type Listener = () => void

/**
 * Follows the server's event stream for the views the open screens show.
 * One stream serves every screen: when the set of views changes the stream
 * is reopened with the new set, and the views no screen shows are dropped.
 */
export class LiveStore {
  state: LiveState = initialState
  private listeners = new Set<Listener>()
  private wanted = new Map<string, number>()
  private es: EventSource | null = null
  private open = false
  private failures = 0
  private reopenTimer: ReturnType<typeof setTimeout> | undefined
  private views: string[] = []

  constructor(readonly url = '/api/v1/events') {}

  subscribe = (fn: Listener): (() => void) => {
    this.listeners.add(fn)
    return () => this.listeners.delete(fn)
  }

  getState = (): LiveState => this.state

  private commit(next: LiveState) {
    this.state = next
    for (const fn of this.listeners) fn()
  }

  /** Starts the stream once the screens have said what they show; returns a function that stops it. */
  start(): () => void {
    this.open = true
    this.schedule()
    return () => {
      this.open = false
      clearTimeout(this.pending)
      clearTimeout(this.reopenTimer)
      this.es?.close()
      this.es = null
    }
  }

  /** A screen says it shows a view; the returned function says it stopped. */
  want(view: string): () => void {
    this.wanted.set(view, (this.wanted.get(view) ?? 0) + 1)
    this.schedule()
    return () => {
      const n = (this.wanted.get(view) ?? 1) - 1
      if (n <= 0) this.wanted.delete(view)
      else this.wanted.set(view, n)
      this.schedule()
    }
  }

  /** The views the stream carries now. */
  current(): string[] {
    return this.views
  }

  private wantedViews(): string[] {
    return [...this.wanted.keys()].sort()
  }

  private pending: ReturnType<typeof setTimeout> | undefined

  /** Opens the stream, or reopens it, once the screens have settled on their views. */
  private schedule() {
    clearTimeout(this.pending)
    this.pending = setTimeout(() => {
      if (!this.open) return
      const next = this.wantedViews()
      if (this.es && next.join(',') === this.views.join(',')) return
      // Views no screen shows are dropped; the rest keep their rows until
      // the new snapshot replaces them.
      const views = new Map(this.state.views)
      for (const name of views.keys()) if (!next.includes(name)) views.delete(name)
      this.commit({ ...this.state, views })
      this.failures = 0
      this.reopen(0)
    }, 20)
  }

  private connect() {
    if (!this.open) return
    // A stream opened while the page is still loading keeps some browsers
    // (Firefox) from ever firing the load event; wait for it first.
    if (typeof document !== 'undefined' && document.readyState !== 'complete') {
      window.addEventListener('load', () => this.connect(), { once: true })
      return
    }
    this.views = this.wantedViews()
    const url = this.views.length ? `${this.url}?views=${encodeURIComponent(this.views.join(','))}` : this.url
    const es = new EventSource(url)
    this.es = es
    const current = () => this.state
    es.addEventListener('open', () => {
      this.failures = 0
      this.commit({ ...current(), stream: 'open' })
    })
    es.addEventListener('error', (e) => {
      if (es !== this.es) return
      this.commit({ ...current(), stream: current().updatedAt ? 'reconnecting' : 'connecting' })
      // EventSource retries a dropped connection by itself (the server asks
      // for 3 seconds), but gives up for good on an HTTP error such as a
      // 503 from a busy server or a 502 from a proxy: reopen it then,
      // waiting longer after each failure, up to 30 seconds.
      if ((e.target as EventSource).readyState === EventSource.CLOSED) {
        this.failures++
        // EventSource does not say why: when the session has ended, the
        // server answers 401 to anything, and sign-in is the way back.
        void fetch('/api/v1/me', { headers: { Accept: 'application/json' } }).then((r) => unauthorized(r), () => false)
        this.reopen(Math.min(30_000, 1000 * 2 ** Math.min(this.failures, 5)) * (0.75 + Math.random() / 2))
      }
    })
    es.addEventListener('snapshot', (e) => {
      const snap = JSON.parse((e as MessageEvent<string>).data) as Snapshot
      this.commit({ ...applySnapshot(current(), snap), stream: 'open' })
    })
    es.addEventListener('rows', (e) => {
      const next = applyRows(current(), JSON.parse((e as MessageEvent<string>).data) as RowsEvent)
      if (next === null) this.reopen(100)
      else if (next !== current()) this.commit(next)
    })
    es.addEventListener('meta', (e) => {
      this.commit(applyMeta(current(), JSON.parse((e as MessageEvent<string>).data) as MetaEvent))
    })
    es.addEventListener('refresh', (e) => {
      const next = applyRefresh(current(), JSON.parse((e as MessageEvent<string>).data) as RefreshEvent)
      if (next === null) this.reopen(100)
      else this.commit(next)
    })
  }

  private reopen(delay: number) {
    this.es?.close()
    this.es = null
    clearTimeout(this.reopenTimer)
    this.reopenTimer = setTimeout(() => this.connect(), delay)
  }
}

export const LiveContext = createContext<LiveStore | null>(null)

function useStore(): LiveStore {
  const store = useContext(LiveContext)
  if (!store) throw new Error('LiveContext is missing')
  return store
}

/** The cluster details, settings and stream state. */
export function useLiveMeta(): LiveState {
  const store = useStore()
  return useSyncExternalStore(store.subscribe, store.getState, store.getState)
}

const EMPTY_VIEW: ViewState = { version: 0, rows: new Map(), usage: new Map(), loaded: false }

/**
 * The rows of a view, kept current while the component is mounted. The
 * array is rebuilt only when the view's version changes.
 */
export function useView<R>(name: string | null): { rows: R[]; usage: Map<string, Usage>; version: number; loaded: boolean } {
  const store = useStore()
  useEffect(() => (name ? store.want(name) : undefined), [store, name])
  const view = useSyncExternalStore(
    store.subscribe,
    () => (name ? ((store.getState().views.get(name) as ViewState<R> | undefined) ?? (EMPTY_VIEW as ViewState<R>)) : (EMPTY_VIEW as ViewState<R>)),
    () => EMPTY_VIEW as ViewState<R>,
  )
  const rows = useMemo(() => [...view.rows.values()], [view.rows])
  return { rows, usage: view.usage, version: view.version, loaded: view.loaded }
}

/** Increments on every refresh tick; details screens fetch again on it. */
export function useRefreshTick(): number {
  const store = useStore()
  return useSyncExternalStore(store.subscribe, () => store.getState().tick, () => 0)
}

/** A store per stream URL: a new one when another cluster is opened; tests pass their own. */
export function useLiveStore(url?: string): LiveStore {
  const [store, setStore] = useState(() => new LiveStore(url))
  if (store.url !== (url ?? '/api/v1/events')) setStore(new LiveStore(url))
  useEffect(() => store.start(), [store])
  return store
}

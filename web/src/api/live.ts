import { useEffect, useState } from 'react'
import type { ClusterInfo, Delta, NodeRow, Refresh, Settings, Snapshot } from './types'
import { compareNatural } from '../lib/format'

/** The browser's own connection to the 221B server. */
export type StreamState = 'connecting' | 'open' | 'reconnecting'

export interface LiveState {
  rows: NodeRow[]
  cluster: ClusterInfo | null
  settings: Settings | null
  unscheduled: number
  version: number
  /** When the server last published a change. */
  updatedAt: Date | null
  /** When the server last refreshed: every browser gets this at the same moment. */
  refreshedAt: Date | null
  stream: StreamState
}

export const initialState: LiveState = {
  rows: [],
  cluster: null,
  settings: null,
  unscheduled: 0,
  version: 0,
  updatedAt: null,
  refreshedAt: null,
  stream: 'connecting',
}

function sortRows(rows: Iterable<NodeRow>): NodeRow[] {
  return Array.from(rows).sort((a, b) => compareNatural(a.name, b.name))
}

/** Replaces everything with a snapshot. */
export function applySnapshot(state: LiveState, snap: Snapshot): LiveState {
  return {
    ...state,
    rows: sortRows(snap.rows),
    cluster: snap.cluster,
    settings: snap.settings ?? null,
    unscheduled: snap.unscheduled,
    version: snap.version,
    updatedAt: snap.at ? new Date(snap.at) : null,
    refreshedAt: snap.refreshedAt ? new Date(snap.refreshedAt) : null,
  }
}

/**
 * Applies a delta. Deltas the state already has are ignored; a gap returns
 * null, which means the stream must be reopened to get a new snapshot.
 */
export function applyDelta(state: LiveState, d: Delta): LiveState | null {
  if (d.version <= state.version) return state
  if (d.version !== state.version + 1) return null
  let rows = state.rows
  if (d.upserts?.length || d.removes?.length) {
    const byName = new Map(state.rows.map((r) => [r.name, r]))
    for (const name of d.removes ?? []) byName.delete(name)
    for (const r of d.upserts ?? []) byName.set(r.name, r)
    rows = sortRows(byName.values())
  }
  return {
    ...state,
    rows,
    cluster: d.cluster ?? state.cluster,
    settings: d.settings ?? state.settings,
    unscheduled: d.unscheduled ?? state.unscheduled,
    version: d.version,
    updatedAt: d.at ? new Date(d.at) : state.updatedAt,
  }
}

/**
 * Notes a refresh. The server sends its version with it, so a browser that
 * missed a delta finds out at the next refresh: null means reopen.
 */
export function applyRefresh(state: LiveState, r: Refresh): LiveState | null {
  if (r.version !== state.version) return null
  return { ...state, refreshedAt: r.at ? new Date(r.at) : state.refreshedAt }
}

/** Follows the server's event stream and keeps the rows current. */
export function useLive(url = '/api/v1/events'): LiveState {
  const [state, setState] = useState<LiveState>(initialState)

  useEffect(() => {
    let es: EventSource | null = null
    let closed = false
    let failures = 0
    let reopenTimer: ReturnType<typeof setTimeout> | undefined
    // The stream's own copy of the state, so a gap is seen at once rather
    // than inside a deferred React update.
    let current = initialState
    const commit = (next: LiveState) => {
      current = next
      setState(next)
    }

    const open = () => {
      if (closed) return
      es = new EventSource(url)
      es.addEventListener('open', () => {
        failures = 0
        commit({ ...current, stream: 'open' })
      })
      es.addEventListener('error', (e) => {
        commit({ ...current, stream: current.version > 0 ? 'reconnecting' : 'connecting' })
        // EventSource retries a dropped connection by itself (the server
        // asks for 3 seconds), but gives up for good on an HTTP error such
        // as a 503 from a busy server or a 502 from a proxy: reopen it then,
        // waiting longer after each failure, up to 30 seconds.
        if ((e.target as EventSource).readyState === EventSource.CLOSED) {
          failures++
          reopen(Math.min(30_000, 1000 * 2 ** Math.min(failures, 5)) * (0.75 + Math.random() / 2))
        }
      })
      es.addEventListener('snapshot', (e) => {
        const snap = JSON.parse((e as MessageEvent<string>).data) as Snapshot
        commit({ ...applySnapshot(current, snap), stream: 'open' })
      })
      es.addEventListener('delta', (e) => {
        const d = JSON.parse((e as MessageEvent<string>).data) as Delta
        const next = applyDelta(current, d)
        if (next === null) reopen(100)
        else if (next !== current) commit(next)
      })
      es.addEventListener('refresh', (e) => {
        const r = JSON.parse((e as MessageEvent<string>).data) as Refresh
        const next = applyRefresh(current, r)
        if (next === null) reopen(100)
        else commit(next)
      })
    }
    const reopen = (delay: number) => {
      es?.close()
      clearTimeout(reopenTimer)
      reopenTimer = setTimeout(open, delay)
    }
    open()
    return () => {
      closed = true
      clearTimeout(reopenTimer)
      es?.close()
    }
  }, [url])

  return state
}

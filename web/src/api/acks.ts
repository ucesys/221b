import { useMemo, useSyncExternalStore } from 'react'
import { unauthorized } from './auth'
import { useView } from './live'
import type { AlertRow } from './types'
import { apiPath, useRoute } from '../lib/router'

type Ack = NonNullable<AlertRow['ack']>

// What this browser asked for and the server's rows do not show yet
// (asked after 0.7.1): an acknowledgement shows the moment it is clicked.
// The rows follow within a refresh on the replica that saved it, and up
// to a few seconds later from another, which reads the settings again
// every 3 seconds. An entry stands while the rows disagree with it, for
// at most WAIT.
const WAIT = 15_000
const asked = new Map<string, Ack | null>()
const listeners = new Set<() => void>()
let version = 0

function note(cluster: string, key: string, ack: Ack | null | undefined) {
  const k = cluster + '\n' + key
  if (ack === undefined) asked.delete(k)
  else {
    asked.set(k, ack)
    setTimeout(() => {
      if (asked.get(k) === ack) note(cluster, key, undefined)
    }, WAIT)
  }
  version++
  for (const fn of listeners) fn()
}

function subscribe(fn: () => void) {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

/** The alerts with what this browser just acknowledged, or took back, shown at once. */
export function withAsked(rows: AlertRow[], cluster: string, pending: Map<string, Ack | null> = asked): AlertRow[] {
  if (!pending.size) return rows
  return rows.map((r) => {
    const k = cluster + '\n' + r.key
    if (!pending.has(k)) return r
    const want = pending.get(k)!
    return !!r.ack === !!want ? r : { ...r, ack: want ?? undefined }
  })
}

/** The Alerts view, with this browser's acknowledgements shown before the server's rows have them. */
export function useAlerts(): { rows: AlertRow[]; loaded: boolean } {
  const view = useView<AlertRow>('alerts')
  const cluster = useRoute().cluster
  const v = useSyncExternalStore(subscribe, () => version)
  const rows = useMemo(() => withAsked(view.rows, cluster), [view.rows, cluster, v]) // eslint-disable-line react-hooks/exhaustive-deps
  return { rows, loaded: view.loaded }
}

/**
 * Acknowledges an alert for everyone, or takes that back. The server keeps
 * it with the settings; every open console sees the change, this one at
 * once. Rejects with the server's message, and the alert shows as before.
 */
export async function acknowledge(cluster: string, key: string, acknowledge: boolean): Promise<void> {
  note(cluster, key, acknowledge ? { by: 'you', at: new Date().toISOString() } : null)
  try {
    const ack = await send(cluster, key, acknowledge)
    if (acknowledge && ack) note(cluster, key, ack)
  } catch (e) {
    note(cluster, key, undefined)
    throw e
  }
}

async function send(cluster: string, key: string, acknowledge: boolean): Promise<Ack | undefined> {
  let res: Response
  try {
    res = await fetch(apiPath('alerts/ack', cluster), {
      method: 'POST',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: JSON.stringify({ key, acknowledge }),
    })
  } catch {
    throw new Error('The server could not be reached.')
  }
  if (res.ok) {
    try {
      return ((await res.json()) as { ack?: Ack }).ack
    } catch {
      return undefined
    }
  }
  if (unauthorized(res)) throw new Error('Your session has ended; sign in again.')
  let msg = `The server answered ${res.status}.`
  try {
    const body = (await res.json()) as { error?: string }
    if (body.error) msg = body.error
  } catch {
    // Not JSON: keep the status.
  }
  throw new Error(msg)
}

// The home page's data (asked at the Phase 4 review): every cluster at a
// glance, from GET /api/v1/home, filtered by what the person may see.

import { useEffect, useState } from 'react'
import { unauthorized } from './auth'
import type { ClusterSummary, Env } from './clusters'

export interface HomeUse {
  /** CPU in millicores, memory in bytes. */
  allocatable: number
  requested: number
  /** What metrics-server reports, over usedNodes nodes. */
  used: number
  usedNodes: number
}

export interface HomeCluster {
  id: string
  name: string
  /** Whether Sherlock can answer about it now, for the person asking. */
  sherlock?: boolean
  env: Env
  description?: string
  local?: boolean
  state: ClusterSummary['state']
  message?: string
  version?: string
  expiring?: boolean
  expired?: boolean
  synced: boolean
  metrics?: 'available' | 'unavailable' | 'unknown' | string
  refreshedAt?: string
  nodes: { total: number; ready: number; controlPlane: number; cordoned: number; errors: number; warnings: number }
  cpu: HomeUse
  memory: HomeUse
  /**
   * ready, unready, pending, failing and completed add up to total; running
   * is every pod on a node that has not finished; unknown pods, on a node
   * that stopped reporting, are among the unready.
   */
  pods: { total: number; running: number; capacity: number; notReady: number; failing: number; ready: number; unready: number; pending: number; completed: number; unknown: number }
  namespaces: number
  services: number
  /** The Storage tab's claims, how many are bound, and its rows under Needs attention, errors the red ones. */
  storage: { claims: number; bound: number; attention: number; errors: number }
  /** nodesDown are nodes not ready; nodeErrors the ready ones with an error, so that no node counts twice. */
  alerts: { failed: number; unknown: number; pending: number; nodesDown: number; nodeErrors: number; nodeWarnings: number; volumes: number; acknowledged: number; recovered: number }
}

export async function fetchHome(): Promise<HomeCluster[]> {
  const res = await fetch('/api/v1/home', { headers: { Accept: 'application/json' } })
  if (unauthorized(res)) throw new Error('Your session has ended; sign in again.')
  if (!res.ok) throw new Error(`The console answered ${res.status}.`)
  const body = (await res.json()) as { clusters?: HomeCluster[] }
  return body.clusters ?? []
}

/** Every cluster at a glance, asked again every few seconds while the page is open. */
export function useHome(every = 5_000): { clusters: HomeCluster[]; loaded: boolean; error: string } {
  const [state, setState] = useState<{ clusters: HomeCluster[]; loaded: boolean; error: string }>({ clusters: [], loaded: false, error: '' })
  useEffect(() => {
    let stop = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const load = async () => {
      try {
        const clusters = await fetchHome()
        if (!stop) setState({ clusters, loaded: true, error: '' })
      } catch (e) {
        if (!stop) setState((s) => ({ ...s, loaded: true, error: e instanceof Error ? e.message : String(e) }))
      }
      if (!stop) timer = setTimeout(() => void load(), every)
    }
    void load()
    return () => {
      stop = true
      clearTimeout(timer)
    }
  }, [every])
  return state
}

import { unauthorized } from './auth'
import type { Fleet } from './types'
import { apiPath } from '../lib/router'

/** What a kubelet is configured to keep from pods, as it says now. */
export interface KubeletConfig {
  systemReserved?: Record<string, string>
  kubeReserved?: Record<string, string>
  reservedSystemCPUs?: string
  evictionHard?: Record<string, string>
  enforceNodeAllocatable?: string[]
  maxPods?: number
}

/** A node's kubelet, or why it could not be read. */
export interface KubeletView {
  config?: KubeletConfig
  note?: string
}

export async function fetchKubelet(cluster: string, node: string): Promise<KubeletView> {
  const res = await fetch(apiPath(`nodes/${encodeURIComponent(node)}/kubelet`, cluster))
  if (!res.ok) throw new Error(`HTTP ${res.status}`)
  return (await res.json()) as KubeletView
}

/**
 * Replaces a cluster's fleets on the server (P4-01), which keeps them with
 * the console's settings; every open console sees the change. Rejects with
 * the server's message.
 */
export async function saveFleets(cluster: string, fleets: Fleet[]): Promise<Fleet[]> {
  let res: Response
  try {
    res = await fetch(apiPath('fleets', cluster), {
      method: 'PUT',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: JSON.stringify({ fleets }),
    })
  } catch {
    throw new Error('The server could not be reached.')
  }
  if (!res.ok) {
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
  return ((await res.json()) as { fleets: Fleet[] }).fleets
}

import { unauthorized } from './auth'
import type { Fleet } from './types'
import { apiPath } from '../lib/router'

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

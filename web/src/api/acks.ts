import { unauthorized } from './auth'
import { apiPath } from '../lib/router'

/**
 * Acknowledges an alert for everyone, or takes that back. The server keeps
 * it with the settings; every open console sees the change. Rejects with
 * the server's message.
 */
export async function acknowledge(cluster: string, key: string, acknowledge: boolean): Promise<void> {
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
  if (res.ok) return
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

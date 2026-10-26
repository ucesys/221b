import { unauthorized } from './auth'
import type { TimeFormat } from '../lib/format'
import type { Settings } from './types'

/** The refresh rates the Settings page offers, in seconds. */
export const REFRESH_CHOICES = [2, 3, 5, 10, 30, 60, 300] as const

/** How long a pod may wait, in seconds, before it is an alert: the choices and the default. */
export const GRACE_CHOICES = [0, 60, 120, 300, 600] as const
export const DEFAULT_GRACE = 120

/** "5 s", "1 min", "5 min". */
export function refreshLabel(seconds: number): string {
  if (seconds % 60 === 0) return `${seconds / 60} min`
  return `${seconds} s`
}

/**
 * Saves the shared settings on the server, which applies them to every
 * open console at once. Rejects with the server's message.
 */
export async function saveSettings(patch: { refreshSeconds: number; timeFormat?: TimeFormat; pendingGraceSeconds?: number }, url = '/api/v1/settings'): Promise<Settings> {
  let res: Response
  try {
    res = await fetch(url, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(patch),
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
  return (await res.json()) as Settings
}

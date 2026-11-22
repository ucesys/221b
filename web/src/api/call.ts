// The JSON calls Automation's pages make, and the facts' (api/facts.ts), which
// stays out of api/automation.ts for what imports it without a bundler.
import { BACKGROUND, unauthorized } from './auth'

/** A call the server refused: its words, its status and the rest of its answer (a 409 carries the record as it is now). */
export class CallError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly body?: Record<string, unknown>,
    /** The answer says the console's database is unavailable (DB-15): what was asked for is not known, which is not the same as there being none. */
    readonly databaseAway = false,
  ) {
    super(message)
    this.name = 'CallError'
  }
}

/**
 * How an answer says the database is unavailable (DB-15): a header the
 * server sets on every such 503, whatever the words are. A part that is
 * off answers 503 too, without it.
 */
export const DATABASE_HEADER = 'X-221b-Database'
export const saysDatabaseAway = (res: Response) => res.headers?.get?.(DATABASE_HEADER) === 'unavailable'

const awayListeners = new Set<() => void>()
/** Calls fn whenever an answer says the database is unavailable, so that what shows its state reads it again at once; returns how to stop. */
export function onDatabaseAway(fn: () => void): () => void {
  awayListeners.add(fn)
  return () => {
    awayListeners.delete(fn)
  }
}

export async function call<T>(url: string, init: RequestInit = {}, background = false): Promise<T> {
  let res: Response
  const headers: Record<string, string> = {
    Accept: 'application/json',
    ...(background ? BACKGROUND : {}),
  }
  if (init.body !== undefined) headers['Content-Type'] = 'application/json'
  try {
    res = await fetch(url, { ...init, headers })
  } catch {
    throw new Error('The server could not be reached.')
  }
  if (!res.ok) {
    if (unauthorized(res)) throw new Error('Your session has ended; sign in again.')
    let msg = `The server answered ${res.status}.`
    let body: Record<string, unknown> | undefined
    try {
      body = (await res.json()) as Record<string, unknown>
      if (typeof body.error === 'string' && body.error) msg = body.error
    } catch {
      // Not JSON: keep the status.
    }
    const away = saysDatabaseAway(res)
    if (away) for (const fn of awayListeners) fn()
    throw new CallError(msg, res.status, body, away)
  }
  return (await res.json()) as T
}

export const post = (body: unknown): RequestInit => ({
  method: 'POST',
  body: JSON.stringify(body),
})

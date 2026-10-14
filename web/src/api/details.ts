import { useEffect, useState } from 'react'
import { unauthorized } from './auth'
import { useRefreshTick } from './live'

export interface Details<T> {
  data: T | null
  /** The server's error for a request that failed; "" otherwise. */
  error: string
  /** 404: the object is gone (or was never there). */
  gone: boolean
  /** 403: Kubernetes does not let this person see it (P3-02). */
  forbidden: boolean
  loading: boolean
}

/** Reads an error body: the server's {"error": ...}, or the status. */
export async function errorText(r: Response): Promise<string> {
  if (unauthorized(r)) return 'Your session has ended; sign in again.'
  try {
    const body = (await r.json()) as { error?: string }
    if (body.error) return body.error
  } catch {
    // not JSON
  }
  return `The server answered ${r.status}.`
}

/**
 * Fetches a details endpoint and fetches it again on every refresh tick,
 * so a drawer follows the object as the table does. The last good data is
 * kept while a later read fails, so a deleted pod's details stay readable
 * under the notice that it is gone.
 */
export function useDetails<T>(url: string | null): Details<T> {
  const tick = useRefreshTick()
  const [state, setState] = useState<Details<T>>({ data: null, error: '', gone: false, forbidden: false, loading: !!url })
  const [last, setLast] = useState(url)
  if (url !== last) {
    // Another object: start over rather than show the previous one.
    setLast(url)
    setState({ data: null, error: '', gone: false, forbidden: false, loading: !!url })
  }
  useEffect(() => {
    if (!url) return
    let stale = false
    fetch(url, { headers: { Accept: 'application/json' } })
      .then(async (r) => {
        if (stale) return
        if (!r.ok) {
          if (unauthorized(r)) return
          const msg = await errorText(r)
          setState((s) => ({ data: s.data, error: msg, gone: r.status === 404, forbidden: r.status === 403, loading: false }))
          return
        }
        const data = (await r.json()) as T
        if (!stale) setState({ data, error: '', gone: false, forbidden: false, loading: false })
      })
      .catch(() => {
        if (!stale) setState((s) => ({ ...s, error: 'Could not reach the server.', loading: false }))
      })
    return () => {
      stale = true
    }
  }, [url, tick])
  return state
}

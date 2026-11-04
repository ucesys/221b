// Who is signed in (P3-01, P3-03), and what to do when the session is gone:
// the server answers 401, and the browser goes to the sign-in page, which
// brings the person back to the same page afterwards.

import { useEffect, useState } from 'react'
import type { Me } from './types'

/**
 * The header of a request the page makes by itself, on a timer or a refresh
 * tick: the server checks the session and does not count it as activity, so
 * the idle limit ends a session left open in a tab (P3-01). Everything not
 * marked counts.
 */
export const BACKGROUND = { 'X-221B-Background': '1' } as const

/** The headers of a JSON read, marked when the page makes it by itself. */
export const jsonHeaders = (background = false): Record<string, string> => (background ? { Accept: 'application/json', ...BACKGROUND } : { Accept: 'application/json' })

/** Sends the browser to the sign-in page, to come back here afterwards. */
export function toSignIn(): void {
  const here = window.location.pathname + window.location.search + window.location.hash
  window.location.assign('/signin?next=' + encodeURIComponent(here))
}

/** True, after sending the browser to sign in, when the response says the session is gone. */
export function unauthorized(res: Response): boolean {
  if (res.status !== 401) return false
  toSignIn()
  return true
}

/** After a stream failed for a reason EventSource does not say: when the session has ended, the server answers 401, and sign-in is the way back. */
export function checkSession(): void {
  void fetch('/api/v1/me', { headers: jsonHeaders(true) }).then(unauthorized, () => false)
}

/** Reads who is signed in; null while unknown or when the server could not be reached. */
export async function fetchMe(): Promise<Me | null> {
  try {
    const res = await fetch('/api/v1/me', { headers: { Accept: 'application/json' } })
    if (unauthorized(res) || !res.ok) return null
    return (await res.json()) as Me
  } catch {
    return null
  }
}

/** How often real use is told to the server, at most. */
export const ACTIVE_EVERY = 3 * 60_000

let activeAt = 0

/** Tells the server the person is here, with a request not marked as the page's own. */
function active(): void {
  const now = Date.now()
  if (now - activeAt < ACTIVE_EVERY) return
  activeAt = now
  void fetch('/api/v1/me', { headers: { Accept: 'application/json' } }).then(unauthorized, () => false)
}

/**
 * Counts the person's pointer and keys as activity while the page is open,
 * so someone reading and clicking keeps their session. Heard on the way
 * down, as a terminal keeps its keys to itself. A browser reports a still
 * pointer as moved when the page changes under it: that is not the person.
 */
export function useActivity(): void {
  useEffect(() => {
    const moved = (e: PointerEvent) => {
      if (e.movementX || e.movementY) active()
    }
    const on = { capture: true, passive: true }
    window.addEventListener('pointermove', moved, on)
    for (const name of ['pointerdown', 'keydown', 'wheel']) window.addEventListener(name, active, on)
    return () => {
      window.removeEventListener('pointermove', moved, on)
      for (const name of ['pointerdown', 'keydown', 'wheel']) window.removeEventListener(name, active, on)
    }
  }, [])
}

/** Ends the session and goes to the sign-in page. */
export async function signOut(): Promise<void> {
  try {
    await fetch('/signout', { method: 'POST', headers: { Accept: 'application/json' } })
  } finally {
    window.location.assign('/signin')
  }
}

/** Who is signed in, fetched once per page load. */
export function useMe(): Me | null {
  const [me, setMe] = useState<Me | null>(null)
  useEffect(() => {
    let gone = false
    void fetchMe().then((m) => {
      if (!gone) setMe(m)
    })
    return () => {
      gone = true
    }
  }, [])
  return me
}

// Who is signed in (P3-01, P3-03), and what to do when the session is gone:
// the server answers 401, and the browser goes to the sign-in page, which
// brings the person back to the same page afterwards.

import { useEffect, useState } from 'react'
import type { Me } from './types'

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

// Whether the console's database is away (DB-15), for the banner on every
// page and for the pages that must not take "could not be read" for
// "nothing there". Read as the Inbox's lists are (useShared): once for
// everything that shows it, again every half minute, every ten seconds
// while it is away so that the banner goes soon after it is back, and at
// once when any answer says the database is unavailable.
import { useEffect } from 'react'
import { onDatabaseAway } from '../api/call'
import { fetchDatabase, type DatabaseState } from '../api/database'
import { reloadShared, useShared } from './cases'

export const DATABASE = 'console.database'
/** How often the state is read again while the database is away. */
export const AWAY_EVERY = 10_000
/** An answer that says the database is away has the state read again, at most this often. */
const NOTICE_EVERY = 5_000

/** Whether this state says the database is away: one the console has, and cannot reach. */
export const isAway = (d: DatabaseState | null): boolean => !!d && d.configured && !d.available

/** The database's state as last read; null before the first answer, and for a console that could not say. */
export function useDatabase(): DatabaseState | null {
  return useShared<DatabaseState>(DATABASE, fetchDatabase).value
}

/**
 * Keeps the state fresh, for whatever shows it; mounted once, by the
 * banner. While the database is away it is asked every ten seconds; an
 * answer anywhere in the app that says the database is unavailable has it
 * asked at once, unless it already says so.
 */
export function useDatabaseWatch(): DatabaseState | null {
  const d = useDatabase()
  const away = isAway(d)
  useEffect(() => {
    if (!away) return
    const t = setInterval(() => reloadShared(DATABASE), AWAY_EVERY)
    return () => clearInterval(t)
  }, [away])
  useEffect(() => {
    if (away) return
    let last = 0
    return onDatabaseAway(() => {
      const now = Date.now()
      if (now - last < NOTICE_EVERY) return
      last = now
      reloadShared(DATABASE)
    })
  }, [away])
  return d
}

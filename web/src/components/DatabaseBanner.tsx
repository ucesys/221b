// The banner every page has while the console's database is away
// (DB-15): a console that loses its database goes on running, and each
// page that needs it would otherwise fail on its own.
import type { DatabaseState } from '../api/database'
import { fmtTs } from '../lib/format'
import { isAway, useDatabaseWatch } from '../lib/database'
import { AlertIcon } from './Icons'

export const DATABASE_BANNER_TITLE = 'The database is unavailable'
/** What still works, and what does not until it is back. */
export const DATABASE_BANNER_DETAIL =
  'Sign-in and the pages read live from the clusters still work. What the console keeps in the database cannot be read or changed until it is back: the Inbox, cases and reports, jobs and their runs, Mycroft, Automation, applications, credentials, the audit log and the settings.'

/** The banner's words for a state, or null when the database is there (or the console has none). */
export function databaseBanner(d: DatabaseState | null): { title: string; detail: string } | null {
  if (!isAway(d)) return null
  const since = d?.since ? ` Unavailable since ${fmtTs(d.since)}.` : ''
  // Why, in the console's words; the server gives it to a SuperAdmin only.
  const why = d?.error ? ` Why: ${d.error.replace(/\.$/, '')}.` : ''
  return { title: DATABASE_BANNER_TITLE, detail: DATABASE_BANNER_DETAIL + since + why + ' This banner goes when the database is back.' }
}

/**
 * Under the top bar on every page, as the banners about a cluster are
 * (Banners.tsx): a polite status, so a screen reader says it once when it
 * appears, with nothing to focus and nothing to dismiss; it goes by
 * itself when the database answers again.
 */
export function DatabaseBanner() {
  const b = databaseBanner(useDatabaseWatch())
  if (!b) return null
  return (
    <div role="status" aria-live="polite" style={{ flex: 'none', display: 'flex', flexDirection: 'column' }}>
      <div className="rk-banner" data-tone="err" data-testid="database-banner" style={{ alignItems: 'flex-start' }}>
        <AlertIcon />
        <strong className="rk-banner-title">{b.title}</strong>
        {/* The whole sentence, on as many lines as it takes: it says what to expect of every page. */}
        <span className="rk-banner-detail" style={{ whiteSpace: 'normal' }}>
          {b.detail}
        </span>
      </div>
    </div>
  )
}

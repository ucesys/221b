// The ConfigMap wiki store's room (Phase 12.1, KN-37). The temporary store
// (CF-57) keeps every page, excerpt and staged change in one ConfigMap key
// of at most 900 KiB, below a ConfigMap's 1 MiB: a few hundred pages of a
// few KiB each, fewer with the sources' excerpts. Once full, Mycroft's new
// pages and changes are refused whole. The Wiki says so from STORE_NEAR on,
// and points to the git and S3 stores, which have no such limit.
import type { JSX } from 'react'
import { adminHref } from '../../lib/router'

/** From this share of the room on, the Wiki warns. */
export const STORE_NEAR = 0.8

/** From this share on, the store is as good as full. */
export const STORE_FULL = 0.97

const kib = (n: number) => `${Math.ceil(n / 1024)} KiB`

/** The warning that the ConfigMap store is nearly or quite full, with where to move the wiki. */
export function StoreRoom({ usage, maySetup }: { usage: { used: number; limit: number }; maySetup: boolean }): JSX.Element {
  const share = usage.limit > 0 ? usage.used / usage.limit : 1
  const full = share >= STORE_FULL
  return (
    <div className="rk-callout" data-tone={full ? 'err' : 'warn'} role={full ? 'alert' : undefined} style={{ fontSize: 12.5, flexDirection: 'column', gap: 6 }} data-testid="wiki-store-room">
      <strong>
        {full ? 'The wiki’s ConfigMap store is full' : 'The wiki’s ConfigMap store is nearly full'}: {kib(usage.used)} of {kib(usage.limit)} used ({Math.min(100, Math.round(share * 100))}%).
      </strong>
      <span>
        The ConfigMap store is temporary, for trying Mycroft: one ConfigMap holds every page, the sources’ excerpts and the changes waiting for review, a few hundred pages at most.{' '}
        {full ? 'Mycroft’s new pages and changes are refused until it is moved.' : 'Once it is full, Mycroft’s new pages and changes are refused.'} A git repository or an S3 bucket has no such limit, and keeps every page’s history.
      </span>
      {maySetup ? (
        <span>
          <a className="rk-btn" data-on href={adminHref('settings', ['knowledge'])} data-testid="wiki-store-move">
            Move the wiki to Git or S3
          </a>
        </span>
      ) : (
        <span className="rk-fhint">A SuperAdmin moves it to Git or S3 in Administration › Settings › Knowledge.</span>
      )}
    </div>
  )
}

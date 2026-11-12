// "Known problems right now" (IS-27): the open known issues and planned
// maintenance, each to follow instead of reporting, and the status page
// when the settings name one.
import { useState } from 'react'
import { fmtTs } from '../lib/format'
import { knownPill, type Known } from './api'
import { Pill } from './Pill'

export interface KnownPanelProps {
  known: Known[] | null
  statusPage: string
  onFollow: (id: string, on: boolean) => Promise<void>
}

/** A planned maintenance's window: "Sun 02:00–04:00". */
export function windowText(w: { start: string; end: string }): string {
  return `${fmtTs(w.start)} – ${fmtTs(w.end)}`
}

export function KnownPanel({ known, statusPage, onFollow }: KnownPanelProps) {
  const [busy, setBusy] = useState('')
  const follow = async (k: Known) => {
    setBusy(k.id)
    try {
      await onFollow(k.id, !k.following)
    } finally {
      setBusy('')
    }
  }
  return (
    <aside className="rk-card rp-card rp-side" aria-labelledby="rp-now">
      <h2 id="rp-now" className="rp-h2 rp-h2-small">
        Known problems right now
      </h2>
      <span className="rk-dim rp-small">Check here first: if yours is listed, you can follow it instead of reporting.</span>
      {known === null && <span className="rk-dim rp-small">Loading…</span>}
      {known?.length === 0 && <span className="rk-dim rp-small">None just now.</span>}
      {known && known.length > 0 && (
        <ul className="rp-knownlist" data-testid="known-list">
          {known.map((k) => (
            <li key={k.id}>
              <strong>{k.kind === 'planned' ? 'Planned: ' + k.title : k.title}</strong>
              <span>
                {k.kind === 'planned' && k.window ? <Pill text={windowText(k.window)} tone="muted" /> : <Pill {...knownPill(k)} />} <span className="rp-when">updated {fmtTs(k.updated)}</span>
              </span>
              {k.workaround && <span className="rp-small rk-dim">{k.workaround}</span>}
              <span className="rp-actions">
                <button type="button" className="rk-link rp-small" disabled={busy === k.id} aria-pressed={k.following} onClick={() => void follow(k)}>
                  {k.following ? 'Following · stop' : 'Follow'}
                </button>
                {k.link && (
                  <a className="rp-small" href={k.link} target="_blank" rel="noreferrer">
                    Details
                  </a>
                )}
              </span>
            </li>
          ))}
        </ul>
      )}
      {statusPage && (
        <a className="rp-small" href={statusPage} target="_blank" rel="noreferrer">
          All known problems, on Confluence
        </a>
      )}
    </aside>
  )
}

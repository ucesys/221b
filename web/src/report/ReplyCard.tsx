// What a reporter is told once a report is in (IS-10, IS-11): a known
// issue it matched, with its status, what to do, the expected fix and the
// last update, as the operators wrote them; or the number it was received
// as. "No, mine is something else" sends it to investigation instead.
import { useState } from 'react'
import { fmtClock, fmtTs } from '../lib/format'
import { knownPill, type Receipt } from './api'
import { Pill } from './Pill'

export interface ReplyCardProps {
  receipt: Receipt
  /** Unlinks the report from the known issue; resolves once done. */
  onUnlink: (n: number) => Promise<void>
}

export function ReplyCard({ receipt, onUnlink }: ReplyCardProps) {
  const { report, known, removed } = receipt
  const [unlinked, setUnlinked] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const matched = known && !unlinked
  const unlink = async () => {
    setBusy(true)
    setError('')
    try {
      await onUnlink(report.number)
      setUnlinked(true)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  const scrubbed = removed && (
    <span className="rp-note" data-testid="reply-removed">
      We removed something that looked like a password or a token before keeping your case.
    </span>
  )
  if (!matched) {
    return (
      <section className="rk-card rp-card rp-reply" aria-labelledby="rp-got" data-testid="reply-card">
        <h2 id="rp-got" className="rp-h2">
          Received as case <strong>C-{report.number}</strong>
        </h2>
        <span className="rk-dim">
          {unlinked ? 'Thanks. We will look into it as a problem of its own and tell you here when we know more.' : 'We will look into it and tell you here when we know more.'}
        </span>
        {scrubbed}
      </section>
    )
  }
  const pill = knownPill(known)
  const last = known.lastUpdate
  return (
    <section className="rk-card rp-card rp-reply" data-matched="true" aria-labelledby="rp-got" data-testid="reply-card">
      <span className="rp-meta">
        Received as case <strong>C-{report.number}</strong> · {fmtClock(report.created)}
      </span>
      <h2 id="rp-got" className="rp-h2">
        This looks like a problem we already know about
      </h2>
      <div className="rp-known">
        <div className="rp-line">
          <strong>{known.title}</strong>
          <Pill {...pill} />
        </div>
        <dl className="rp-kv">
          {known.workaround && (
            <>
              <dt>What you can do</dt>
              <dd>{known.workaround}</dd>
            </>
          )}
          {known.expected && (
            <>
              <dt>Expected fix</dt>
              <dd>{known.expected}</dd>
            </>
          )}
          {last && (
            <>
              <dt>Last update</dt>
              <dd>
                {fmtTs(last.at)}: {last.text}
              </dd>
            </>
          )}
        </dl>
      </div>
      <span className="rk-dim">We added your report to it and will tell you here when it changes.</span>
      {scrubbed}
      <div className="rp-actions">
        <button type="button" className="rk-btn" disabled={busy} onClick={() => void unlink()}>
          No, mine is something else
        </button>
        {known.link && (
          <a className="rk-btn" href={known.link} target="_blank" rel="noreferrer">
            Read the full page
          </a>
        )}
      </div>
      {error && (
        <span className="rk-t" data-tone="err" role="alert">
          {error}
        </span>
      )}
    </section>
  )
}

// "Your cases" (IS-27; a report is a case since Phase 12's wave 5): each
// with its status, the explanation a closed one was closed with, "Still
// broken" on a closed one (with what still happens), and its
// conversation, where the reporter answers a question.
import { useState } from 'react'
import { fmtTs } from '../lib/format'
import { priorityText, statusPill, type Report } from './api'
import { Pill } from './Pill'

export interface MyReportsProps {
  reports: Report[] | null
  error?: string
  onReopen: (n: number, reason: string) => Promise<void>
  onMessage: (n: number, text: string) => Promise<void>
}

/** The first line of a report, cut to fit a row. */
export function firstLine(text: string | undefined, max = 90): string {
  const line = (text ?? '').split('\n')[0]?.trim() ?? ''
  return line.length > max ? line.slice(0, max - 1).trimEnd() + '…' : line
}

export function MyReports({ reports, error, onReopen, onMessage }: MyReportsProps) {
  const [open, setOpen] = useState<number | null>(null)
  return (
    <section className="rk-card rp-card" aria-labelledby="rp-mine">
      <h2 id="rp-mine" className="rp-h2">
        Your cases
      </h2>
      {error && (
        <span className="rk-t" data-tone="err" role="alert">
          {error}
        </span>
      )}
      {reports === null && !error && <span className="rk-dim">Loading…</span>}
      {reports?.length === 0 && <span className="rk-dim">Nothing yet. What you report shows here, with how it stands.</span>}
      {reports && reports.length > 0 && (
        <ul className="rp-rows" data-testid="my-reports">
          {reports.map((r) => (
            <ReportRow key={r.number} report={r} open={open === r.number} onToggle={() => setOpen(open === r.number ? null : r.number)} onReopen={onReopen} onMessage={onMessage} />
          ))}
        </ul>
      )}
    </section>
  )
}

interface RowProps {
  report: Report
  open: boolean
  onToggle: () => void
  onReopen: (n: number, reason: string) => Promise<void>
  onMessage: (n: number, text: string) => Promise<void>
}

function ReportRow({ report: r, open, onToggle, onReopen, onMessage }: RowProps) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [reply, setReply] = useState('')
  const [again, setAgain] = useState<string | null>(null)
  const act = async (fn: () => Promise<void>) => {
    setBusy(true)
    setError('')
    try {
      await fn()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  const pill = statusPill(r)
  const summary = r.pruned ? 'Kept without its text after the retention' : firstLine(r.text)
  const id = `rp-report-${r.number}`
  return (
    <li className="rp-row" data-testid={`report-${r.number}`}>
      <div className="rp-line">
        <span className="rk-mono rp-num">C-{r.number}</span>
        <button type="button" className="rk-link rp-summary" aria-expanded={open} aria-controls={id} onClick={onToggle}>
          {summary || `Case C-${r.number}`}
        </button>
        <Pill {...pill} />
        {r.status === 'closed' && r.reason ? (
          <button type="button" className="rk-link" onClick={onToggle}>
            Why
          </button>
        ) : (
          <span className="rp-when">{fmtTs(r.updated)}</span>
        )}
      </div>
      {r.waiting && r.status !== 'closed' && (
        <span className="rk-t rp-ask" data-tone="warn">
          We asked you something about this case: answer below.
        </span>
      )}
      {open && (
        <div id={id} className="rp-detail">
          {r.status === 'closed' && r.reason && (
            <p className="rp-reason">
              <strong>Closed:</strong> {r.reason}
            </p>
          )}
          {r.text && <p className="rp-text">{r.text}</p>}
          <span className="rp-note">{priorityText(r.priority)}</span>
          {r.scrubbed && <span className="rp-note">Something that looked like a password or a token was removed from it.</span>}
          {r.known && (
            <p className="rk-dim">
              Linked to <strong>{r.known.title}</strong>
              {r.known.expected ? `, expected fix ${r.known.expected}` : ''}.
            </p>
          )}
          {r.plain && <p className="rp-plain">{r.plain}</p>}
          {(r.messages ?? []).length > 0 && (
            <ol className="rp-messages" aria-label="Conversation">
              {(r.messages ?? []).map((m) => (
                <li key={m.id} data-kind={m.kind}>
                  <span className="rp-meta">
                    {m.kind === 'system' ? 'Update' : m.from.name || m.from.id} · {fmtTs(m.at)}
                  </span>
                  <span className="rp-text">{m.text}</span>
                </li>
              ))}
            </ol>
          )}
          {r.status !== 'closed' && (
            <form
              className="rp-replyform"
              onSubmit={(e) => {
                e.preventDefault()
                const text = reply.trim()
                if (text) void act(() => onMessage(r.number, text).then(() => setReply('')))
              }}
            >
              <label className="rk-sr" htmlFor={`${id}-reply`}>
                Add to case C-{r.number}
              </label>
              <textarea id={`${id}-reply`} className="rp-say rp-say-small" value={reply} placeholder="Add to this case" onChange={(e) => setReply(e.target.value)} />
              <button type="submit" className="rk-btn" disabled={busy || !reply.trim()}>
                Add
              </button>
            </form>
          )}
          {r.status === 'closed' && r.canReopen && again === null && (
            <button type="button" className="rk-btn" disabled={busy} onClick={() => setAgain('')}>
              Still broken
            </button>
          )}
          {r.status === 'closed' && r.canReopen && again !== null && (
            <form
              className="rp-replyform"
              onSubmit={(e) => {
                e.preventDefault()
                const text = again.trim()
                if (text) void act(() => onReopen(r.number, text).then(() => setAgain(null)))
              }}
            >
              <label className="rk-sr" htmlFor={`${id}-again`}>
                What still happens
              </label>
              <textarea id={`${id}-again`} className="rp-say rp-say-small" value={again} placeholder="What still happens? We open the case again with it." onChange={(e) => setAgain(e.target.value)} />
              <button type="submit" className="rk-btn" disabled={busy || !again.trim()}>
                Open again
              </button>
              <button type="button" className="rk-btn" disabled={busy} onClick={() => setAgain(null)}>
                Cancel
              </button>
            </form>
          )}
        </div>
      )}
      {error && (
        <span className="rk-t" data-tone="err" role="alert">
          {error}
        </span>
      )}
    </li>
  )
}

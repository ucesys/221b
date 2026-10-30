import { useEffect, useState } from 'react'
import { fetchAudit, type AuditEvent, type AuditPage } from '../api/admin'
import { fmtTs } from '../lib/format'
import type { AdminProps } from './AdminPage'
import { DownloadIcon } from './Icons'

/** Who an event names, shortest first; the title has the rest. */
function whoText(e: AuditEvent): { text: string; title: string } {
  const parts = [e.who.name, e.who.email, e.who.subject].filter((x): x is string => !!x)
  return { text: parts[0] ?? '—', title: parts.join(' · ') }
}

/** Saves text as a file the browser downloads. */
export function download(name: string, text: string, type = 'application/json'): void {
  const url = URL.createObjectURL(new Blob([text], { type }))
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

/**
 * Administration › Audit log (asked October 30, 2026): the newest events
 * the console kept, who did what and when, searched by words and narrowed
 * by action and outcome. The full record stays on standard output and
 * the webhook; the console keeps the newest two thousand beside its
 * settings.
 */
export function AdminAudit(_: AdminProps) {
  const [q, setQ] = useState('')
  const [action, setAction] = useState('')
  const [outcome, setOutcome] = useState('')
  const [page, setPage] = useState<AuditPage | null>(null)
  const [err, setErr] = useState('')
  const [tick, setTick] = useState(0)
  useEffect(() => {
    let on = true
    const t = setTimeout(
      () => {
        fetchAudit({ q, action, outcome, limit: 500 }).then(
          (p) => {
            if (!on) return
            setPage(p)
            setErr('')
          },
          (e: unknown) => on && setErr(e instanceof Error ? e.message : String(e)),
        )
      },
      q ? 250 : 0,
    )
    return () => {
      on = false
      clearTimeout(t)
    }
  }, [q, action, outcome, tick])
  const events = page?.events ?? []
  return (
    <section className="rk-card" style={{ padding: '16px 18px', display: 'flex', flexDirection: 'column', gap: 14 }} data-testid="admin-audit">
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 12 }}>
        <h2 style={{ margin: 0, fontSize: 15, fontWeight: 600 }}>Audit log</h2>
        <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>Who did what, and when: sign-ins, settings, connections, users, applications, Sherlock, Watson and the Toolbox.</span>
        <span className="rk-field" style={{ marginLeft: 'auto', width: 260 }}>
          <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Find a person, target or detail" aria-label="Find an event" data-testid="audit-search" />
        </span>
        <label className="rk-field" style={{ width: 200 }}>
          <select value={action} onChange={(e) => setAction(e.target.value)} aria-label="Action" data-testid="audit-action">
            <option value="">every action</option>
            {(page?.actions ?? []).map((a) => (
              <option key={a} value={a}>
                {a}
              </option>
            ))}
          </select>
        </label>
        <label className="rk-field" style={{ width: 130 }}>
          <select value={outcome} onChange={(e) => setOutcome(e.target.value)} aria-label="Outcome" data-testid="audit-outcome">
            <option value="">any outcome</option>
            <option value="ok">ok</option>
            <option value="failed">failed</option>
            <option value="denied">denied</option>
            <option value="stopped">stopped</option>
          </select>
        </label>
        <button className="rk-btn" onClick={() => setTick((n) => n + 1)} title="Read the log again" data-testid="audit-refresh">
          Refresh
        </button>
        <button className="rk-btn" onClick={() => download(`221b-audit-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-')}.json`, JSON.stringify(events, null, 2))} disabled={events.length === 0} title="The events shown, as JSON" data-testid="audit-download">
          <DownloadIcon />
          JSON
        </button>
      </div>
      {err && <p className="tb-err">{err}</p>}
      {page && !page.stored && (
        <p className="rk-callout" data-tone="info" style={{ margin: 0, fontSize: 12.5 }}>
          This console keeps the events in memory, until it restarts: the settings ConfigMap, where a console in a cluster keeps them, is not in use here.
        </p>
      )}
      {page && page.dropped > 0 && (
        <p className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }}>
          {page.dropped} events never reached the audit webhook: its queue was full.
        </p>
      )}
      {page === null ? (
        <p className="tb-hint">Loading…</p>
      ) : events.length === 0 ? (
        <p className="tb-hint" style={{ margin: 0 }}>
          {page.total === 0 ? 'Nothing has been recorded yet.' : 'No event matches.'}
        </p>
      ) : (
        <table className="tb-table" data-testid="audit-table">
          <thead>
            <tr>
              <th>When</th>
              <th>Action</th>
              <th>Who</th>
              <th>Cluster</th>
              <th>Target</th>
              <th>Outcome</th>
              <th>Detail</th>
              <th>From</th>
            </tr>
          </thead>
          <tbody>
            {events.map((e, i) => {
              const who = whoText(e)
              return (
                <tr key={i} data-action={e.action} data-outcome={e.outcome}>
                  <td style={{ whiteSpace: 'nowrap' }}>{fmtTs(e.time, { seconds: true })}</td>
                  <td className="rk-mono">{e.action}</td>
                  <td title={who.title}>{who.text}</td>
                  <td>{e.cluster || '—'}</td>
                  <td className="rk-mono" style={{ overflowWrap: 'anywhere' }}>
                    {e.target || '—'}
                  </td>
                  <td>
                    <span className="rk-pill" data-tone={e.outcome === 'ok' ? 'ok' : e.outcome === 'stopped' ? 'info' : 'err'}>
                      {e.outcome}
                    </span>
                  </td>
                  <td style={{ overflowWrap: 'anywhere', maxWidth: 480 }}>{e.detail || '—'}</td>
                  <td className="rk-mono">{e.from || '—'}</td>
                </tr>
              )
            })}
          </tbody>
        </table>
      )}
      {page && (
        <p className="tb-hint" style={{ margin: 0 }}>
          {events.length} of {page.total} kept events shown{events.length === 500 ? ' (the newest 500; narrow the search for older ones)' : ''}. The console keeps the newest 2,000 events beside its settings; the full log is on its standard output and the audit webhook, when one is configured.
        </p>
      )}
    </section>
  )
}

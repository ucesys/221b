import { useEffect, useMemo, useState } from 'react'
import { fetchAudit, filterAudit, type AuditEvent, type AuditPage } from '../api/admin'
import { fmtTs } from '../lib/format'
import { csvText, exportFileName, type ExportFormat } from '../lib/table'
import { download, xlsxBytes, type Cell } from '../lib/xlsx'
import type { AdminProps } from './AdminPage'
import { ExportMenu } from './ExportMenu'

/** Who an event names, shortest first; the title has the rest. */
function whoText(e: AuditEvent): { text: string; title: string } {
  const parts = [e.who.name, e.who.email, e.who.subject].filter((x): x is string => !!x)
  return { text: parts[0] ?? '—', title: parts.join(' · ') }
}

/** What a restored event says of where it came from. */
export function restoredText(taken: string): string {
  return `Restored from the backup of ${fmtTs(taken)}`
}

const EXPORT_HEAD = ['When', 'Action', 'Who', 'Cluster', 'Target', 'Outcome', 'Detail', 'From', 'Restored']

/** The export's rows: each event as the table shows it, the person in full. */
function exportRows(events: AuditEvent[]): Cell[][] {
  return events.map((e) => [e.time, e.action, whoText(e).title, e.cluster ?? '', e.target ?? '', e.outcome, e.detail ?? '', e.from ?? '', e.restored ?? ''])
}

/**
 * Administration › Audit log (asked October 30, 2026): the newest events
 * the console kept, who did what and when, searched by words and narrowed
 * by action and outcome. The full record stays on standard output and
 * the webhook; the console keeps the newest two thousand beside its
 * settings.
 */
export function AdminAudit({ onToast }: AdminProps) {
  const [q, setQ] = useState('')
  const [action, setAction] = useState('')
  const [outcome, setOutcome] = useState('')
  const [page, setPage] = useState<AuditPage | null>(null)
  const [err, setErr] = useState('')
  const [tick, setTick] = useState(0)
  // Read once and on Refresh; the search and the filters work on what came.
  useEffect(() => {
    let on = true
    fetchAudit().then(
      (p) => {
        if (!on) return
        setPage(p)
        setErr('')
      },
      (e: unknown) => on && setErr(e instanceof Error ? e.message : String(e)),
    )
    return () => {
      on = false
    }
  }, [tick])
  const events = useMemo(() => filterAudit(page?.events ?? [], { q, action, outcome }).slice(0, 500), [page, q, action, outcome])
  const exportAs = (format: ExportFormat) => {
    const file = exportFileName('221b', 'audit-log', format)
    try {
      if (format === 'xlsx') download(xlsxBytes('Audit log', EXPORT_HEAD, exportRows(events)), file)
      else if (format === 'csv') download(csvText(EXPORT_HEAD, exportRows(events)), file, 'text/csv;charset=utf-8')
      else download(JSON.stringify(events, null, 2) + '\n', file, 'application/json')
      onToast(`Downloaded ${file}`)
    } catch {
      onToast('Couldn’t build the file.')
    }
  }
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
        <ExportMenu onExport={exportAs} disabled={events.length === 0} what="the events shown" />
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
                  <td style={{ overflowWrap: 'anywhere', maxWidth: 480 }}>
                    {e.detail || '—'}
                    {e.restored && (
                      <div className="rk-fhint" data-testid="audit-restored">
                        {restoredText(e.restored)}
                      </div>
                    )}
                  </td>
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

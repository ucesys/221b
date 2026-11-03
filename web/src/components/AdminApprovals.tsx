import { useEffect, useState } from 'react'
import { fetchApprovals, fetchScript, type ApprovalRow, type Approvals, type Script } from '../api/automation'
import { markSeen } from '../lib/approvalsSeen'
import { fmtTs } from '../lib/format'
import { clusterHref, FIRST_TAB, href } from '../lib/router'
import type { AdminProps } from './AdminPage'
import { VersionPane } from './AutomationTab'
import { Drawer } from './Drawer'

const errText = (e: unknown) => (e instanceof Error ? e.message : String(e))

const KIND = { script: 'Script version', change: 'Variable change', command: 'Watson command' } as const
const STATE = {
  pending: { text: 'Pending', tone: 'warn' },
  approved: { text: 'Approved', tone: 'ok' },
  rejected: { text: 'Rejected', tone: 'err' },
  revoked: { text: 'Revoked', tone: 'muted' },
} as const

/** A decided script version, read here without leaving Approvals. */
function DecidedVersion({ row, onClose }: { row: ApprovalRow; onClose: () => void }) {
  const [script, setScript] = useState<Script | null>(null)
  const [err, setErr] = useState('')
  const [against, setAgainst] = useState<number | null>(null)
  useEffect(() => {
    let gone = false
    fetchScript(row.script ?? '').then(
      (s) => !gone && setScript(s),
      (e: unknown) => !gone && setErr(errText(e)),
    )
    return () => {
      gone = true
    }
  }, [row.script])
  const v = script?.versions.find((x) => x.n === row.version)
  return (
    <Drawer
      label="Script version"
      kicker="Approvals"
      title={row.what}
      facts={
        <span style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center', fontSize: 12.5 }}>
          <span className="rk-tag" data-tone={STATE[row.state].tone}>
            {STATE[row.state].text}
          </span>
          {row.approvedBy && `by ${row.approvedBy}${row.approvedAt ? `, ${fmtTs(row.approvedAt)}` : ''}`}
          {row.comment && <span style={{ color: 'var(--fg3)' }}>“{row.comment}”</span>}
        </span>
      }
      onClose={onClose}
      width={1100}
      testId="approval-version"
    >
      {err && <p className="tb-err">{err}</p>}
      {script && !v && <p className="tb-hint">v{row.version} is no longer kept: newer versions replaced it.</p>}
      {script && v && <VersionPane script={script} v={v} against={against} onAgainst={setAgainst} onReviewed={setScript} />}
      {!script && !err && <p className="tb-hint">Loading…</p>}
    </Drawer>
  )
}

/** Where a row is read: the version, the fleet's variables, or Watson's card while it waits. */
function linkOf(r: ApprovalRow): string | undefined {
  if (r.kind === 'script') return href('scripts', [], { script: r.script, version: String(r.version) })
  if (r.kind === 'change') return clusterHref(r.cluster ?? 'local', 'variables', [], { fleet: r.fleet })
  return r.state === 'pending' ? clusterHref(r.cluster ?? 'local', FIRST_TAB, [], { watson: 'open' }) : undefined
}

/**
 * Administration › Approvals: what waits for this person on top, then what
 * was approved, rejected or revoked, newest first. Nothing is approved
 * here: Review opens a waiting row where it can be read first, and Approve
 * is there; a decided script version opens here, over the list.
 */
export function AdminApprovals(_: AdminProps) {
  const [list, setList] = useState<Approvals | null>(null)
  const [err, setErr] = useState('')
  const [open, setOpen] = useState<ApprovalRow | null>(null)
  useEffect(() => {
    let gone = false
    fetchApprovals().then(
      (a) => {
        if (gone) return
        setList(a)
        markSeen(a.items)
      },
      (e: unknown) => !gone && setErr(errText(e)),
    )
    return () => {
      gone = true
    }
  }, [])
  const waiting = list?.items.filter((r) => r.state === 'pending').length ?? 0
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }} data-testid="admin-approvals">
      <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>
        What waits for you to approve, then what was decided. Review opens it, to read before you decide. What you wrote or asked for yourself is left out where it takes a second person.
      </p>
      {err && <p className="tb-err">{err}</p>}
      {list && waiting === 0 && (
        <p className="tb-hint" data-testid="approvals-none">
          Nothing is waiting for your approval.
        </p>
      )}
      {list && (
        <section className="rk-card" style={{ padding: '12px 16px', overflowX: 'auto' }} aria-label="Approvals" data-testid="approvals-table">
          <table className="tb-table">
            <thead>
              <tr>
                <th>Kind</th>
                <th>What</th>
                <th>Cluster</th>
                <th>Status</th>
                <th>Requested by</th>
                <th>Requested on</th>
                <th>Approved by</th>
                <th>Approved on</th>
                <th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {list.items.length === 0 && (
                <tr>
                  <td colSpan={9} style={{ color: 'var(--fg3)' }}>
                    Nothing has been asked for or approved yet.
                  </td>
                </tr>
              )}
              {list.items.map((r) => {
                const link = linkOf(r)
                return (
                  <tr key={`${r.kind}-${r.cluster ?? ''}-${r.id ?? r.what}`} data-testid={`approval-${r.kind}`} data-state={r.state}>
                    <td>{KIND[r.kind]}</td>
                    <td className={r.kind === 'change' ? undefined : 'rk-mono'}>
                      {r.what}
                      {(r.mine || r.own) && (
                        <span className="rk-pill" data-tone="info" style={{ marginLeft: 6, fontFamily: 'var(--sans, inherit)' }} title="You asked for it" data-testid="approval-mine">
                          yours
                        </span>
                      )}
                    </td>
                    <td>{r.kind === 'script' ? '—' : (r.clusterName ?? r.cluster)}</td>
                    <td>
                      <span className="rk-tag" data-tone={STATE[r.state].tone}>
                        {STATE[r.state].text}
                      </span>
                      {r.comment && (
                        <div style={{ color: 'var(--fg3)', fontSize: 12, marginTop: 2 }} data-testid="approval-comment">
                          {r.comment}
                        </div>
                      )}
                    </td>
                    <td>{r.requestedBy || '—'}</td>
                    <td>{r.requestedAt ? fmtTs(r.requestedAt) : '—'}</td>
                    <td>{r.approvedBy || '—'}</td>
                    <td>{r.approvedAt ? fmtTs(r.approvedAt) : '—'}</td>
                    <td style={{ textAlign: 'right' }}>
                      {r.kind === 'script' && r.state !== 'pending' ? (
                        <button className="rk-btn" onClick={() => setOpen(r)} data-testid="approval-script-open">
                          Open
                        </button>
                      ) : link && (
                        <a className="rk-btn" href={link} data-testid={`approval-${r.kind}-review`}>
                          {r.state === 'pending' ? 'Review' : 'Open'}
                        </a>
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </section>
      )}
      {open && <DecidedVersion row={open} onClose={() => setOpen(null)} />}
    </div>
  )
}

import { useEffect, useState } from 'react'
import { decideRequest, fetchApprovals, fetchScript, type ApprovalRow, type Approvals, type Script } from '../api/automation'
import { markSeen } from '../lib/approvalsSeen'
import { fmtTs } from '../lib/format'
import { clusterHref, FIRST_TAB, href } from '../lib/router'
import type { AdminProps } from './AdminPage'
import { VersionPane } from './AutomationTab'
import { DecideDialog } from './automation/DecideDialog'
import { Drawer } from './Drawer'

const errText = (e: unknown) => (e instanceof Error ? e.message : String(e))

const KIND = { script: 'Script version', change: 'Variable change', command: 'Watson command', admin: 'Console change' } as const
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

/**
 * A console change that waits for a second SuperAdmin: taking a cluster off
 * production, or making someone a SuperAdmin. It is decided here, as it has
 * no page of its own; whoever asked may only withdraw it.
 */
function RequestActions({ row, onDecided }: { row: ApprovalRow; onDecided: () => void }) {
  const [how, setHow] = useState<'approve' | 'reject' | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  if (row.state !== 'pending') return null
  const decide = async (comment: string) => {
    setBusy(true)
    setErr('')
    try {
      await decideRequest(row.id ?? '', how === 'approve', comment)
      setHow(null)
      onDecided()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <span style={{ display: 'inline-flex', gap: 6 }}>
      {!row.own && (
        <button className="rk-btn" data-size="sm" data-on onClick={() => setHow('approve')} data-testid="approval-admin-approve">
          Approve…
        </button>
      )}
      <button className="rk-btn" data-size="sm" onClick={() => setHow('reject')} data-testid="approval-admin-reject">
        {row.own ? 'Withdraw…' : 'Reject…'}
      </button>
      {how && (
        <DecideDialog how={how} what={row.what} busy={busy} error={err} onCancel={() => setHow(null)} onConfirm={(c) => void decide(c)}>
          {how === 'approve'
            ? `${row.requestedBy} asked for this. Approving makes the change now.`
            : row.own
              ? 'You asked for this. Withdrawing leaves things as they are.'
              : `${row.requestedBy} reads your reason. Rejecting leaves things as they are.`}
        </DecideDialog>
      )}
    </span>
  )
}

/** Where a row is read: the version, the fleet's variables, or Watson's card while it waits. */
function linkOf(r: ApprovalRow): string | undefined {
  if (r.kind === 'admin') return undefined
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
  const [tick, setTick] = useState(0)
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
  }, [tick])
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
                    <td className={r.kind === 'change' || r.kind === 'admin' ? undefined : 'rk-mono'}>
                      {r.what}
                      {(r.mine || r.own) && (
                        <span className="rk-pill" data-tone="info" style={{ marginLeft: 6, fontFamily: 'var(--sans, inherit)' }} title="You asked for it" data-testid="approval-mine">
                          yours
                        </span>
                      )}
                    </td>
                    <td>{r.kind === 'script' ? '—' : (r.clusterName ?? r.cluster ?? '—')}</td>
                    <td>
                      <span className="rk-tag" data-tone={STATE[r.state].tone}>
                        {STATE[r.state].text}
                      </span>
                      {r.overtaken && (
                        <div style={{ color: 'var(--fg3)', fontSize: 12, marginTop: 2 }} data-testid="approval-overtaken">
                          {r.overtaken}
                        </div>
                      )}
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
                      {r.kind === 'admin' ? (
                        <RequestActions row={r} onDecided={() => setTick((t) => t + 1)} />
                      ) : r.kind === 'script' && r.state !== 'pending' ? (
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

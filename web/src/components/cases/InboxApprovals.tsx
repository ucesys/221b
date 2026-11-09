// Approvals in Cases › Inbox (CS-06, IN-02): what was Administration ›
// Approvals until Phase 12, now rows of the Inbox's one table, each
// opening in its drawer (InboxDrawers.tsx) with what is here: a decided
// script version read in place, a console change decided on the spot, and
// where every other kind is reviewed. Approving still takes the approval
// permission (a SuperAdmin), as the API checks.
import { useEffect, useState } from 'react'
import { decideRequest, fetchScript, type ApprovalRow, type Script } from '../../api/automation'
import { fmtTs, localTimes } from '../../lib/format'
import { CallError } from '../../api/call'
import { clusterHref, FIRST_TAB, href } from '../../lib/router'
import { VersionPane } from '../AutomationTab'
import { DecideDialog } from '../automation/DecideDialog'
import { Drawer } from '../Drawer'
import { APPROVAL_STATE } from './inboxRows'

const errText = (e: unknown) => (e instanceof Error ? e.message : String(e))


/** A decided script version, read here without leaving the Inbox. */
export function DecidedVersion({ row, onClose }: { row: ApprovalRow; onClose: () => void }) {
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
          <span className="rk-tag" data-tone={APPROVAL_STATE[row.state].tone}>
            {APPROVAL_STATE[row.state].text}
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
export function RequestActions({ row, onDecided, onConflict }: { row: ApprovalRow; onDecided: () => void; onConflict: (msg: string) => void }) {
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
      // Someone decided it first (IB-09): who and when, in the dialog, and
      // the list read again, which shows it decided.
      if (e instanceof CallError && e.status === 409) {
        setHow(null)
        onConflict(localTimes(e.message))
        onDecided()
      } else setErr(errText(e))
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
export function linkOf(r: ApprovalRow): string | undefined {
  if (r.kind === 'admin') return undefined
  if (r.kind === 'script') return href('scripts', [], { script: r.script, version: String(r.version) })
  if (r.kind === 'change') return clusterHref(r.cluster ?? 'local', 'variables', [], { fleet: r.fleet })
  return r.state === 'pending' ? clusterHref(r.cluster ?? 'local', FIRST_TAB, [], { watson: 'open' }) : undefined
}

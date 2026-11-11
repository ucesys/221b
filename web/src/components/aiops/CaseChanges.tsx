// The changes that resolve a case (Phase 12, wave 6b, CF-28 to CF-31):
// every proposed change that names the case, whoever linked it (the agent
// that proposed it, or a person), with its card's state now; Link a
// change, which picks one of the cards on the case's cluster that wait,
// were approved, or were applied in the last 30 days; Unlink for a link a
// person made (an agent's own link stays: reject the card instead); and,
// once a change applied the fix, where its check stands: waiting for the
// next run of the job that found the problem, verified (at P0 and P1 the
// RCA closes it), or not held.
import { useEffect, useState } from 'react'
import { fetchProposals, LINK_APPLIED_DAYS, linkable, stateText, stateTone, type CardState, type Proposal } from '../../api/cards'
import { actorText } from '../../api/actor'
import { linkChange, verifiedText, waitText, type Case, type CaseCard } from '../../api/casework'
import { fmtTs } from '../../lib/format'
import { ConfirmDialog } from '../ConfirmDialog'
import { jobHref } from './jobsRoute'
import { errText, Tag } from './parts'
import { proposalHref, resolvesText } from './ProposalLinks'

const CARD_STATE: Record<string, string> = {
  queued: 'Waiting for approval',
  approved: 'Approved, running',
  applied: 'Applied',
  failed: 'Failed',
  rejected: 'Rejected',
  expired: 'Expired',
  replaced: 'Replaced',
  gone: 'No longer kept',
  unknown: 'Unknown',
}

const cardTone = (s: string) => (s in CARD_STATE && s !== 'gone' && s !== 'unknown' ? stateTone(s as CardState) : 'muted')

/** Who linked a change to the case, in words. */
export const linkedText = (p: Pick<CaseCard, 'linkedBy'>) => (!p.linkedBy || p.linkedBy === 'agent' ? 'named by the agent' : `linked by ${p.linkedBy}`)

/** A change's card, linked to its drawer in the Inbox. */
function ChangeLink({ id, testId }: { id: string; testId?: string }) {
  return (
    <a className="rk-link rk-mono" href={proposalHref(id)} data-testid={testId}>
      {id}
    </a>
  )
}

/** The job whose next run checks the fix, linked; or what it is when the case does not say. */
function VerifyJob({ c }: { c: Case }) {
  if (!c.verifyJob) return <>the job that found it</>
  return (
    <a className="rk-link" href={jobHref(c.verifyJob.id)} data-testid="case-verify-job">
      {c.verifyJob.label || c.verifyJob.id}
    </a>
  )
}

/**
 * Where the check of a fix a change applied stands (CF-31): "Fix applied
 * by change N — waiting for the next run of <job> to check", "Verified —
 * add the RCA to close" (with Fix… for who may), or "The fix did not
 * hold". Nothing for a case without one. Since the review (I-1) it says
 * what a verification rested on, and how long a fix has waited.
 */
export function Verification({ c, busy, onFix }: { c: Case; busy: boolean; onFix: () => void }) {
  if (!c.verification) return null
  const change = c.fixApplied?.change
  const by = change ? (
    <>
      change <ChangeLink id={change} testId="case-verification-change" />
    </>
  ) : (
    'a change'
  )
  if (c.verification === 'waiting')
    return (
      <div className="rk-callout" data-tone="info" data-testid="case-verification" data-state="waiting" style={{ flexDirection: 'column', gap: 4 }}>
        <span>
          <strong>Fix applied by {by}</strong> — waiting for a validation run: the next run of <VerifyJob c={c} /> re-checks it.
        </span>
        {c.verifyWait && (
          <span style={{ fontSize: 12.5 }} data-testid="case-verify-wait">
            {waitText(c.verifyWait, fmtTs)}
          </span>
        )}
        <span className="tb-hint" style={{ fontSize: 12 }}>
          {c.priority <= 1 ? 'If that run re-checks it and finds the problem gone, the fix is verified; then add the RCA to close the case.' : 'If that run re-checks it and finds the problem gone, the case closes as fixed by itself.'}
        </span>
      </div>
    )
  if (c.verification === 'verified')
    return (
      <div className="rk-callout" data-tone="ok" data-testid="case-verification" data-state="verified" style={{ alignItems: 'center', flexWrap: 'wrap' }}>
        <span style={{ flex: '1 1 300px' }}>
          <strong>Verified — add the RCA to close.</strong> A run of <VerifyJob c={c} /> re-checked it after {by} and found the problem gone. At P{c.priority} a person closes it with its root-cause analysis.
          {c.verifiedBy && (
            <span style={{ display: 'block', fontSize: 12.5, marginTop: 4 }} data-testid="case-verified-by">
              {verifiedText(c.verifiedBy, fmtTs)}
            </span>
          )}
        </span>
        {c.mayChange && (c.allowedOutcomes ?? []).includes('fix') && (
          <button className="rk-btn" data-on data-size="sm" disabled={busy} onClick={onFix} data-testid="case-verification-fix">
            Fix…
          </button>
        )}
      </div>
    )
  return (
    <div className="rk-callout" data-tone="err" data-testid="case-verification" data-state="notHeld" style={{ flexDirection: 'column', gap: 4 }}>
      <span>
        <strong>The fix did not hold.</strong> The next run of <VerifyJob c={c} /> found the problem again after {change || c.fixApplied === undefined ? by : 'it was closed as fixed'}.
      </span>
      <span className="tb-hint" style={{ fontSize: 12 }}>
        The case stays open: analyze it again, or link another change.
      </span>
    </div>
  )
}

/**
 * The proposed changes that resolve the case, with their state, who linked
 * them and when they were applied; Link a change and Unlink for those who
 * work the cases (CF-28).
 */
export function CaseChanges({ c, busy, onUnlink, onLinked }: { c: Case; busy: boolean; onUnlink: (card: string) => void; onLinked: (v: Case) => void }) {
  const [linking, setLinking] = useState(false)
  const list = c.proposals ?? []
  const open = c.status !== 'closed'
  return (
    <>
      <span style={{ display: 'flex', gap: 8, alignItems: 'center', marginTop: 4 }}>
        <span className="rk-h" style={{ fontSize: 12 }}>
          Proposed changes
        </span>
        {c.mayChange && open && (
          <button
            className="rk-btn"
            data-size="sm"
            disabled={busy}
            onClick={() => setLinking(true)}
            title="A change that resolves this case: it is then listed here, and when it is applied the case records the fix"
            data-testid="case-link-change"
          >
            Link a change…
          </button>
        )}
      </span>
      {list.length === 0 ? (
        <span className="tb-hint" data-testid="case-cards">
          None.
        </span>
      ) : (
        <ul style={{ margin: 0, padding: 0, listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 6 }} data-testid="case-cards">
          {list.map((p) => (
            <li key={p.id} style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }} data-testid="case-card" data-id={p.id} data-linked-by={!p.linkedBy || p.linkedBy === 'agent' ? 'agent' : 'person'}>
              <Tag tone={cardTone(p.state)}>{CARD_STATE[p.state] ?? p.state}</Tag>
              {p.state === 'gone' ? (
                <span className="rk-mono">{p.id}</span>
              ) : (
                <a className="rk-link" href={proposalHref(p.id)}>
                  {p.title || p.id}
                </a>
              )}
              <span className="tb-sub" data-testid="case-card-linked">
                {p.state !== 'gone' && p.title ? `${p.id} · ` : ''}
                {linkedText(p)}
                {p.appliedAt ? ` · applied ${fmtTs(p.appliedAt)}` : ''}
              </span>
              {(p.proposedBy || p.decidedBy) && (
                // Who proposed it and who decided it (CF-35).
                <span className="tb-sub" data-testid="case-card-who">
                  {[p.proposedBy ? `proposed by ${actorText(p.proposedBy)}` : '', p.decidedBy ? `decided by ${actorText(p.decidedBy)}` : ''].filter(Boolean).join(' · ')}
                </span>
              )}
              {p.replacedBy && (
                <span className="tb-sub">
                  replaced by <ChangeLink id={p.replacedBy} />
                </span>
              )}
              {c.mayChange && p.linkedBy && p.linkedBy !== 'agent' && (
                <button className="rk-btn" data-size="sm" disabled={busy} onClick={() => onUnlink(p.id)} title="It no longer resolves this case; the change itself stays as it is" data-testid="case-card-unlink">
                  Unlink
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
      {linking && (
        <LinkChange
          c={c}
          onCancel={() => setLinking(false)}
          onLinked={(v) => {
            setLinking(false)
            onLinked(v)
          }}
        />
      )}
    </>
  )
}

/**
 * Link a change: the cards a person may link (on the case's cluster,
 * waiting, approved, or applied lately, not linked yet), found by title
 * or id; the server's refusal (another cluster, linked already, the case
 * closed) shows here.
 */
function LinkChange({ c, onCancel, onLinked }: { c: Case; onCancel: () => void; onLinked: (v: Case) => void }) {
  const [list, setList] = useState<Proposal[] | null>(null)
  const [loadErr, setLoadErr] = useState('')
  const [q, setQ] = useState('')
  const [pick, setPick] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  useEffect(() => {
    let gone = false
    fetchProposals(c.cluster ? { cluster: c.cluster } : {}).then(
      (r) => !gone && setList(r.proposals),
      (e: unknown) => !gone && setLoadErr(errText(e)),
    )
    return () => {
      gone = true
    }
  }, [c.cluster])
  const shown = list ? linkable(list, { cluster: c.cluster, linked: (c.proposals ?? []).map((p) => p.id), q }) : []
  const link = async () => {
    setBusy(true)
    setErr('')
    try {
      onLinked(await linkChange(c.id, pick))
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <ConfirmDialog title={`Link a change to ${c.id}`} confirm="Link" busy={busy} disabled={!pick || !shown.some((p) => p.id === pick)} error={err} onCancel={onCancel} onConfirm={() => void link()}>
      <span style={{ color: 'var(--fg2)' }}>
        Pick the change that resolves this case. It is listed on the case, and when it is applied the case records the fix. Shown: the changes {c.cluster ? `on ${c.cluster} ` : ''}that wait, were approved, or were applied in the last{' '}
        {LINK_APPLIED_DAYS} days.
      </span>
      <input className="cs-in" aria-label="Find a change by its title or id" placeholder="Title or id" value={q} onChange={(e) => setQ(e.target.value)} data-testid="case-link-search" />
      {loadErr ? (
        <p className="tb-err" role="alert" style={{ margin: 0 }} data-testid="case-link-load-error">
          {loadErr}
        </p>
      ) : !list ? (
        <p className="tb-hint" style={{ margin: 0 }}>
          Loading…
        </p>
      ) : shown.length === 0 ? (
        <p className="tb-hint" style={{ margin: 0 }} data-testid="case-link-none">
          {q.trim() ? 'No change matches.' : 'No change to link.'}
        </p>
      ) : (
        <div role="radiogroup" aria-label="Changes" style={{ maxHeight: 260, overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 6 }} data-testid="case-link-list">
          {shown.map((p) => (
            <label key={p.id} style={{ display: 'flex', gap: 8, alignItems: 'baseline', cursor: 'pointer' }} data-testid="case-link-option" data-id={p.id}>
              <input type="radio" name="case-link" checked={pick === p.id} onChange={() => setPick(p.id)} />
              <span style={{ display: 'flex', flexDirection: 'column', minWidth: 0 }}>
                <span>{p.title}</span>
                <span className="tb-sub">
                  <span className="rk-mono">{p.id}</span> · {stateText(p)} · {p.proposedBy ? actorText(p.proposedBy) : p.agentName}
                  {p.resolves?.length ? ` · resolves ${resolvesText(p.resolves, p.resolvesTitles)}` : ''}
                </span>
              </span>
            </label>
          ))}
        </div>
      )}
    </ConfirmDialog>
  )
}

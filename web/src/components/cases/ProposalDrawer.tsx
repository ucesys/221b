import { useCallback, useEffect, useState } from 'react'
import { ACTION_KINDS, approveProposal, commandOf, fetchProposal, inWords, ProposalConflict, recheckProposal, rejectProposal, secondText, stateText, stateTone, targetResource, type CardAction, type CardPlan, type Proposal } from '../../api/cards'
import { actorFull, actorText } from '../../api/actor'
import { useCasesItem } from '../../lib/casesItem'
import { fmtTs } from '../../lib/format'
import { DecideDialog } from '../automation/DecideDialog'
import { Drawer } from '../Drawer'
import { RunInWatson } from '../Sherlock'
import { CaseLink, Resolves } from './ProposalLinks'
import { ProposalCommands, ProposalTroubleshoot } from './ProposalTroubleshoot'
import { ObjectLink, OpenFor } from './ObjectLink'

const errText = (e: unknown) => (e instanceof Error ? e.message : String(e))

const RISK = { low: 'ok', medium: 'warn', high: 'err' } as const

/** What the card says its condition is, in words. */
function conditionText(p: Proposal): string {
  const a = p.condition.args ?? {}
  if (p.condition.kind === 'alert-firing') return `The alert ${a.alert ? a.alert + ' ' : ''}on ${a.key} still fires`
  if (p.condition.kind === 'resource-version') return `${a.resource} ${a.name}${a.namespace ? ' in ' + a.namespace : ''} is still at version ${a.resourceVersion}`
  return ''
}

/** One action: the exact command, or the console change, with its impact and risk. */
function ActionBlock({ a, plan, n, of, cluster }: { a: CardAction; plan?: CardPlan; n: number; of: number; cluster: string }) {
  const cmd = commandOf(a)
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }} data-testid="proposal-action">
      <span style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap', fontSize: 12.5 }}>
        {of > 1 && <strong>{n}.</strong>}
        <span>{ACTION_KINDS[a.kind] ?? a.kind}</span>
        {a.risk && (
          <span className="rk-tag" data-tone={RISK[a.risk]}>
            risk: {a.risk}
          </span>
        )}
      </span>
      {cmd ? (
        <pre className="rk-code" style={{ margin: 0, padding: '8px 12px', whiteSpace: 'pre-wrap' }}>
          $ {cmd}
        </pre>
      ) : (
        <div style={{ fontWeight: 500 }}>{plan?.summary ?? a.kind}</div>
      )}
      {a.impact && <span style={{ color: 'var(--fg2)', fontSize: 12.5 }}>The agent says: {a.impact}</span>}
      {plan?.targets && plan.targets.length > 0 && (
        <span style={{ color: 'var(--fg2)', fontSize: 12.5, display: 'flex', gap: 6, flexWrap: 'wrap', alignItems: 'baseline' }} data-testid="proposal-targets">
          Acts on:
          {plan.targets.map((t, i) => {
            // Each object linked to its page, with how long it has run (IB-06).
            const r = targetResource(t)
            return (
              <span key={t}>
                {r ? <ObjectLink cluster={cluster} resource={r} label={t} /> : t}
                {i < plan.targets!.length - 1 && ','}
              </span>
            )
          })}
        </span>
      )}
      {plan?.impact && plan.impact.length > 0 && (
        <ul style={{ margin: 0, paddingLeft: 18, fontSize: 12.5, color: 'var(--fg2)' }}>
          {plan.impact.map((l, i) => (
            <li key={i}>{l}</li>
          ))}
        </ul>
      )}
      {plan?.unread && (
        <span className="tb-err" style={{ fontSize: 12.5 }}>
          Watson could not read its target when it was proposed: {plan.unread}
        </span>
      )}
    </div>
  )
}

/**
 * Who decided a card, in words, with when (CF-35): the server's decidedBy,
 * or, from a server that does not say, the people on its approvals or its
 * rejection; "" while nobody has.
 */
export function decidedText(p: Pick<Proposal, 'state' | 'decidedBy' | 'approvals' | 'rejection' | 'updated' | 'lastAt'>): string {
  if (p.state === 'queued') return ''
  const at = p.rejection?.at ?? p.approvals?.[p.approvals.length - 1]?.at ?? p.lastAt ?? p.updated
  const when = at ? `, ${fmtTs(at)}` : ''
  if (p.decidedBy) return actorText(p.decidedBy) + when
  if (p.rejection) return p.rejection.by.name + when
  const names = [...new Set((p.approvals ?? []).map((d) => (d.standing ? 'a standing approval' : d.by.name)))]
  return names.length ? names.join(' and ') + when : ''
}

/**
 * A proposed change, an agent's proposal read before anyone decides
 * (design: Self-explaining; "Proposed changes" since IB-04):
 * what started it, the agent and its reasoning, the evidence with dates,
 * the exact actions with their impact and risk, when it expires, and what
 * Watson checks again when it is approved; an agent never counts as a
 * person. Since CF-61 and CF-62 (ADR-136) the drawer opens near full
 * screen, the proposal on the left and Troubleshoot on the right
 * (ProposalTroubleshoot.tsx: its own Sherlock conversation and its Watson),
 * tabs Proposal | Troubleshoot on a phone; the foot's Run in Watson, which
 * was Approve, brings its commands to that Watson as one card, the
 * proposal's own, where Watson asks for the approval: that approval is the
 * proposal's. Check again and Reject… stay at the foot, and "Needs a
 * second approval" when one of two was given.
 */
export function ProposalDrawer({ id, initial, onClose, onDecided, onToast }: { id: string; initial?: Proposal; onClose: () => void; onDecided?: (p: Proposal) => void; onToast?: (msg: string) => void }) {
  const [p, setP] = useState<Proposal | null>(initial ?? null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [rejecting, setRejecting] = useState(false)
  const [rejectErr, setRejectErr] = useState('')
  // Run in Watson at the foot: each press brings the card to Watson again.
  const [bring, setBring] = useState(0)
  // The log is read again as the commands run from the proposal change.
  const reload = useCallback(() => {
    fetchProposal(id).then(setP, () => {})
  }, [id])
  // Sherlock, asked while it is open, knows which change "this" is (IB-11).
  useCasesItem(p ? { kind: 'proposal', id: p.id, cluster: p.cluster, summary: `${p.title} · ${stateText(p)}` } : { kind: 'proposal', id })
  useEffect(() => {
    let gone = false
    fetchProposal(id).then(
      (x) => !gone && setP(x),
      (e: unknown) => !gone && setErr(errText(e)),
    )
    return () => {
      gone = true
    }
  }, [id])
  if (!p) {
    return (
      <Drawer label="Proposed change" kicker="Proposed change" title="…" onClose={onClose} width={680} testId="proposal-drawer" copy={false}>
        {err ? <p className="tb-err">{err}</p> : <p className="tb-hint">Loading…</p>}
      </Drawer>
    )
  }
  const decided = (x: Proposal, msg: string) => {
    setP(x)
    onDecided?.(x)
    onToast?.(msg)
  }
  const approve = async () => {
    setBusy(true)
    setErr('')
    try {
      const x = await approveProposal(p.id)
      decided(x, x.state === 'queued' ? `Approved: it waits for ${x.needed - (x.approvals?.length ?? 0)} more.` : 'Approved: Watson runs it now.')
    } catch (e) {
      setErr(errText(e))
      if (e instanceof ProposalConflict) {
        // Someone decided it first (IB-08): the card as it is now, and the
        // list behind it read again.
        setP(e.proposal)
        onDecided?.(e.proposal)
      } else {
        // A card that does not hold says why on it.
        fetchProposal(p.id).then(setP, () => {})
      }
    } finally {
      setBusy(false)
    }
  }
  const recheck = async () => {
    setBusy(true)
    setErr('')
    try {
      setP(await recheckProposal(p.id))
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  const reject = async (reason: string) => {
    setBusy(true)
    setRejectErr('')
    try {
      const x = await rejectProposal(p.id, reason)
      setRejecting(false)
      decided(x, 'Rejected.')
    } catch (e) {
      if (e instanceof ProposalConflict) {
        setRejecting(false)
        setErr(e.message)
        setP(e.proposal)
        onDecided?.(e.proposal)
      } else setRejectErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  const open = p.state === 'queued'
  const second = secondText(p)
  const cond = conditionText(p)
  const evidence = p.evidence ?? []
  return (
    <Drawer
      label="Review: a proposed change"
      kicker={`Proposed change · ${p.clusterName}`}
      title={p.title}
      copy={false}
      width={680}
      size="full"
      split={{ label: 'Troubleshoot', main: 'Proposal', content: <ProposalTroubleshoot p={p} onToast={onToast} onCommands={reload} bring={bring} onApprove={() => void approve()} busy={busy} error={err} /> }}
      sideOn={bring || undefined}
      onClose={onClose}
      testId="proposal-drawer"
      facts={
        <span style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
          <span className="rk-pill" data-tone={stateTone(p.state)} data-testid="proposal-state">
            {stateText(p)}
          </span>
          {p.production && p.needed > 1 && (
            <span className="rk-pill" data-tone="err">
              Production: {p.needed} people
            </span>
          )}
          {open && (
            <span className="rk-pill" data-tone="warn" title={fmtTs(p.expires)}>
              Expires {inWords(p.expires)}
            </span>
          )}
          {open && (
            <span className="rk-pill" title={`Proposed ${fmtTs(p.created)}`}>
              <OpenFor since={p.created} prefix="Waiting " />
            </span>
          )}
        </span>
      }
      footer={
        open ? (
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, alignItems: 'center', flex: '1 1 auto' }}>
            {/* Only while one of two approvals is given (CF-62). */}
            {second && (
              <span className="rk-pill" data-tone="warn" data-testid="proposal-count">
                {second}
              </span>
            )}
            <span style={{ marginRight: 'auto' }} />
            <button className="rk-btn" onClick={() => void recheck()} disabled={busy} data-testid="proposal-recheck">
              Check again
            </button>
            <button className="rk-btn" onClick={() => setRejecting(true)} disabled={busy} data-testid="proposal-reject">
              Reject…
            </button>
            {/* Approve, renamed (CF-62): its commands go to the drawer's Watson, which asks for the approval. */}
            <RunInWatson onClick={() => setBring((n) => n + 1)} disabled={busy || !p.canApprove} title={p.canApprove ? 'Brings its commands to Watson beside it as one card. Watson asks for your approval there; that approval is the proposal’s.' : p.cannot} testId="proposal-run-watson" />
          </div>
        ) : undefined
      }
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
        {err && (
          <p className="tb-err" role="alert" style={{ margin: 0 }} data-testid="proposal-error">
            {err}
          </p>
        )}
        {open && p.cannot && (
          <p className="tb-hint" style={{ margin: 0 }} data-testid="proposal-cannot">
            {p.cannot}
          </p>
        )}
        <dl className="rk-kv">
          <dt>Started by</dt>
          <dd>
            {p.source.label || p.source.kind}
            {p.source.setBy && p.source.kind !== 'chat' && ` · trigger set by ${p.source.setBy}`}
            {p.source.kind === 'case' && (
              <>
                {' · '}
                <CaseLink p={p} />
              </>
            )}
          </dd>
          <dt>Resolves</dt>
          <dd data-testid="proposal-resolves">
            {p.resolves?.length ? <Resolves ids={p.resolves} by={p.resolvesBy} titles={p.resolvesTitles} label={false} /> : <span style={{ color: 'var(--fg3)' }}>No case named.</span>}
          </dd>
          <dt>Proposed by</dt>
          <dd data-testid="proposal-proposed-by">
            {p.proposedBy ? actorText(p.proposedBy) : p.agentName}
            {p.model && (
              <>
                {' '}
                (<span className="rk-mono">{p.model}</span>)
              </>
            )}
            , {fmtTs(p.created)}
          </dd>
          {decidedText(p) && (
            // Who decided it (CF-35): the approver who completed it, the rejecter, or the system.
            <>
              <dt>Decided by</dt>
              <dd data-testid="proposal-decided-by">{decidedText(p)}</dd>
            </>
          )}
          {p.reasoning && (
            <>
              <dt>Why</dt>
              <dd style={{ whiteSpace: 'pre-wrap' }}>{p.reasoning}</dd>
            </>
          )}
          <dt>Evidence</dt>
          <dd>
            {evidence.length === 0 ? (
              <span style={{ color: 'var(--fg3)' }}>None given.</span>
            ) : (
              <ul style={{ margin: 0, paddingLeft: 18, display: 'flex', flexDirection: 'column', gap: 4 }} data-testid="proposal-evidence">
                {evidence.map((e, i) => (
                  <li key={i}>
                    <span className="rk-tag">{e.kind}</span> <span className={e.kind === 'log' || e.kind === 'event' ? 'rk-mono' : undefined}>{e.text}</span>
                    {e.at && <span style={{ color: 'var(--fg3)' }}> · {fmtTs(e.at)}</span>}
                    {e.ref && <span style={{ color: 'var(--fg3)' }}> · {e.ref}</span>}
                  </li>
                ))}
              </ul>
            )}
          </dd>
        </dl>
        <section style={{ display: 'flex', flexDirection: 'column', gap: 10 }} aria-label="What Watson will run">
          <h3 className="rk-h">{open ? 'What Watson will run' : 'What Watson was to run'}</h3>
          {p.actions.map((a, i) => (
            <ActionBlock key={i} a={a} plan={p.plans[i]} n={i + 1} of={p.actions.length} cluster={p.cluster} />
          ))}
        </section>
        {open && (
          <div className="rk-callout" style={{ flexDirection: 'column', gap: 4, background: 'var(--surface2)' }} data-testid="proposal-checks">
            <strong>Checked again when you approve it in Watson</strong>
            <span style={{ color: 'var(--fg2)', fontSize: 12.5 }}>
              {cond ? `${cond}, and ` : ''}what it acts on is unchanged since the proposal. If either changed, approving is refused and says why.
            </span>
            {p.recheck && (
              <span className={p.recheck.ok ? undefined : 'tb-err'} style={{ fontSize: 12.5 }} data-testid="proposal-recheck-result">
                {p.recheck.ok ? `Checked ${fmtTs(p.recheck.at)}: it holds.` : `Checked ${fmtTs(p.recheck.at)}: ${p.recheck.why}`}
              </span>
            )}
          </div>
        )}
        <ProposalCommands p={p} />
        {p.results && p.results.length > 0 && (
          <section style={{ display: 'flex', flexDirection: 'column', gap: 6 }} aria-label="Results" data-testid="proposal-results">
            <h3 className="rk-h">Results</h3>
            {p.results.map((r) => (
              <div key={r.action} style={{ fontSize: 12.5 }}>
                <span className="rk-tag" data-tone={r.ok ? 'ok' : 'err'}>
                  {r.ok ? 'done' : 'failed'}
                </span>{' '}
                {r.summary}
                {r.detail && (
                  <pre className="rk-code" style={{ margin: '4px 0 0', padding: '8px 12px', whiteSpace: 'pre-wrap' }}>
                    {r.detail}
                  </pre>
                )}
              </div>
            ))}
          </section>
        )}
        {p.reason && !open && <p style={{ margin: 0, color: 'var(--fg2)' }}>{p.reason}</p>}
        {p.history && p.history.length > 0 && (
          <section style={{ display: 'flex', flexDirection: 'column', gap: 4 }} aria-label="History" data-testid="proposal-history">
            <h3 className="rk-h">History</h3>
            {p.history.map((h, i) => (
              <span key={i} style={{ fontSize: 12.5, color: 'var(--fg2)' }} data-testid="proposal-history-entry">
                {fmtTs(h.at)} · {h.what}
                {(h.actor || h.by) && <span data-testid="proposal-history-by" data-kind={h.actor?.kind}>{` · ${h.actor ? actorFull(h.actor) : h.by}`}</span>}
              </span>
            ))}
          </section>
        )}
      </div>
      {rejecting && (
        <DecideDialog how="reject" what={p.title} busy={busy} error={rejectErr} onCancel={() => setRejecting(false)} onConfirm={(c) => void reject(c)}>
          {p.requester ? `${p.requester.name} asked for this in a chat and reads your reason.` : `${p.agentName} proposed this. Rejecting leaves things as they are; the reason is kept with the card.`}
        </DecideDialog>
      )}
    </Drawer>
  )
}

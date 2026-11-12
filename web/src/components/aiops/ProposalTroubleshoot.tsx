// A proposed change's Troubleshoot side (Phase 12, CF-61, CF-62): beside
// the proposal, Sherlock and Watson, one at a time with a switch, as on a
// case (CaseTroubleshoot.tsx). Sherlock's conversation is the proposal's
// own, shared by the people who may approve it or work the cases, each
// question naming who asked; it starts from the proposal and the cases it
// resolves, and is deleted when the proposal is decided. Watson runs on the
// proposal's cluster with every command sent from it ("proposal:PC-12"),
// which logs it. Run in Watson at the foot of the drawer brings the
// proposal's own commands into this Watson as one card: the proposal's
// card itself, approved there with the proposal's one approval (two people
// on production), then run as the approver, here, from the proposal.
import { useEffect, useRef, useState } from 'react'
import { commandOf, inWords, secondText, stateText, type Proposal } from '../../api/cards'
import type { CaseCommand } from '../../api/casework'
import { askProposalSherlock, proposalFrom, stopProposalSherlock, useProposalSherlock } from '../../api/proposalSherlock'
import { sendToWatson, useWatson, WatsonError } from '../../api/watson'
import { fmtTs } from '../../lib/format'
import type { Pane } from '../../lib/sherlock'
import { WatsonIcon } from '../Icons'
import { SherlockPane, type SharedChat } from '../Sherlock'
import { autoRunReads, WatsonContext, WatsonPane, type WatsonApi } from '../Watson'
import { commandStatusText } from './CaseTroubleshoot'

interface Props {
  p: Proposal
  onToast?: (msg: string) => void
  /** A command sent from the proposal started or ended: read the proposal again for its log. */
  onCommands?: () => void
  /** Changed to a new number, Run in Watson at the foot brought the proposal's card here. */
  bring?: number
  /** Approves the proposal: its one approval, which the drawer gives and reports. */
  onApprove: () => void
  busy?: boolean
  /** Why the last approval did not hold, for the card. */
  error?: string
}

const live = (p: Pick<Proposal, 'state'>) => p.state === 'queued' || p.state === 'approved'

/** Sherlock and Watson on a proposed change: the drawer's Troubleshoot part. */
export function ProposalTroubleshoot(props: Props) {
  const { p } = props
  // Open in the drawer while it waited, the side stays on once decided,
  // so the run of its approval shows in Watson.
  const [seen] = useState(live(p))
  if (!seen && !live(p)) {
    return (
      <p className="tb-hint" style={{ margin: 0, padding: 20 }} data-testid="proposal-trouble-decided">
        Decided. Sherlock’s conversation on the proposed change was deleted when it was decided; the changes run from it that finished done are kept with it, under Commands run from this proposed change.
      </p>
    )
  }
  return <Panes {...props} />
}

function Panes({ p, onToast, onCommands, bring, onApprove, busy, error }: Props) {
  const toast = onToast ?? (() => undefined)
  const waiting = p.state === 'queued'
  const sherlock = useProposalSherlock(p.id, live(p))
  const watson = useWatson(p.cluster, !!p.mayAsk)
  const [pane, setPane] = useState<Pane>('sherlock')
  const [brought, setBrought] = useState(false)
  const name = p.clusterName || p.cluster
  const from = proposalFrom(p.id)
  // The log is the server's: as a command sent from the proposal comes or
  // changes state, the proposal is read again, shortly after and later.
  const changed = useRef(onCommands)
  useEffect(() => {
    changed.current = onCommands
  }, [onCommands])
  const states = watson.items
    .filter((x) => x.from === from)
    .map((x) => `${x.id}:${x.status}`)
    .join(',')
  useEffect(() => {
    if (!states) return
    const soon = setTimeout(() => changed.current?.(), 400)
    const later = setTimeout(() => changed.current?.(), 2000)
    return () => {
      clearTimeout(soon)
      clearTimeout(later)
    }
  }, [states])
  // Run in Watson at the foot: the proposal's card, in Watson.
  // A side mounted by the hand-over (on a phone) takes it too.
  const [last, setLast] = useState(0)
  if ((bring ?? 0) !== last) {
    setLast(bring ?? 0)
    if (bring) {
      setBrought(true)
      setPane('watson')
    }
  }
  // Watson's terminal is there for who may ask, where Sherlock is on, as
  // on a case; the proposal's card shows once brought even without it.
  const hasW = !!p.mayAsk && !!sherlock.status?.enabled && !!watson.status?.available
  const withW = hasW || brought
  const shown: Pane = withW ? pane : 'sherlock'
  const watsonApi: WatsonApi = {
    available: hasW && waiting,
    readOnly: !!watson.status?.readOnly,
    how: watson.status?.how,
    onCase: 'proposal',
    send: (command, o = {}) => {
      setPane('watson')
      sendToWatson(p.cluster, command, { run: autoRunReads(), why: o.why, from }).catch((e: unknown) => toast(e instanceof WatsonError ? e.message : String(e)))
    },
  }
  const shared: SharedChat = {
    ask: (q) => askProposalSherlock(p.id, q),
    stop: () => stopProposalSherlock(p.id),
    sub: `The conversation on ${p.id}, shared by everyone who may approve it or works its cases`,
    empty: `Ask Sherlock about ${p.id} on ${name}. Sherlock starts from the proposed change and the cases it resolves, and reads the cluster as you, with your rights. Everyone who may approve it or works its cases sees this conversation and adds to it; it is deleted when the proposed change is decided.`,
    readOnly: !waiting ? `Decided: ${stateText(p)}. The conversation was deleted.` : p.mayAsk ? undefined : 'You read this conversation: only the people who may approve proposed changes or work the cases ask here.',
  }
  const switcher = withW ? (
    <div className="rk-seg" role="group" aria-label="Show" data-testid="proposal-trouble-switch">
      <button aria-pressed={shown === 'sherlock'} onClick={() => setPane('sherlock')} data-testid="proposal-trouble-sherlock">
        Sherlock
      </button>
      <button aria-pressed={shown === 'watson'} onClick={() => setPane('watson')} data-testid="proposal-trouble-watson">
        Watson
      </button>
    </div>
  ) : undefined
  const card = brought ? <ProposalCard p={p} onApprove={onApprove} busy={busy} error={error} /> : null
  return (
    <WatsonContext.Provider value={watsonApi}>
      {/* Its own Sherlock takes questions: the open dock is put away while it shows (R2-8). */}
      <div style={{ flex: '1 1 auto', minHeight: 0, display: 'flex', flexDirection: 'column' }} data-testid="proposal-trouble" data-pane={shown} data-own-chat={waiting && p.mayAsk && sherlock.status?.enabled ? 'true' : undefined}>
        <div className="cs-tpane" style={{ display: shown === 'sherlock' ? 'flex' : 'none' }}>
          <SherlockPane cluster={p.cluster} clusterName={name} state={sherlock} onToast={toast} switcher={switcher} shared={shared} />
        </div>
        {withW && (
          <div className="cs-tpane" style={{ display: shown === 'watson' ? 'flex' : 'none' }}>
            {hasW ? (
              <WatsonPane cluster={p.cluster} clusterName={name} state={watson} onToast={toast} switcher={switcher} scope={{ from, label: p.id }} tail={card} />
            ) : (
              <section className="rk-dpane rk-wat" aria-label="Watson" data-testid="watson">
                <div className="rk-dph">
                  <span className="rk-avatar" data-k="w" aria-hidden="true">
                    <WatsonIcon />
                  </span>
                  <h2>Watson</h2>
                  <small>{`The card of ${p.id}`}</small>
                  {switcher}
                </div>
                <div className="rk-term" role="log" aria-label="Watson console" tabIndex={0} data-testid="watson-log">
                  <div className="rk-tl" data-k="dim" data-testid="proposal-trouble-alone">
                    Watson’s terminal is not here{p.mayAsk ? `: it runs where Sherlock is on for ${name}` : ''}. Approving runs the proposed change as you; its output shows in your Watson panel.
                  </div>
                  {card}
                </div>
              </section>
            )}
          </div>
        )}
      </div>
    </WatsonContext.Provider>
  )
}

/**
 * The proposal's own card in its Watson (CF-62): its commands as one
 * batch, with what they act on, and the approval Watson asks for, which is
 * the proposal's one approval. Approved by the last person it needs, its
 * commands run as them, from the proposal, in this Watson.
 */
function ProposalCard({ p, onApprove, busy, error }: { p: Proposal; onApprove: () => void; busy?: boolean; error?: string }) {
  const waiting = p.state === 'queued'
  const got = p.approvals?.length ?? 0
  const completes = got + 1 >= p.needed
  const second = secondText(p)
  const head = !waiting ? stateText(p) : p.canApprove ? 'Waiting for your approval' : second || 'Waiting for approval'
  return (
    <div className="rk-appr" data-kind="change" data-testid="watson-proposal-card" data-id={p.id} data-state={p.state}>
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px 8px' }}>
        <strong style={{ fontSize: 12, fontWeight: 600, color: waiting ? 'var(--term-warn)' : 'var(--term-fg)' }}>{head}</strong>
        <span className="rk-ttag" data-k="change">
          Changes the cluster
        </span>
        <span style={{ fontSize: 11.5, color: 'var(--term-dim)' }}>From {p.id}</span>
        {waiting && (
          <span style={{ fontSize: 11.5, color: 'var(--term-dim)', marginLeft: 'auto' }} title={fmtTs(p.expires)}>
            expires {inWords(p.expires)}
          </span>
        )}
      </div>
      {p.actions.map((a, i) => {
        const plan = p.plans[i]
        return (
          <div key={i} data-testid="watson-proposal-action">
            <pre data-testid="watson-card-command">{commandOf(a) || plan?.summary || a.kind}</pre>
            {(plan?.targets?.length ?? 0) > 0 && <p style={{ marginTop: 6, color: 'var(--term-dim)' }}>Acts on {plan!.targets!.join(', ')}</p>}
            {(plan?.impact?.length ?? 0) > 0 && (
              <ul>
                {plan!.impact!.map((t, j) => (
                  <li key={j}>{t}</li>
                ))}
              </ul>
            )}
          </div>
        )
      })}
      <p>{p.title}</p>
      {waiting && second && (
        <p style={{ marginTop: 6, color: 'var(--term-warn)' }} data-testid="proposal-card-second">
          {second}: on production a change needs two people, and {p.approvals?.[0]?.by.name ?? 'someone'} approved it.
        </p>
      )}
      {waiting && !p.canApprove && p.cannot && (
        <p style={{ marginTop: 6, color: 'var(--term-warn)' }} data-testid="watson-waiting">
          {p.cannot}
        </p>
      )}
      {error && (
        <p style={{ marginTop: 6, color: 'var(--term-err, var(--term-warn))' }} role="alert" data-testid="proposal-card-error">
          {error}
        </p>
      )}
      {waiting && (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, marginTop: 10 }}>
          <button
            className="rk-tbtn"
            data-primary="true"
            onClick={onApprove}
            disabled={busy || !p.canApprove}
            title={p.canApprove ? (completes ? 'Approves the proposed change: Watson checks it again, then runs it as you, here' : 'Approves the proposed change: it then waits for a second person') : p.cannot}
            data-testid="proposal-approve"
          >
            {completes ? 'Approve and run' : 'Approve'}
          </button>
        </div>
      )}
    </div>
  )
}

/**
 * The commands run from the proposal (CF-61): everyone's while it waits,
 * with who ran each, when, read or change, and how it ended; once decided,
 * the changes that finished done, which it keeps. Never the output.
 */
export function ProposalCommands({ p }: { p: Pick<Proposal, 'commands' | 'state'> }) {
  const list: CaseCommand[] = p.commands ?? []
  const waiting = p.state === 'queued' || p.state === 'approved'
  if (list.length === 0) return null
  return (
    <section className="cs-sec" aria-label="Commands run from this proposed change" data-testid="proposal-commands">
      <h3 className="rk-h">{waiting ? 'Commands run from this proposed change' : 'Changes run from it'}</h3>
      <ol className="cs-msgs">
        {list
          .slice()
          .reverse()
          .map((x) => (
            <li key={x.id} className="cs-msg" data-testid="proposal-command" data-status={x.status} data-kind={x.change ? 'change' : 'read'}>
              <span className="cs-meta">
                {x.by} · {fmtTs(x.at)} ·{' '}
                <span className="rk-tag" data-tone={x.change ? 'warn' : 'muted'}>
                  {x.change ? 'change' : 'read'}
                </span>{' '}
                · {commandStatusText(x)}
                {x.approver ? ` · approved by ${x.approver}` : ''}
              </span>
              <span className="cs-text rk-mono" style={{ fontSize: 12 }}>
                $ {x.line}
              </span>
            </li>
          ))}
      </ol>
      <span className="tb-hint" style={{ fontSize: 12 }}>
        {waiting
          ? 'Run from the drawer’s Watson. Once decided it keeps only the changes that finished done, and gives them to the cases it resolves; no output is ever kept.'
          : 'Kept when it was decided, and given to the cases it resolves.'}
      </span>
    </section>
  )
}

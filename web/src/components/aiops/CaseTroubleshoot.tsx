// Troubleshooting a case in its drawer (Phase 12, wave 6f, CF-46 to CF-49):
// beside the case, Sherlock and Watson, one at a time with a switch, as
// they sit in the dock on a narrow screen. Sherlock's conversation is the
// case's, shared by the people who work it, each question naming who asked;
// it is deleted when the case closes. Watson runs on the case's cluster with
// every command sent from the case ("case:C-12"), which logs it; Run in
// Watson, on Sherlock's commands and on the case's suggested fix, hands the
// command to this Watson as the dock's Send to Watson does: a card that asks
// before it runs, run as the person, with all of Watson's rules. The log
// itself is on the case's side (CaseCommands).
import { useEffect, useRef, useState } from 'react'
import { askCaseSherlock, stopCaseSherlock, useCaseSherlock } from '../../api/caseSherlock'
import { caseFrom, type Case, type CaseCommand } from '../../api/casework'
import { sendToWatson, useWatson, WatsonError } from '../../api/watson'
import { fmtTs } from '../../lib/format'
import type { Pane } from '../../lib/sherlock'
import { SherlockPane, type SharedChat } from '../Sherlock'
import { autoRunReads, WatsonContext, WatsonPane, type WatsonApi } from '../Watson'

interface Props {
  c: Case
  /** The case's cluster as people name it. */
  clusterName?: string
  onToast?: (msg: string) => void
  /** A command sent from the case started or ended: the case's log changed, read it again. */
  onCommands?: () => void
  /** A command handed over from the case's side (its suggested fix): n tells one hand-over from the next. */
  run?: CaseRun | null
  /** The hand-over reached Watson: let it go, so it is not handed again. */
  onRan?: () => void
  /** Whether this Watson takes commands now, for the case's side to offer Run in Watson. */
  onWatson?: (on: boolean) => void
}

/** A command for the case's Watson, from the case's side of the drawer. */
export interface CaseRun {
  command: string
  n: number
}

/** Sherlock and Watson on a case: the drawer's Troubleshoot part. */
export function CaseTroubleshoot({ c, clusterName, onToast, onCommands, run, onRan, onWatson }: Props) {
  const open = c.status !== 'closed'
  const cluster = c.cluster ?? ''
  const name = clusterName || c.clusterName || cluster
  const toast = onToast ?? (() => undefined)
  if (!cluster) {
    return (
      <p className="tb-hint" style={{ margin: 0, padding: 20 }} data-testid="case-trouble-none">
        The case names no cluster: name one (Edit…) to troubleshoot it here with Sherlock and Watson.
      </p>
    )
  }
  if (!open) {
    return (
      <p className="tb-hint" style={{ margin: 0, padding: 20 }} data-testid="case-trouble-closed">
        Closed. Sherlock’s conversation on the case was deleted when it closed; the changes run from it that finished done are kept with it, under Commands run from this case. Reopen it to troubleshoot it again.
      </p>
    )
  }
  return <Panes c={c} cluster={cluster} name={name} onToast={toast} onCommands={onCommands} run={run} onRan={onRan} onWatson={onWatson} />
}

function Panes({
  c,
  cluster,
  name,
  onToast,
  onCommands,
  run,
  onRan,
  onWatson,
}: {
  c: Case
  cluster: string
  name: string
  onToast: (msg: string) => void
  onCommands?: () => void
  run?: CaseRun | null
  onRan?: () => void
  onWatson?: (on: boolean) => void
}) {
  const sherlock = useCaseSherlock(c.id)
  const watson = useWatson(cluster, c.mayChange)
  const [pane, setPane] = useState<Pane>('sherlock')
  const from = caseFrom(c.id)
  // The log is the server's, which hears of a command's end as Watson's
  // stream does: as a command sent from the case comes or changes state,
  // the case is read again, shortly after and once more later.
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
  // Watson is there for who works the cases, where Sherlock is on, as in the dock.
  const hasW = c.mayChange && !!sherlock.status?.enabled && !!watson.status?.available
  const shown: Pane = hasW ? pane : 'sherlock'
  // Run in Watson hands the command to this Watson, sent from the case: a
  // change waits on its card for an approval, a read for Run unless reads
  // run by themselves, as in the dock.
  const watsonApi: WatsonApi = {
    available: hasW,
    readOnly: !!watson.status?.readOnly,
    how: watson.status?.how,
    onCase: true,
    send: (command, o = {}) => {
      setPane('watson')
      sendToWatson(cluster, command, { run: autoRunReads(), why: o.why, from }).catch((e: unknown) => onToast(e instanceof WatsonError ? e.message : String(e)))
    },
  }
  const sendRef = useRef(watsonApi.send)
  const ran = useRef(onRan)
  useEffect(() => {
    sendRef.current = watsonApi.send
    ran.current = onRan
  })
  useEffect(() => {
    onWatson?.(hasW)
  }, [hasW, onWatson])
  useEffect(() => {
    if (!run || !hasW) return
    sendRef.current(run.command)
    ran.current?.()
  }, [run, hasW])
  const shared: SharedChat = {
    ask: (q) => askCaseSherlock(c.id, q),
    stop: () => stopCaseSherlock(c.id),
    sub: `The conversation on ${c.id}, shared by everyone who works it`,
    empty: `Ask Sherlock about ${c.id} on ${name}. Sherlock starts from the case, its analysis and the cases like it, and reads the cluster as you, with your rights. Everyone who works the case sees this conversation and adds to it; it is deleted when the case closes.`,
    readOnly: c.mayChange ? undefined : 'You read this conversation: only the people who work the cases ask here.',
  }
  const switcher = hasW ? (
    <div className="rk-seg" role="group" aria-label="Show" data-testid="case-trouble-switch">
      <button aria-pressed={shown === 'sherlock'} onClick={() => setPane('sherlock')} data-testid="case-trouble-sherlock">
        Sherlock
      </button>
      <button aria-pressed={shown === 'watson'} onClick={() => setPane('watson')} data-testid="case-trouble-watson">
        Watson
      </button>
    </div>
  ) : undefined
  return (
    <WatsonContext.Provider value={watsonApi}>
      <div style={{ flex: '1 1 auto', minHeight: 0, display: 'flex', flexDirection: 'column' }} data-testid="case-trouble" data-pane={shown}>
        <div className="cs-tpane" style={{ display: shown === 'sherlock' ? 'flex' : 'none' }}>
          <SherlockPane cluster={cluster} clusterName={name} state={sherlock} onToast={onToast} switcher={switcher} shared={shared} />
        </div>
        {hasW && (
          <div className="cs-tpane" style={{ display: shown === 'watson' ? 'flex' : 'none' }}>
            <WatsonPane cluster={cluster} clusterName={name} state={watson} onToast={onToast} switcher={switcher} scope={{ from, label: c.id }} />
          </div>
        )}
      </div>
    </WatsonContext.Provider>
  )
}

/** A command's status as the log says it: a case's or a proposal's (CF-61). */
export function commandStatusText(x: CaseCommand): string {
  switch (x.status) {
    case 'done':
      return x.exit ? `exit code ${x.exit}` : 'done'
    case 'pending':
      return x.change ? 'waiting for approval' : 'not run yet'
    default:
      return x.status
  }
}

/**
 * The commands run from the case (CF-48): everyone's, while it is open,
 * with who ran each, when, read or change, and how it ended; once closed,
 * the changes that finished done, which the case keeps. Never the output.
 */
export function CaseCommands({ c }: { c: Case }) {
  const list = c.commands ?? []
  const open = c.status !== 'closed'
  if (list.length === 0) return null
  return (
    <div className="cs-sec" data-testid="case-commands">
      <span className="rk-h">{open ? 'Commands run from this case' : 'Changes that fixed it'}</span>
      <ol className="cs-msgs">
        {list
          .slice()
          .reverse()
          .map((x) => (
            <li key={x.id} className="cs-msg" data-testid="case-command" data-status={x.status} data-kind={x.change ? 'change' : 'read'}>
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
        {open
          ? 'Run from the drawer’s Watson. When the case closes it keeps only the changes that finished done, for the cases like it later; the reads and the rest go, and no output is ever kept.'
          : 'Kept when the case closed: a later case like it starts from them.'}
      </span>
    </div>
  )
}

import { createContext, useContext, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { useToolbox } from './toolbox/context'
import { APPROVE_ALL_CHOICES, approveAllWatson, approveWatson, clearWatson, discardWatson, editWatson, endApproveAllWatson, sendToWatson, stopWatson, WatsonError, watsonSupports, type Send, type WatsonItem, type WatsonLine, type WatsonState } from '../api/watson'
import { fmtClock } from '../lib/format'
import { readJSON, writeJSON } from '../lib/storage'
import { CopyIcon, WatsonIcon } from './Icons'

// ---------- sending from anywhere ----------

export interface WatsonApi {
  /** Whether Watson runs in this cluster now. */
  available: boolean
  /** Whether changes are refused here (a read-only connection). */
  readOnly: boolean
  /** Where changes are refused, how to allow them. */
  how?: string
  /** Where Watson is off, why and how to turn it on, for the fixes that would use it. */
  off?: WatsonOff
  /** Hands commands to Watson: changes get an approval card, reads run. */
  send: (command: string, o?: Send) => void
}

/** Why Watson is not offered here, and where to turn it on. */
export interface WatsonOff {
  text: string
  link?: { label: string; href: string }
}

/**
 * Where Watson is off (6.3 review), a fix says so, rather than offering
 * only Copy: Watson runs where Sherlock is on, which an administrator
 * turns on in Settings.
 */
export function watsonOff(o: { installed: boolean; sherlockOn: boolean; admin: boolean; settingsHref: string }): WatsonOff | undefined {
  if (!o.installed || o.sherlockOn) return undefined
  if (o.admin) return { text: 'To run it from here, turn on Sherlock for this cluster: Watson runs commands where Sherlock is on.', link: { label: 'Turn on Sherlock', href: o.settingsHref } }
  return { text: 'To run it from here, ask an administrator to turn on Sherlock for this cluster: Watson runs commands where Sherlock is on.' }
}

export const WatsonContext = createContext<WatsonApi>({ available: false, readOnly: true, send: () => undefined })

export function useWatsonApi(): WatsonApi {
  return useContext(WatsonContext)
}

/**
 * A button that hands a change to Watson (P6-06): nothing changes until
 * someone approves the card it makes. Nothing shows while Watson is not
 * available; on a read-only connection the button says why it is off.
 */
export function WatsonButton({ command, from, why, label, title, testid, over }: { command: string; from: string; why?: string; label: string; title?: string; testid?: string; over?: boolean }) {
  const { available, readOnly, send } = useWatsonApi()
  if (!available) return null
  return (
    <button
      className="rk-btn"
      onClick={() => send(command, { from, why, over })}
      title={readOnly ? 'This cluster is connected read-only, so Watson changes nothing here.' : (title ?? `Hands “${command}” to Watson. Nothing runs until it is approved.`)}
      disabled={readOnly}
      data-testid={testid ?? 'watson-button'}
      data-command={command}
    >
      {label}
    </button>
  )
}

/**
 * A fix's command (P6-06): Send to Watson, which makes an approval card,
 * and Copy. A command Watson does not run (creating a binding, say) can
 * only be copied.
 */
export function FixCommand({ command, from, why, onCopy }: { command: string; from: string; why?: string; onCopy: () => void }) {
  const { available, readOnly, how, off, send } = useWatsonApi()
  const [sent, setSent] = useState(false)
  const runs = available && watsonSupports(command)
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }} data-testid="fix-command">
      <code className="rk-mono" style={{ fontSize: 11.5, color: 'var(--fg2)', overflowWrap: 'anywhere' }}>
        {command}
      </code>
      {runs && (
        <button
          className="rk-btn"
          onClick={() => {
            send(command, { from, why })
            setSent(true)
          }}
          disabled={readOnly || sent}
          title={readOnly ? 'This cluster is connected read-only, so Watson changes nothing here.' : 'Hands the command to Watson. Nothing changes until someone approves it there.'}
          style={{ height: 26 }}
          data-testid="fix-send-watson"
        >
          <WatsonIcon />
          {sent ? 'Sent to Watson' : 'Send to Watson'}
        </button>
      )}
      <button className="rk-btn" onClick={onCopy} title={runs ? 'Copy the command' : 'Copy the command; Watson does not run this kind'} style={{ height: 26 }}>
        <CopyIcon />
        Copy the command
      </button>
      {runs && readOnly && (
        <span className="rk-fhint" style={{ flexBasis: '100%' }} data-testid="fix-read-only">
          This cluster is connected read-only, so Watson changes nothing here.{how ? ` ${how}` : ''}
        </span>
      )}
      {!available && off && watsonSupports(command) && (
        <span className="rk-fhint" style={{ flexBasis: '100%' }} data-testid="fix-watson-off">
          {off.text}
          {off.link && (
            <>
              {' '}
              <a className="rk-link" href={off.link.href}>
                {off.link.label}
              </a>
            </>
          )}
        </span>
      )}
    </div>
  )
}

// ---------- the terminal ----------

const AUTO_KEY = '221b.watson.auto'
const HISTORY_KEY = '221b.watson.history'

/** Whether reads that arrive from Sherlock or a button run at once. */
export function autoRunReads(): boolean {
  return readJSON<boolean>(AUTO_KEY, true) !== false
}

function useTick(on: boolean, every = 1000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!on) return
    // At once, too: the time may have stood still while nothing ticked.
    const first = setTimeout(() => setNow(Date.now()), 0)
    const t = setInterval(() => setNow(Date.now()), every)
    return () => {
      clearTimeout(first)
      clearInterval(t)
    }
  }, [on, every])
  return now
}

function seconds(ms: number): string {
  const s = Math.max(0, ms) / 1000
  if (s < 10) return `${s.toFixed(1)} s`
  if (s < 120) return `${Math.round(s)} s`
  return `${Math.floor(s / 60)} min ${Math.round(s % 60)} s`
}

function remaining(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000))
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
}

function Lines({ lines }: { lines?: WatsonLine[] }) {
  return (
    <>
      {(lines ?? []).map((l, i) => (
        <div key={i} className="rk-tl" data-k={l.k === 'err' ? 'err' : l.k === 'info' ? 'dim' : undefined}>
          {l.t}
        </div>
      ))}
    </>
  )
}

interface CardProps {
  it: WatsonItem
  cluster: string
  now: number
  onToast: (msg: string) => void
  /** The standing approval (R2-04): the longest allowed in minutes (0: none), when the person's ends, and the handlers. */
  standing?: { max: number; until: string | null; give: (minutes: number) => void; end: () => void }
}

/** Whether a standing approval holds at this moment. */
function standingOn(until: string | null | undefined, now: number): boolean {
  return !!until && Date.parse(until) > now
}

/** An approval card (P6-04), or the Run card of a read that waits. */
function Card({ it, cluster, now, onToast, standing }: CardProps) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(it.command)
  const [busy, setBusy] = useState(false)
  const edit = useRef<HTMLTextAreaElement>(null)
  useEffect(() => {
    if (editing) edit.current?.focus()
  }, [editing])
  const change = it.kind === 'change'
  const plan = it.plan
  // A Toolbox card (TB-36): it opens a session rather than running a line.
  const session = it.session
  const ready = !change || (!!plan?.ready && !plan.dryRunning)
  const left = it.expires ? Date.parse(it.expires) - now : 0
  const act = async (f: () => Promise<unknown>) => {
    setBusy(true)
    try {
      await f()
    } catch (e) {
      onToast(e instanceof WatsonError ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  const save = () =>
    act(async () => {
      await editWatson(cluster, it.id, draft)
      setEditing(false)
    })
  return (
    <div className="rk-appr" data-kind={change ? 'change' : 'read'} data-testid="watson-card" data-id={it.id} data-command={it.command}>
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px 8px' }}>
        <strong style={{ fontSize: 12, fontWeight: 600, color: change ? 'var(--term-warn)' : 'var(--term-fg)' }}>{change ? (it.mine ? 'Waiting for your approval' : `${it.by} asks for approval`) : 'Ready to run'}</strong>
        <span className="rk-ttag" data-k={session ? 'session' : change ? 'change' : 'read'}>
          {session ? 'Opens a session' : change ? 'Changes the cluster' : 'Read-only'}
        </span>
        <span style={{ fontSize: 11.5, color: 'var(--term-dim)' }}>From {it.from}</span>
        {change && it.expires && (
          <span style={{ fontSize: 11.5, color: left < 60_000 ? 'var(--term-warn)' : 'var(--term-dim)', marginLeft: 'auto' }} title="An approval card is good for 10 minutes, and lapses sooner if the objects it acts on change." data-testid="watson-expiry">
            lapses in {remaining(left)}
          </span>
        )}
      </div>
      {editing ? (
        <textarea
          className="rk-tedit"
          aria-label="Edit the command"
          spellCheck={false}
          ref={edit}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Escape') {
              e.stopPropagation()
              setEditing(false)
            } else if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
              e.preventDefault()
              void save()
            }
          }}
          data-testid="watson-edit-text"
        />
      ) : (
        <pre data-testid="watson-card-command">{it.command}</pre>
      )}
      {it.why && <p>{it.why}</p>}
      {change && plan && (
        <>
          {(plan.targets?.length ?? 0) > 0 && (
            <p style={{ marginTop: 6, color: 'var(--term-dim)' }} data-testid="watson-targets">
              Acts on {plan.targets!.join(', ')}
            </p>
          )}
          {(plan.impact?.length ?? 0) > 0 && (
            <ul data-testid="watson-impact">
              {plan.impact!.map((t, i) => (
                <li key={i}>{t}</li>
              ))}
            </ul>
          )}
          {!plan.ready && !plan.dryRunning && (
            <p style={{ marginTop: 6, color: 'var(--term-dim)' }}>
              <span className="rk-spin" aria-hidden="true" style={{ marginRight: 8, verticalAlign: '-1px' }} />
              Working out what it changes…
            </p>
          )}
          {plan.object && (
            <details className="rk-tdry" data-testid="watson-object">
              <summary>What it creates</summary>
              <pre style={{ maxHeight: 260, overflow: 'auto', whiteSpace: 'pre' }}>{plan.object}</pre>
            </details>
          )}
          {(plan.ready || plan.dryRunning) && (
            <details className="rk-tdry" open={plan.dryRunExit !== 0 || (plan.dryRun?.length ?? 0) <= 6} data-testid="watson-dryrun">
              <summary>
                {session && !plan.object ? 'Access review' : 'Server dry run'}
                {plan.dryRunning ? ': running' : plan.dryRunExit === 0 ? (session && !plan.object ? ': allowed' : ': nothing changed, no error') : session && !plan.object ? ': not allowed' : `: exit code ${plan.dryRunExit}`}
                {plan.dryRunning && <span className="rk-spin" aria-hidden="true" style={{ marginLeft: 8, verticalAlign: '-1px' }} />}
              </summary>
              <div className="rk-tdry-out">
                <Lines lines={plan.dryRun} />
              </div>
              {plan.dryRunNote && <p style={{ color: 'var(--term-dim)', fontSize: 11.5 }}>{plan.dryRunNote}</p>}
            </details>
          )}
        </>
      )}
      {it.waiting && (
        <p style={{ marginTop: 6, color: 'var(--term-warn)' }} data-testid="watson-waiting">
          {it.waiting}
        </p>
      )}
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, marginTop: 10 }}>
        {editing ? (
          <>
            <button className="rk-tbtn" data-primary="true" onClick={() => void save()} disabled={busy || !draft.trim()} data-testid="watson-save">
              Save
            </button>
            <button className="rk-tbtn" onClick={() => setEditing(false)}>
              Cancel
            </button>
          </>
        ) : (
          <>
            {(it.canApprove || !it.waiting) && (
              <button className="rk-tbtn" data-primary="true" onClick={() => void act(() => approveWatson(cluster, it.id))} disabled={busy || !ready || !it.canApprove} data-testid="watson-approve" title={ready ? undefined : 'Watson is still working out what it changes'}>
                {session ? 'Approve and open' : change ? 'Approve and run' : 'Run'}
              </button>
            )}
            {change && it.canApprove && standing && standing.max > 0 && (standingOn(standing.until, now) ? (
              <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }} data-testid="watson-standing">
                <span className="rk-tl" data-k="warn" style={{ margin: 0 }} title="Every card you could approve is approved as it is ready, until then">
                  approving all until {fmtClock(standing.until!)}
                </span>
                <button className="rk-tbtn" onClick={standing.end} disabled={busy} data-testid="watson-standing-end">
                  Stop
                </button>
              </span>
            ) : (
              <select
                className="rk-tbtn"
                value=""
                onChange={(e) => e.target.value && standing.give(Number(e.target.value))}
                disabled={busy}
                aria-label="Approve all for"
                title="Approves this card and every card you could approve, as each is ready, for the next minutes. An administrator sets the longest in Settings."
                data-testid="watson-approve-all"
              >
                <option value="">Approve all for…</option>
                {APPROVE_ALL_CHOICES.filter((m) => m <= standing.max).map((m) => (
                  <option key={m} value={m}>
                    {m < 60 ? `${m} minutes` : m === 60 ? '1 hour' : `${m / 60} hours`}
                  </option>
                ))}
              </select>
            ))}
            {it.mine && (
              <>
                {!session && (
                  <button className="rk-tbtn" onClick={() => setEditing(true)} disabled={busy} data-testid="watson-edit">
                    Edit
                  </button>
                )}
                <button className="rk-tbtn" onClick={() => void act(() => discardWatson(cluster, it.id))} disabled={busy} data-testid="watson-discard">
                  Discard
                </button>
              </>
            )}
          </>
        )}
      </div>
    </div>
  )
}

/** A command that ran, runs, or will not run. */
function Ran({ it, prompt, cluster, now, onToast, onAgain }: { it: WatsonItem; prompt: string; cluster: string; now: number; onToast: (m: string) => void; onAgain: (cmd: string) => void }) {
  const running = it.status === 'running' || it.status === 'queued'
  const toolbox = useToolbox()
  if (it.status === 'discarded') {
    return (
      <div className="rk-tl" data-k="dim" style={{ paddingBottom: 8 }} data-testid="watson-discarded">
        # discarded: {it.command}
      </div>
    )
  }
  if (it.status === 'refused' || it.status === 'lapsed') {
    return (
      <div className="rk-tblock" data-testid={`watson-${it.status}`} data-id={it.id}>
        <div className="rk-tprompt">
          <b>{prompt}</b>
          <span>{it.command}</span>
        </div>
        <div className="rk-tl" data-k={it.status === 'refused' ? 'err' : 'warn'} data-testid="watson-reason">
          {it.reason}
        </div>
        <div className="rk-tstat" data-k="stop">
          <b>{it.status === 'refused' ? '✗' : '⏱'}</b>
          <span>{it.status === 'refused' ? `refused${it.rule ? ` · ${it.rule}` : ''} · nothing ran` : 'lapsed · nothing ran'}</span>
          {it.status === 'lapsed' && it.mine && !it.session && (
            <button className="rk-tbtn" onClick={() => onAgain(it.command)} style={{ height: 22, padding: '0 8px', fontSize: 11.5 }} data-testid="watson-again">
              New card
            </button>
          )}
        </div>
      </div>
    )
  }
  const dur = it.started ? (it.finished ? Date.parse(it.finished) : now) - Date.parse(it.started) : 0
  const status =
    it.status === 'queued'
      ? 'queued — runs after the command before it'
      : it.status === 'running'
        ? `running · ${seconds(dur)}`
        : it.status === 'stopped'
          ? `stopped after ${seconds(dur)}`
          : `${it.exit ? `exit code ${it.exit}` : 'done'} · ${seconds(dur)}${it.approver && it.kind === 'change' ? ` · approved by ${it.approver}` : ''}`
  return (
    <div className="rk-tblock" data-testid="watson-ran" data-id={it.id} data-status={it.status}>
      <div className="rk-tprompt">
        <b>{prompt}</b>
        <span>{it.command}</span>
      </div>
      <div data-testid="watson-output">
        <Lines lines={it.lines} />
      </div>
      {running ? (
        <div className="rk-tstat">
          <span className="rk-spin" aria-hidden="true" />
          <span>{status}</span>
          <button className="rk-tbtn" onClick={() => void stopWatson(cluster, it.id).catch((e: unknown) => onToast(e instanceof Error ? e.message : String(e)))} style={{ height: 22, padding: '0 8px', fontSize: 11.5 }} data-testid="watson-stop">
            Stop
          </button>
        </div>
      ) : (
        <div className="rk-tstat" data-k={it.status === 'stopped' ? 'stop' : it.exit ? 'err' : 'ok'} data-testid="watson-status">
          <b>{it.status === 'stopped' ? '^C' : it.exit ? '✗' : '✓'}</b>
          <span>{status}</span>
          {it.session?.sessionId && toolbox && (
            <button className="rk-tbtn" onClick={() => toolbox.showSession(it.session!.sessionId!)} style={{ height: 22, padding: '0 8px', fontSize: 11.5 }} data-testid="watson-open-session">
              Open session
            </button>
          )}
        </div>
      )}
    </div>
  )
}

interface PaneProps {
  cluster: string
  clusterName: string
  state: WatsonState
  /** The pane's controls, when docked or alone in a window. */
  controls?: ReactNode
  /** The Sherlock and Watson switch on narrow screens. */
  switcher?: ReactNode
  onToast: (msg: string) => void
}

/** Watson's pane (P6-01): a terminal of commands, cards and output. */
export function WatsonPane({ cluster, clusterName, state, controls, switcher, onToast }: PaneProps) {
  const status = state.status
  const items = state.items
  const [draft, setDraft] = useState('')
  const [auto, setAuto] = useState(autoRunReads)
  const [hist, setHist] = useState<string[]>(() => readJSON<string[]>(HISTORY_KEY, []))
  const [pos, setPos] = useState(-1)
  const scroller = useRef<HTMLDivElement>(null)
  const pinned = useRef(true)
  const pending = items.filter((x) => x.status === 'pending')
  const rest = items.filter((x) => x.status !== 'pending')
  const [standing, setStanding] = useState<{ cluster: string; until: string | null } | null>(null)
  const until = standing?.cluster === cluster ? standing.until : (status?.approvedUntil ?? null)
  const now = useTick(items.some((x) => x.status === 'running' || (x.status === 'pending' && x.kind === 'change')) || !!until)
  const prompt = status?.prompt ?? `watson@${clusterName}:~$`
  const waiting = pending.filter((x) => x.kind === 'change' && x.canApprove).length
  // A standing approval (R2-04): given here, or found in the status on
  // opening; it ends on its own or with Stop.
  const approveAllMax = status?.approveAllMax ?? 0
  const approveAll = async (minutes: number) => {
    try {
      const r = await approveAllWatson(cluster, minutes)
      setStanding({ cluster, until: r.until })
    } catch (e) {
      onToast(e instanceof WatsonError ? e.message : String(e))
    }
  }
  const endApproveAll = async () => {
    try {
      await endApproveAllWatson(cluster)
      setStanding({ cluster, until: null })
    } catch (e) {
      onToast(e instanceof WatsonError ? e.message : String(e))
    }
  }

  useLayoutEffect(() => {
    const el = scroller.current
    if (el && pinned.current) el.scrollTop = el.scrollHeight
  })

  const submit = async (text: string) => {
    const cmd = text.trim()
    if (!cmd) return
    pinned.current = true
    const next = [...hist.filter((h) => h !== cmd), cmd].slice(-50)
    setHist(next)
    writeJSON(HISTORY_KEY, next)
    setPos(-1)
    setDraft('')
    try {
      // Typed reads run at once (P6-01); changes wait for their card.
      await sendToWatson(cluster, cmd, { from: 'Typed', run: true })
    } catch (e) {
      onToast(e instanceof WatsonError ? e.message : String(e))
      setDraft(cmd)
    }
  }
  const clear = async () => {
    try {
      await clearWatson(cluster)
    } catch (e) {
      onToast(e instanceof Error ? e.message : String(e))
    }
  }
  const finishedCount = rest.filter((x) => x.status !== 'running' && x.status !== 'queued').length
  return (
    <section className="rk-dpane rk-wat" aria-label="Watson" data-testid="watson">
      <div className="rk-dph">
        <span className={'rk-avatar' + (switcher ? ' rk-wide' : '')} data-k="w" aria-hidden="true">
          <WatsonIcon />
        </span>
        <h2 className={switcher ? 'rk-wide' : undefined}>Watson</h2>
        <small className="rk-wide">Runs commands once you approve them</small>
        {switcher}
        {waiting > 0 && (
          <span className="rk-count rk-wide" data-tone="warn" style={{ flex: 'none', whiteSpace: 'nowrap' }} data-testid="watson-waiting-count">
            {waiting} waiting
          </span>
        )}
        <span style={{ flex: '1 1 auto' }} />
        <label className="rk-tcheck rk-wide" title="Read-only commands from Sherlock and buttons, such as get, describe and logs, run as soon as they arrive; changes still wait for an approval">
          <input
            type="checkbox"
            checked={auto}
            onChange={(e) => {
              setAuto(e.target.checked)
              writeJSON(AUTO_KEY, e.target.checked)
            }}
            data-testid="watson-auto"
          />
          <span>Auto-run reads</span>
        </label>
        {standingOn(until, now) && (
          <span className="rk-wide" style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 12, whiteSpace: 'nowrap' }} data-testid="watson-standing-header">
            <span className="rk-pill" data-tone="warn" title="Every card you could approve is approved as it is ready, until then">
              <span className="rk-dot" />
              approving all until {fmtClock(until!)}
            </span>
            <button className="rk-btn" onClick={() => void endApproveAll()} style={{ height: 28 }} data-testid="watson-standing-end-header">
              Stop
            </button>
          </span>
        )}
        {finishedCount > 0 && (
          <button className="rk-btn" onClick={() => void clear()} title="Remove finished commands from the console" style={{ height: 28 }} data-testid="watson-clear">
            Clear
          </button>
        )}
        {controls}
      </div>
      <div
        className="rk-term"
        ref={scroller}
        role="log"
        aria-label="Watson console"
        // Scrollable with the keyboard, even when it holds only output.
        tabIndex={0}
        onScroll={(e) => {
          const el = e.currentTarget
          pinned.current = el.scrollTop + el.clientHeight >= el.scrollHeight - 48
        }}
        data-testid="watson-log"
      >
        {items.length === 0 && (
          <div data-testid="watson-empty">
            <div className="rk-tl" data-k="head">
              Watson · kubectl and helm on {clusterName}
            </div>
            <div className="rk-tl" data-k="dim">
              Reads run at once. Changes wait here on a card until someone approves them, and run as that person. They come from Sherlock’s answers or from the line below.
            </div>
            {status?.readOnly && (
              <div className="rk-tl" data-k="warn" data-testid="watson-readonly">
                {clusterName} is connected read-only: Watson runs reads here and refuses changes. {status.how}
              </div>
            )}
            {status?.twoPerson && (
              <div className="rk-tl" data-k="warn">
                Changes to this production cluster need a second person’s approval.
              </div>
            )}
            {status && (
              <>
                <div className="rk-tl" data-k="dim">
                  Reads: {status.reads.join(', ')}.
                </div>
                <div className="rk-tl" data-k="dim">
                  Changes: {status.changes.join(', ')}.
                </div>
              </>
            )}
            <div className="rk-tl" data-k="dim">
              Try: kubectl get nodes
            </div>
          </div>
        )}
        {rest.map((it) => (
          <Ran key={it.id} it={it} prompt={prompt} cluster={cluster} now={now} onToast={onToast} onAgain={(cmd) => void sendToWatson(cluster, cmd, { from: 'Typed again' }).catch((e: unknown) => onToast(String(e)))} />
        ))}
        {pending.map((it) => (
          <Card key={it.id} it={it} cluster={cluster} now={now} onToast={onToast} standing={status?.readOnly ? undefined : { max: approveAllMax, until, give: (m) => void approveAll(m), end: () => void endApproveAll() }} />
        ))}
      </div>
      <div className="rk-tin">
        <span aria-hidden="true">$</span>
        <input
          type="text"
          aria-label="Command for Watson"
          placeholder="Type a kubectl or helm command and press Enter"
          spellCheck={false}
          autoComplete="off"
          value={draft}
          maxLength={4000}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !e.nativeEvent.isComposing) {
              e.preventDefault()
              void submit(draft)
            } else if (e.key === 'ArrowUp' && hist.length) {
              e.preventDefault()
              const p = pos < 0 ? hist.length - 1 : Math.max(0, pos - 1)
              setPos(p)
              setDraft(hist[p] ?? '')
            } else if (e.key === 'ArrowDown' && pos >= 0) {
              e.preventDefault()
              const p = pos + 1
              if (p >= hist.length) {
                setPos(-1)
                setDraft('')
              } else {
                setPos(p)
                setDraft(hist[p] ?? '')
              }
            }
          }}
          data-testid="watson-input"
        />
        <button className="rk-tbtn" onClick={() => void submit(draft)} disabled={!draft.trim()} data-testid="watson-add">
          Add
        </button>
      </div>
    </section>
  )
}

import { useEffect, useState, type ReactNode } from 'react'
import { clock, endedWords, endSession, extendSession, fBytes, keepSession, sessionOutputHref, tabLabel, toolLabel, transcriptHref, ToolboxError, type SessionView } from '../../api/toolbox'
import { copyText } from '../../lib/clipboard'
import { fmtClock, fmtTs } from '../../lib/format'
import { CopyIcon, DownloadIcon } from '../Icons'
import { Terminal } from './Terminal'
import { ConfirmDialog } from '../ConfirmDialog'

/** An administrator ends someone else's session with a reason they see (TB-12). */
function EndOther({ cluster, s, onDone, onToast }: { cluster: string; s: SessionView; onDone: () => void; onToast: (m: string) => void }) {
  const [why, setWhy] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  return (
    <ConfirmDialog
      title={`End ${s.owner}’s session?`}
      confirm="End it"
      danger
      busy={busy}
      error={err}
      onCancel={onDone}
      onConfirm={() => {
        if (why.trim().length < 5) {
          setErr('Say why: they see it.')
          return
        }
        setBusy(true)
        endSession(cluster, s.id, why.trim()).then(
          () => {
            onToast(`Ended ${s.owner}’s session.`)
            onDone()
          },
          (e: unknown) => {
            setBusy(false)
            setErr(e instanceof Error ? e.message : String(e))
          },
        )
      }}
    >
      <span>
        <span className="rk-mono">{s.summary}</span>. They see your reason where the session was; the audit log keeps it.
      </span>
      <textarea className="rk-area" value={why} onChange={(e) => setWhy(e.target.value)} aria-label="Why" placeholder="Why it ends" />
    </ConfirmDialog>
  )
}

/** Ticks every second while mounted. */
function useTick(on = true): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!on) return
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [on])
  return now
}

/** Seconds left before a session ends for no activity, and before its hard limit. */
export function remaining(s: SessionView, now: number): { idle: number; limit: number } {
  const idle = (Date.parse(s.lastActive) + s.idleSeconds * 1000 - now) / 1000
  const limit = s.ends ? (Date.parse(s.ends) - now) / 1000 : Infinity
  return { idle, limit }
}

/** A session's state for its tab: live, idle (ending soon), starting, ended. */
export function tabState(s: SessionView, now: number): 'live' | 'idle' | 'starting' | 'ended' {
  if (s.state === 'ended') return 'ended'
  if (s.state === 'starting') return 'starting'
  return remaining(s, now).idle < 120 ? 'idle' : 'live'
}

interface PaneProps {
  cluster: string
  session: SessionView
  /** The viewer is a SuperAdmin: may watch and end others' sessions. */
  admin: boolean
  dark: boolean
  controls: ReactNode
  onToast: (m: string) => void
}

/** One session's pane in the dock (TB-10, TB-11, TB-31). */
export function SessionPane({ cluster, session: s, admin, dark, controls, onToast }: PaneProps) {
  const now = useTick(s.state !== 'ended')
  const [busy, setBusy] = useState(false)
  const [watching, setWatching] = useState(false)
  const [asking, setAsking] = useState(false)
  const act = (p: Promise<unknown>, done?: string) => {
    setBusy(true)
    p.then(
      () => {
        setBusy(false)
        if (done) onToast(done)
      },
      (e: unknown) => {
        setBusy(false)
        onToast(e instanceof ToolboxError ? e.message : String(e))
      },
    )
  }
  const end = () => {
    if (s.mine) act(endSession(cluster, s.id), 'Ended.')
    else setAsking(true)
  }
  const { idle, limit } = remaining(s, now)
  const live = s.state !== 'ended'
  const header = (
    <div className="rk-dph" style={{ flexWrap: 'wrap' }}>
      <h2>{s.tool === 'forward' ? 'Port forward' : toolLabel(s.tool)}</h2>
      <small className="rk-mono" title={s.summary}>
        {s.tool === 'node-shell' || s.tool === 'node-command' ? `node ${s.request.node}${s.pod ? ' · pod ' + s.pod : ''}` : `${s.namespace ?? s.request.namespace}/${s.pod ?? s.request.pod ?? ''}${s.container ? ' · ' + s.container : ''}`}
      </small>
      <span style={{ flex: '1 1 auto' }} />
      {!s.mine && <span className="rk-pill">{s.owner}’s</span>}
      {s.approver && <span className="rk-pill" data-tone="info">Approved by {s.approver}</span>}
      {live && s.ends && (
        <span className="rk-pill" title={`The hard limit: ${fmtTs(s.ends)}`}>
          Ends {fmtClock(s.ends)} · {Math.max(0, Math.round(limit / 60))} min
        </span>
      )}
      {s.recorded && <span className="rk-pill">Recorded</span>}
      {(s.watchers?.length ?? 0) > 0 && <span className="rk-pill" data-tone="warn">Watched by {s.watchers!.join(', ')}</span>}
      {s.recorded && (s.mine || admin) && (
        <a className="rk-btn" href={transcriptHref(cluster, s.id)} download>
          <DownloadIcon />
          Transcript
        </a>
      )}
      {live && s.mine && s.ends && limit < 15 * 60 && (
        <button className="rk-btn" disabled={busy} onClick={() => act(extendSession(cluster, s.id), 'Extended.')} title="Ask for more time, up to the console's limit">
          Extend
        </button>
      )}
      {live && !s.mine && admin && s.interactive && (
        <button className="rk-btn" aria-pressed={watching} onClick={() => setWatching((w) => !w)} title="Watch this terminal, read-only; its owner sees that you watch">
          {watching ? 'Stop watching' : 'Watch'}
        </button>
      )}
      {live && (s.mine || admin) && (
        <button className="rk-btn" data-danger="true" disabled={busy} onClick={end} data-testid="session-end">
          End
        </button>
      )}
      {controls}
    </div>
  )
  const notes = (s.notes?.length ?? 0) > 0 && (
    <div style={{ padding: '6px 14px', borderBottom: '1px solid var(--line)', fontSize: 12, color: 'var(--fg3)', display: 'flex', flexDirection: 'column', gap: 2 }}>
      {s.notes!.map((n) => (
        <span key={n}>{n}</span>
      ))}
    </div>
  )
  let body: ReactNode
  if (s.tool === 'forward') body = <ForwardBody s={s} now={now} />
  else if (s.tool === 'command' || s.tool === 'node-command') body = <CommandBody cluster={cluster} s={s} />
  else if (s.state === 'starting')
    body = (
      <div className="tb-output" style={{ color: 'var(--term-dim)' }}>
        Opening… {s.tool === 'debug-pod' || s.tool === 'node-shell' ? 'waiting for the pod to run.' : s.tool === 'debug-container' ? 'waiting for the debug container to start.' : ''}
      </div>
    )
  else if (s.mine || watching) body = <Terminal key={s.id + (watching ? 'w' : '')} cluster={cluster} id={s.id} watch={!s.mine} dark={dark} onInput={() => undefined} />
  else
    body = (
      <div className="tb-output" style={{ color: 'var(--term-dim)' }}>
        {s.owner}’s session. {admin ? 'Watch shows it, read-only; they see that you watch.' : ''}
      </div>
    )
  const showIdle = live && s.state === 'live' && s.mine && idle < 120
  return (
    <div className="tb-pane" data-testid="session-pane">
      {asking && <EndOther cluster={cluster} s={s} onDone={() => setAsking(false)} onToast={onToast} />}
      {header}
      {notes}
      {body}
      {showIdle && (
        <div className="tb-idle" role="status">
          <span>
            No activity for {Math.max(1, Math.round((s.idleSeconds - idle) / 60))} min · the session ends in {clock(idle)}
            {s.interactive && s.tool !== 'forward' ? ' · any key keeps it' : ''}
          </span>
          <button className="rk-tbtn" onClick={() => act(keepSession(cluster, s.id))}>
            Keep
          </button>
        </div>
      )}
      {!live && (
        <div className="tb-idle" data-k="end">
          <span>
            {endedWords(s)}
            {s.ended ? ` · ${fmtClock(s.ended)}` : ''}
          </span>
        </div>
      )}
    </div>
  )
}

function ForwardBody({ s, now }: { s: SessionView; now: number }) {
  const f = s.forward
  const [copied, setCopied] = useState(false)
  const connected = !!f?.cli
  const { idle } = remaining(s, now)
  const pctIdle = Math.max(0, Math.min(100, (idle / s.idleSeconds) * 100))
  return (
    <div className="tb-pane-body">
      <div className="rk-steps">
        <span data-status="done">✓ Approved{s.approver ? ` by ${s.approver}` : ''}</span>
        <span data-status={connected ? 'done' : s.state === 'ended' ? undefined : 'now'}>{connected ? `✓ 221b connected from ${f!.cli}` : '● Run the command on your computer'}</span>
        {connected && f?.listen && <span data-status="done">✓ Listening on {f.listen}: point any program there</span>}
      </div>
      {s.cli && s.state !== 'ended' && (
        <div className="tb-cmd">
          <code>{s.cli}</code>
          <button
            className="rk-btn"
            onClick={() => {
              void copyText(s.cli!).then((ok) => {
                setCopied(ok)
                setTimeout(() => setCopied(false), 1500)
              })
            }}
          >
            <CopyIcon />
            {copied ? 'Copied' : 'Copy'}
          </button>
        </div>
      )}
      {s.cli && !connected && s.state !== 'ended' && (
        <p className="tb-hint" style={{ margin: 0 }}>
          The <span className="rk-mono">221b</span> command comes with each release; the token in it is this forward’s alone and ends with it. The session waits 5 minutes for it.
        </p>
      )}
      <div className="tb-stats">
        <div className="tb-stat">
          <span>Connections</span>
          <b>
            {f?.open ?? 0} open · {f?.total ?? 0}
          </b>
        </div>
        <div className="tb-stat">
          <span>To the pod</span>
          <b>{fBytes(f?.bytesIn ?? 0)}</b>
        </div>
        <div className="tb-stat">
          <span>From the pod</span>
          <b>{fBytes(f?.bytesOut ?? 0)}</b>
        </div>
        <div className="tb-stat">
          <span>Last traffic</span>
          <b>{f?.last ? fmtClock(f.last, { seconds: true }) : '—'}</b>
        </div>
      </div>
      {s.state === 'live' && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          <span className="tb-hint">
            Ends after {Math.round(s.idleSeconds / 60)} min without traffic: {clock(idle)} left
          </span>
          <span className="tb-meter" data-tone={idle < 120 ? 'warn' : undefined}>
            <span style={{ width: `${pctIdle}%` }} />
          </span>
        </div>
      )}
      {(f?.conns?.length ?? 0) > 0 && (
        <table className="tb-table">
          <thead>
            <tr>
              <th>#</th>
              <th>From</th>
              <th>Opened</th>
              <th>Closed</th>
              <th className="rk-num">Bytes</th>
            </tr>
          </thead>
          <tbody>
            {f!.conns!
              .slice()
              .reverse()
              .map((c) => (
                <tr key={c.id}>
                  <td className="rk-mono">{c.id}</td>
                  <td className="rk-mono">{c.from || '—'}</td>
                  <td>{fmtClock(c.opened, { seconds: true })}</td>
                  <td>{c.closed && !c.closed.startsWith('0001') ? fmtClock(c.closed, { seconds: true }) : <span className="rk-t" data-tone="ok">open</span>}</td>
                  <td className="rk-num">{fBytes(c.bytes)}</td>
                </tr>
              ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

function CommandBody({ cluster, s }: { cluster: string; s: SessionView }) {
  const [out, setOut] = useState('')
  useEffect(() => {
    if (s.state !== 'ended') return
    let live = true
    fetch(sessionOutputHref(cluster, s.id))
      .then((r) => (r.ok ? r.text() : ''))
      .then((t) => live && setOut(t))
      .catch(() => undefined)
    return () => {
      live = false
    }
  }, [cluster, s.id, s.state])
  return (
    <pre className="tb-output" data-testid="session-output">
      <span style={{ color: 'var(--term-dim)' }}>$ {s.request.command}</span>
      {'\n'}
      {s.state === 'ended' ? out || (s.endedBy === 'failed' ? '' : '(no output)') : 'Running…'}
      {s.state === 'ended' && s.exit !== undefined && s.exit !== 0 && <span style={{ color: 'var(--term-err)' }}>{`\nexit ${s.exit}`}</span>}
    </pre>
  )
}

interface ListProps {
  sessions: SessionView[]
  admin: boolean
  controls: ReactNode
  onOpen: (id: string) => void
  onToast: (m: string) => void
  cluster: string
}

/** Every session the person sees on the cluster (TB-09): theirs, and for a SuperAdmin everyone's. */
export function SessionsPane({ sessions, admin, controls, onOpen, onToast, cluster }: ListProps) {
  const now = useTick()
  const live = sessions.filter((s) => s.state !== 'ended')
  const [ending, setEnding] = useState<SessionView | null>(null)
  return (
    <div className="tb-pane" data-testid="sessions-pane">
      {ending && <EndOther cluster={cluster} s={ending} onDone={() => setEnding(null)} onToast={onToast} />}
      <div className="rk-dph">
        <h2>Sessions</h2>
        <small>
          {live.length} open{admin ? ' · everyone’s, as a SuperAdmin sees them' : ''} · ended ones stay an hour; Administration › Sessions keeps the history
        </small>
        <span style={{ flex: '1 1 auto' }} />
        {controls}
      </div>
      <div className="tb-pane-body" style={{ padding: 0 }}>
        {sessions.length === 0 ? (
          <p className="tb-hint" style={{ padding: '14px 16px', margin: 0 }}>
            No sessions. The Tools button beside a pod or a node opens one, through Watson.
          </p>
        ) : (
          <table className="tb-table">
            <thead>
              <tr>
                <th>Session</th>
                <th>Owner</th>
                <th>State</th>
                <th>Started</th>
                <th>Ends</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {sessions.map((s) => {
                const st = tabState(s, now)
                const { idle, limit } = remaining(s, now)
                return (
                  <tr key={s.id}>
                    <td>
                      <button className="rk-link rk-mono" onClick={() => onOpen(s.id)} style={{ fontSize: 12 }}>
                        {tabLabel(s)}
                      </button>
                      <span className="tb-sub">{s.summary}</span>
                    </td>
                    <td>
                      {s.owner}
                      {s.approver && <span className="tb-sub">approved by {s.approver}</span>}
                    </td>
                    <td>
                      <span className="rk-t" data-tone={st === 'ended' ? 'muted' : st === 'live' ? 'ok' : 'warn'}>
                        {st === 'ended' ? endedWords(s) : st === 'idle' ? `idle · ends in ${clock(idle)}` : st}
                      </span>
                    </td>
                    <td>{s.started ? fmtClock(s.started) : '—'}</td>
                    <td>{s.state === 'ended' ? (s.ended ? fmtClock(s.ended) : '—') : s.ends ? `${fmtClock(s.ends)} · ${Math.round(limit / 60)} min` : '—'}</td>
                    <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                      <button className="rk-btn" onClick={() => onOpen(s.id)}>
                        Open
                      </button>{' '}
                      {s.state !== 'ended' && (s.mine || admin) && (
                        <button
                          className="rk-btn"
                          data-danger="true"
                          onClick={() => {
                            if (!s.mine) return setEnding(s)
                            endSession(cluster, s.id).then(
                              () => onToast('Ended.'),
                              (e: unknown) => onToast(e instanceof Error ? e.message : String(e)),
                            )
                          }}
                        >
                          End
                        </button>
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </div>
    </div>
  )
}
